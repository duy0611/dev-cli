package k8s

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// stubs writes fake executables onto a temporary PATH. Each records its argv,
// one argument per line, and its stdin.
type stubs struct{ dir string }

// newStubs replaces PATH with a directory of the test's own.
//
// Replaces, not precedes: a stub PATH that still ends in the host's leaves
// every binary the test did not install resolving to the operator's own. The
// builder probes docker and podman, so a machine with podman installed ran the
// real one — pushing to a real registry from a unit test, and making "no docker
// on PATH" untrue. A test that wants a host binary asks for it by name.
func newStubs(t *testing.T) *stubs {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	return &stubs{dir: dir}
}

// install writes a stub that records its argv and ignores stdin.
func (s *stubs) install(t *testing.T, name, stdout string, code int) {
	t.Helper()
	s.write(t, name, stdout, code, false)
}

// installReadingStdin also captures stdin.
//
// Separate because reading stdin is not free: a stub that always cats will hang
// forever when the caller leaves the pipe open, which is exactly what the
// devcontainer CLI does to the docker it spawns. Only the tests that write and
// close stdin themselves may use this.
func (s *stubs) installReadingStdin(t *testing.T, name, stdout string, code int) {
	t.Helper()
	s.write(t, name, stdout, code, true)
}

func (s *stubs) write(t *testing.T, name, stdout string, code int, readStdin bool) {
	t.Helper()
	argv := filepath.Join(s.dir, name+".argv")
	stdin := filepath.Join(s.dir, name+".stdin")

	script := "#!/bin/sh\n" +
		": > '" + argv + "'\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> '" + argv + "'; done\n"
	if readStdin {
		// Absolute: PATH holds nothing but this directory, by design.
		script += "/bin/cat > '" + stdin + "'\n"
	}
	if stdout != "" {
		script += "printf '%s' '" + stdout + "'\n"
	}
	script += "exit " + itoa(code) + "\n"

	if err := os.WriteFile(filepath.Join(s.dir, name), []byte(script), 0o755); err != nil {
		t.Fatalf("writing stub %s: %v", name, err)
	}
}

// installScript writes a stub whose body decides what to print, and which
// appends every invocation to a log so a test can see all of them. The
// single-call argv helper truncates, which loses everything but the last.
func (s *stubs) installScript(t *testing.T, name, body string) {
	t.Helper()
	log := filepath.Join(s.dir, name+".log")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> '" + log + "'; done\n" +
		"printf -- '---\\n' >> '" + log + "'\n" +
		body + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(s.dir, name), []byte(script), 0o755); err != nil {
		t.Fatalf("writing stub %s: %v", name, err)
	}
}

// installKubectl writes a logging kubectl stub that drains stdin first, into a
// file named after the subcommand that received it.
//
// Draining matters because seedWorkspace streams a tar into `kubectl exec`: a
// stub that exits without reading took none of the archive, which pipeInto
// reports rather than ignores. Harmless for the calls that send nothing —
// os/exec gives those /dev/null, which cat reaches the end of at once.
//
// Per subcommand, because one command runs several kubectl calls and a single
// file would hold whichever spoke last. The leading flags are skipped in pairs:
// args() only ever emits --context and --namespace, and both carry a value.
func (s *stubs) installKubectl(t *testing.T, body string) {
	t.Helper()
	capture := "sub=stdin\n" +
		"for a in \"$@\"; do\n" +
		"  case \"$a\" in\n" +
		"    --context|--namespace) skip=1 ;;\n" +
		"    *) if [ -z \"$skip\" ]; then sub=$a; break; fi; skip= ;;\n" +
		"  esac\n" +
		"done\n" +
		"/bin/cat > '" + s.dir + "/" + kubectlBin + ".'\"$sub\"'.stdin'\n"
	s.installScript(t, kubectlBin, capture+body)
}

// stdinOf returns what the logging stub was given on stdin for one subcommand.
func (s *stubs) stdinOf(t *testing.T, name, subcommand string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.dir, name+"."+subcommand+".stdin"))
	if err != nil {
		t.Fatalf("stub %s got no stdin for %s: %v", name, subcommand, err)
	}
	return string(b)
}

// calls returns each invocation of a logging stub, joined into one string.
func (s *stubs) calls(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.dir, name+".log"))
	if err != nil {
		t.Fatalf("stub %s was never called: %v", name, err)
	}
	var out []string
	var current []string
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case line == "---":
			out = append(out, strings.Join(current, " "))
			current = nil
		case line != "":
			current = append(current, line)
		}
	}
	return out
}

func (s *stubs) argv(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.dir, name+".argv"))
	if err != nil {
		t.Fatalf("stub %s was never called: %v", name, err)
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func (s *stubs) stdin(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.dir, name+".stdin"))
	if err != nil {
		t.Fatalf("stub %s read no stdin: %v", name, err)
	}
	return string(b)
}

func itoa(n int) string {
	if n < 0 || n > 9 {
		panic("stub exit codes are single digits")
	}
	return string(rune('0' + n))
}

func hasPair(argv []string, a, b string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == a && argv[i+1] == b {
			return true
		}
	}
	return false
}

func testKubectl() kubectl {
	return newKubectl(Config{Context: "prod", Namespace: "sandboxes"})
}

func TestNewKubectlTakesContextAndNamespaceFromConfig(t *testing.T) {
	k := newKubectl(Config{Context: "prod", Namespace: "sandboxes", Registry: "r"})
	if k.context != "prod" || k.namespace != "sandboxes" {
		t.Errorf("newKubectl = %+v, want the config's context and namespace", k)
	}
}

// The invariant of this file: a call that reaches the wrong cluster does not
// fail, it succeeds somewhere else. Every entry point must carry both.
func TestEveryCallCarriesContextAndNamespace(t *testing.T) {
	k := testKubectl()

	calls := map[string]func() error{
		"run":    func() error { return k.run(context.Background(), "get", "pods") },
		"output": func() error { _, err := k.output(context.Background(), "get", "pods"); return err },
		"stream": func() error {
			return k.stream(context.Background(), streamOpts{Stdout: io.Discard}, "exec", "x")
		},
		"apply":  func() error { return k.apply(context.Background(), []byte("{}")) },
		"delete": func() error { return k.deleteIgnoringMissing(context.Background(), "deployment", "x") },
		"pipeInto": func() error {
			return k.pipeInto(context.Background(), func(w io.Writer) error { return nil }, "exec", "x")
		},
	}

	for name, call := range calls {
		s := newStubs(t)
		s.install(t, kubectlBin, "", 0)

		if err := call(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		argv := s.argv(t, kubectlBin)
		if !hasPair(argv, "--context", "prod") {
			t.Errorf("%s: argv %v is missing --context", name, argv)
		}
		if !hasPair(argv, "--namespace", "sandboxes") {
			t.Errorf("%s: argv %v is missing --namespace", name, argv)
		}
	}
}

// An empty context means "whatever kubeconfig selected". Passing --context ""
// would instead select a context named "", which does not exist.
func TestEmptyContextIsOmitted(t *testing.T) {
	s := newStubs(t)
	s.install(t, kubectlBin, "", 0)

	k := kubectl{namespace: "sandboxes"}
	if err := k.run(context.Background(), "get", "pods"); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, a := range s.argv(t, kubectlBin) {
		if a == "--context" {
			t.Errorf("argv %v passed --context with no value configured", s.argv(t, kubectlBin))
		}
	}
}

func TestApplySendsTheManifestOnStdin(t *testing.T) {
	s := newStubs(t)
	s.installReadingStdin(t, kubectlBin, "", 0)

	manifest := `{"kind":"Deployment"}`
	if err := testKubectl().apply(context.Background(), []byte(manifest)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	argv := s.argv(t, kubectlBin)
	if !hasPair(argv, "-f", "-") {
		t.Errorf("argv %v does not read the manifest from stdin", argv)
	}
	// Server-side, so a field this tool does not set is left alone on update
	// rather than stripped.
	if !contains(argv, "--server-side") {
		t.Errorf("argv %v is not a server-side apply", argv)
	}
	if got := s.stdin(t, kubectlBin); got != manifest {
		t.Errorf("stdin = %q, want %q", got, manifest)
	}
}

func TestDeleteIgnoresMissing(t *testing.T) {
	s := newStubs(t)
	s.install(t, kubectlBin, "", 0)

	if err := testKubectl().deleteIgnoringMissing(context.Background(), "deployment", "dev-x"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !contains(s.argv(t, kubectlBin), "--ignore-not-found") {
		t.Errorf("argv %v would fail on an already-deleted object", s.argv(t, kubectlBin))
	}
}

func TestPipeIntoStreamsTheProducer(t *testing.T) {
	s := newStubs(t)
	s.installReadingStdin(t, kubectlBin, "", 0)

	err := testKubectl().pipeInto(context.Background(), func(w io.Writer) error {
		_, err := io.WriteString(w, "tar bytes")
		return err
	}, "exec", "deploy/x", "--", "tar", "-x")
	if err != nil {
		t.Fatalf("pipeInto: %v", err)
	}
	if got := s.stdin(t, kubectlBin); got != "tar bytes" {
		t.Errorf("stdin = %q, want the producer's output", got)
	}
}

// A failed producer leaves kubectl complaining about a truncated archive, which
// says nothing about the real cause.
func TestPipeIntoPrefersTheProducersError(t *testing.T) {
	s := newStubs(t)
	s.install(t, kubectlBin, "", 1)

	err := testKubectl().pipeInto(context.Background(), func(w io.Writer) error {
		return io.ErrClosedPipe
	}, "exec", "deploy/x")
	if err == nil {
		t.Fatal("pipeInto succeeded with a failing producer")
	}
	if !strings.Contains(err.Error(), io.ErrClosedPipe.Error()) {
		t.Errorf("error %q hides the producer's failure", err)
	}
}

// kubectl explains a refusal on stderr; without it the operator gets "exit
// status 1" and nothing else.
func TestErrorsQuoteKubectlsStderr(t *testing.T) {
	s := newStubs(t)
	if err := os.WriteFile(filepath.Join(s.dir, kubectlBin),
		[]byte("#!/bin/sh\necho 'Error from server (NotFound): pods \"x\" not found' >&2\nexit 1\n"),
		0o755); err != nil {
		t.Fatalf("writing stub: %v", err)
	}

	err := testKubectl().run(context.Background(), "get", "pod", "x")
	if err == nil {
		t.Fatal("run succeeded against a failing kubectl")
	}
	if !strings.Contains(err.Error(), "NotFound") {
		t.Errorf("error %q dropped kubectl's explanation", err)
	}
}

func TestMissingKubectlIsNamed(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	err := testKubectl().run(context.Background(), "get", "pods")
	if err == nil {
		t.Fatal("run succeeded with no kubectl on PATH")
	}
	if !strings.Contains(err.Error(), kubectlBin) {
		t.Errorf("error %q does not name the missing binary", err)
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// A binary the test never installed must not resolve to the host's own.
//
// The stubs used to precede PATH rather than replace it, so a machine with
// podman installed answered selectBuilder's probe with the real thing: the
// "docker without buildx" tests pushed to the operator's registry, and "no
// docker on PATH" was never true.
func TestStubsHideBinariesTheTestDidNotInstall(t *testing.T) {
	host := t.TempDir()
	script := filepath.Join(host, podmanBin)
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("writing a host binary: %v", err)
	}
	t.Setenv("PATH", host+string(os.PathListSeparator)+os.Getenv("PATH"))

	newStubs(t)

	if path, err := exec.LookPath(podmanBin); err == nil {
		t.Errorf("%s resolved to %s; the test never installed it", podmanBin, path)
	}
}

// pipeInto must not wait forever on a command that exits without reading.
//
// cmd.Stdin used to be an io.Pipe, which os/exec drains from a goroutine of its
// own. When the command exits early that goroutine gives up, and the producer
// then blocks on a pipe with no reader — a hang, in a path whose whole job is
// streaming a tar into a pod that may have died.
func TestPipeIntoFailsWhenTheCommandStopsReading(t *testing.T) {
	s := newStubs(t)
	s.installScript(t, kubectlBin, "") // exits at once, stdin untouched

	err := testKubectl().pipeInto(context.Background(), func(w io.Writer) error {
		// Bigger than any pipe buffer, so the write cannot quietly land in the
		// kernel and hide the fact that nothing read it.
		_, err := w.Write(make([]byte, 1<<20))
		return err
	}, "exec", "-i", "deployment/x", "--", "tar", "-x")

	if err == nil {
		t.Fatal("pipeInto reported success after the archive went nowhere")
	}
}
