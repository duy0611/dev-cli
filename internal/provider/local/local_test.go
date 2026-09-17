package local

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/model"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/provider"
)

// fakeBin writes an executable onto a temp PATH that records its own argv, one
// argument per line, and prints stdout. Driving the real binaries is what the
// smoke test is for; here the question is only what got called.
type fakeBin struct {
	dir string
}

func newFakePath(t *testing.T) *fakeBin {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &fakeBin{dir: dir}
}

// install writes a stub for name that prints stdout and exits with code.
func (f *fakeBin) install(t *testing.T, name, stdout string, code int) {
	t.Helper()
	log := filepath.Join(f.dir, name+".argv")
	script := "#!/bin/sh\n" +
		": > " + shellQuote(log) + "\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> " + shellQuote(log) + "; done\n"
	if stdout != "" {
		script += "printf '%s' " + shellQuote(stdout) + "\n"
	}
	script += "exit " + strconv.Itoa(code) + "\n"

	path := filepath.Join(f.dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake %s: %v", name, err)
	}
}

// argv returns the arguments the named stub was last called with.
func (f *fakeBin) argv(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, name+".argv"))
	if err != nil {
		t.Fatalf("fake %s was never called: %v", name, err)
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func testContainer() model.Container {
	return model.Container{
		Name:          "demo",
		WorkspaceName: "ws",
		SourceKind:    model.SourceFolder,
		Source:        "/projects/demo",
		ConfigPath:    "/projects/demo/.devcontainer/devcontainer.json",
	}
}

// contains reports whether argv holds want as a consecutive run.
func contains(argv, want []string) bool {
	for i := 0; i+len(want) <= len(argv); i++ {
		ok := true
		for j := range want {
			if argv[i+j] != want[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func TestUpPassesWorkspaceFolderAndIDLabels(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	p := &Provider{}
	if err := p.Up(context.Background(), testContainer(), nil); err != nil {
		t.Fatalf("Up: %v", err)
	}

	argv := f.argv(t, devcontainerBin)
	if argv[0] != "up" {
		t.Errorf("argv[0] = %q, want %q", argv[0], "up")
	}
	for _, want := range [][]string{
		{"--workspace-folder", "/projects/demo"},
		{"--id-label", "dev.workspace=ws"},
		{"--id-label", "dev.container=demo"},
	} {
		if !contains(argv, want) {
			t.Errorf("argv %v is missing %v", argv, want)
		}
	}
	if contains(argv, []string{"--remove-existing-container"}) {
		t.Error("plain Up asked for a recreate")
	}
}

// The invariant most likely to break silently: a call that spells the labels
// differently looks up a container that does not exist, and the CLI creates a
// second one rather than failing.
func TestUpAndExecAgreeOnIDLabels(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)
	c := testContainer()
	p := &Provider{}

	if err := p.Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}
	upLabels := extractLabels(f.argv(t, devcontainerBin))

	if err := p.Exec(context.Background(), c, []string{"true"}, provider.ExecOpts{}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	execLabels := extractLabels(f.argv(t, devcontainerBin))

	if len(upLabels) != 2 {
		t.Fatalf("up passed %d id labels, want 2: %v", len(upLabels), upLabels)
	}
	if strings.Join(upLabels, ",") != strings.Join(execLabels, ",") {
		t.Errorf("id labels differ between up (%v) and exec (%v)", upLabels, execLabels)
	}
}

func extractLabels(argv []string) []string {
	var out []string
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "--id-label" {
			out = append(out, argv[i+1])
		}
	}
	return out
}

func TestRebuildAsksForRecreate(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	p := &Provider{}
	if err := p.Rebuild(context.Background(), testContainer(), nil, true); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	argv := f.argv(t, devcontainerBin)
	if argv[0] != "up" {
		t.Errorf("Rebuild called %q; the CLI has no rebuild subcommand, it is flags on up", argv[0])
	}
	for _, want := range []string{"--remove-existing-container", "--build-no-cache"} {
		if !contains(argv, []string{want}) {
			t.Errorf("argv %v is missing %s", argv, want)
		}
	}
}

func TestRebuildWithoutNoCache(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	p := &Provider{}
	if err := p.Rebuild(context.Background(), testContainer(), nil, false); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if contains(f.argv(t, devcontainerBin), []string{"--build-no-cache"}) {
		t.Error("Rebuild(noCache=false) still passed --build-no-cache")
	}
}

func TestExecPassesEnvAndCommand(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	p := &Provider{}
	err := p.Exec(context.Background(), testContainer(),
		[]string{"printenv", "GREETING"},
		provider.ExecOpts{
			Env:    []provider.EnvVar{{Key: "GREETING", Value: "hello"}},
			Stdout: io.Discard,
		})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	argv := f.argv(t, devcontainerBin)
	if !contains(argv, []string{"--remote-env", "GREETING=hello"}) {
		t.Errorf("argv %v is missing the remote env", argv)
	}
	// The command has to come last, after every flag, or the CLI reads it as
	// an argument to whichever flag precedes it.
	if got := argv[len(argv)-2:]; got[0] != "printenv" || got[1] != "GREETING" {
		t.Errorf("argv ends with %v, want the command last", got)
	}
}

func TestStatus(t *testing.T) {
	cases := map[string]model.Status{
		"running\n": model.StatusRunning,
		"exited\n":  model.StatusStopped,
		"created\n": model.StatusStopped,
		"":          model.StatusAbsent,
	}
	for out, want := range cases {
		f := newFakePath(t)
		f.install(t, dockerBin, out, 0)

		got, err := (&Provider{}).Status(context.Background(), testContainer())
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if got != want {
			t.Errorf("docker said %q: Status = %q, want %q", out, got, want)
		}
	}
}

func TestStatusFiltersOnBothLabels(t *testing.T) {
	f := newFakePath(t)
	f.install(t, dockerBin, "running\n", 0)

	if _, err := (&Provider{}).Status(context.Background(), testContainer()); err != nil {
		t.Fatalf("Status: %v", err)
	}

	argv := f.argv(t, dockerBin)
	for _, want := range [][]string{
		{"--filter", "label=dev.workspace=ws"},
		{"--filter", "label=dev.container=demo"},
	} {
		if !contains(argv, want) {
			t.Errorf("argv %v is missing %v", argv, want)
		}
	}
	// -a, or a stopped container reads as absent and `start` creates a second.
	if !contains(argv, []string{"-a"}) {
		t.Errorf("argv %v does not include stopped containers", argv)
	}
}

func TestStopAndRemoveAreNoOpsWhenAbsent(t *testing.T) {
	f := newFakePath(t)
	f.install(t, dockerBin, "", 0) // no container id

	p := &Provider{}
	c := testContainer()
	if err := p.Stop(context.Background(), c); err != nil {
		t.Errorf("Stop on an absent container: %v", err)
	}
	if err := p.Remove(context.Background(), c); err != nil {
		t.Errorf("Remove on an absent container: %v", err)
	}
}

func TestRemoveForcesWhenPresent(t *testing.T) {
	f := newFakePath(t)
	f.install(t, dockerBin, "abc123\n", 0)

	if err := (&Provider{}).Remove(context.Background(), testContainer()); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// The last docker call is the removal itself.
	if argv := f.argv(t, dockerBin); !contains(argv, []string{"rm", "-f", "abc123"}) {
		t.Errorf("argv %v did not force-remove the container", argv)
	}
}

func TestMissingBinaryIsReportedByName(t *testing.T) {
	// An empty PATH: neither binary is reachable.
	t.Setenv("PATH", t.TempDir())

	err := (&Provider{}).Up(context.Background(), testContainer(), nil)
	if err == nil {
		t.Fatal("Up with no devcontainer CLI succeeded")
	}
	if !strings.Contains(err.Error(), devcontainerBin) {
		t.Errorf("error %q does not name the missing binary", err)
	}
}

// The config path is passed explicitly rather than left to the CLI's own
// lookup, because a generated configuration lives in a temporary directory the
// CLI would never find, and because being explicit costs nothing for a
// project-owned one.
func TestUpPassesTheConfigPath(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	c := testContainer()
	if err := (&Provider{}).Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}

	argv := f.argv(t, devcontainerBin)
	if !contains(argv, []string{"--config", c.ConfigPath}) {
		t.Errorf("up argv missing --config %s: %v", c.ConfigPath, argv)
	}
}

func TestExecPassesTheConfigPath(t *testing.T) {
	f := newFakePath(t)
	f.install(t, devcontainerBin, "", 0)

	c := testContainer()
	err := (&Provider{}).Exec(context.Background(), c, []string{"true"},
		provider.ExecOpts{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	argv := f.argv(t, devcontainerBin)
	if !contains(argv, []string{"--config", c.ConfigPath}) {
		t.Errorf("exec argv missing --config %s: %v", c.ConfigPath, argv)
	}
}
