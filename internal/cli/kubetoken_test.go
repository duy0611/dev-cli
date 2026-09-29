package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/duy0611/dev-cli/internal/kubetoken"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

// stubBin writes an executable into dir.
func stubBin(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestChooseUsesFzf(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	// fzf reads the choices on stdin and prints the pick on stdout; the stub
	// picks the second line and records what it was offered.
	offered := filepath.Join(dir, "offered")
	stubBin(t, dir, "fzf", "/bin/cat > '"+offered+"'\n/usr/bin/sed -n 2p '"+offered+"'")

	var out bytes.Buffer
	c := chooser{prompt: newPrompter(strings.NewReader(""), &out), out: &out}
	got, err := c.choose(context.Background(), "namespace", []string{"default", "team-a", "team-b"}, "team-b")
	if err != nil {
		t.Fatal(err)
	}
	if got != "default" {
		t.Errorf("choose = %q, want the second line fzf was given", got)
	}
	// The default goes first, so enter in fzf takes it.
	b, _ := os.ReadFile(offered)
	if string(b) != "team-b\ndefault\nteam-a\n" {
		t.Errorf("fzf offered %q", b)
	}
}

func TestChooseFzfCancelled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	stubBin(t, dir, "fzf", "/bin/cat >/dev/null; exit 130")

	var out bytes.Buffer
	c := chooser{prompt: newPrompter(strings.NewReader(""), &out), out: &out}
	_, err := c.choose(context.Background(), "context", []string{"a"}, "")
	if !errors.Is(err, errPickCancelled) {
		t.Errorf("err = %v, want cancelled", err)
	}
}

func TestChooseWithoutFzf(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	var out bytes.Buffer
	c := chooser{prompt: newPrompter(strings.NewReader("\n"), &out), out: &out}
	got, err := c.choose(context.Background(), "context", []string{"kind-dev", "prod-eu"}, "prod-eu")
	if err != nil || got != "prod-eu" {
		t.Errorf("choose = %q, %v; want the default on return", got, err)
	}
	// The choices are shown, since without fzf there is nothing else to show
	// them.
	if !strings.Contains(out.String(), "kind-dev") {
		t.Errorf("choices not listed:\n%s", out.String())
	}
}

// An empty list — commonly RBAC forbidding the listing — is not a dead end:
// the operator may know the name without being allowed to list it.
func TestChooseEmptyListIsFreeText(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	stubBin(t, dir, "fzf", "exit 1") // must not be run

	var out bytes.Buffer
	c := chooser{prompt: newPrompter(strings.NewReader("reader\n"), &out), out: &out}
	got, err := c.choose(context.Background(), "service account", nil, "")
	if err != nil || got != "reader" {
		t.Errorf("choose = %q, %v", got, err)
	}
}

func TestChooseEOFCancels(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var out bytes.Buffer
	c := chooser{prompt: newPrompter(strings.NewReader(""), &out), out: &out}
	if _, err := c.choose(context.Background(), "context", nil, ""); !errors.Is(err, errPickCancelled) {
		t.Errorf("err = %v, want cancelled", err)
	}
}

func TestAskDuration(t *testing.T) {
	var out bytes.Buffer
	c := chooser{prompt: newPrompter(strings.NewReader("four hours\n4h\n"), &out), out: &out}
	got, err := c.askDuration()
	if err != nil || got != 4*time.Hour {
		t.Errorf("askDuration = %v, %v", got, err)
	}
	if !strings.Contains(out.String(), "kubectl's default") {
		t.Errorf("the prompt does not say what empty means:\n%s", out.String())
	}

	c = chooser{prompt: newPrompter(strings.NewReader("\n"), &out), out: &out}
	if got, err := c.askDuration(); err != nil || got != 0 {
		t.Errorf("empty = %v, %v; want zero, so kubectl's default applies", got, err)
	}
}

// noTerminal points os.Stdin at /dev/null for one test. go test may hand a
// test binary the terminal it was run from, and then a test of the scripted
// path would stop at a real prompt.
func noTerminal(t *testing.T) {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = old; f.Close() })
}

// A non-terminal run cannot answer the questions, and says so with exit 2
// rather than blocking on a prompt nobody sees.
func TestAskKubeTokenNeedsATerminal(t *testing.T) {
	noTerminal(t)
	_, err := askKubeToken(context.Background())
	if exitCodeOf(err) != exitUsage || !strings.Contains(err.Error(), "terminal") {
		t.Errorf("err = %v (exit %d)", err, exitCodeOf(err))
	}
}

// recordingProvider captures every Exec, so a test can see where a credential
// went.
type recordingProvider struct {
	provider.Provider
	execs     []recordedExec
	noKubectl bool
	failExec  bool
}

type recordedExec struct {
	cmd   []string
	env   []provider.EnvVar
	stdin string
}

func (p *recordingProvider) Exec(_ context.Context, _ model.Container, cmd []string, opts provider.ExecOpts) error {
	r := recordedExec{cmd: cmd, env: opts.Env}
	if opts.Stdin != nil {
		b, _ := io.ReadAll(opts.Stdin)
		r.stdin = string(b)
	}
	p.execs = append(p.execs, r)
	if p.failExec && strings.Contains(strings.Join(cmd, " "), kubeconfigPath) {
		return errors.New("exec failed")
	}
	if p.noKubectl && strings.Contains(strings.Join(cmd, " "), "command -v kubectl") {
		return errors.New("exit status 1")
	}
	return nil
}

func jwt(exp time.Time) string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{}`)) + "." +
		enc.EncodeToString([]byte(`{"exp":`+strconv.FormatInt(exp.Unix(), 10)+`}`)) + ".sig"
}

func TestInjectKubeToken(t *testing.T) {
	a, _ := newTestApp(t)
	token := jwt(time.Now().Add(4 * time.Hour))
	p := &recordingProvider{}
	tg := &target{container: model.Container{Name: "api"}, provider: p}

	mint := func(context.Context, kubetoken.Request) (kubetoken.Token, error) {
		return kubetoken.Token{
			Kubeconfig: []byte("users:\n- user:\n    token: " + token + "\n"),
			Expiry:     time.Now().Add(4 * time.Hour),
		}, nil
	}
	kv, err := a.injectKubeTokenWith(context.Background(), tg, kubetoken.Request{
		Context: "prod-eu", Namespace: "team-a", ServiceAccount: "reader", Duration: 4 * time.Hour,
	}, mint)
	if err != nil {
		t.Fatal(err)
	}
	if kv.Key != "KUBECONFIG" || kv.Value != kubeconfigPath {
		t.Errorf("env = %v", kv)
	}

	if len(p.execs) == 0 {
		t.Fatal("nothing written")
	}
	write := p.execs[0]
	if !strings.Contains(write.stdin, token) {
		t.Error("the kubeconfig did not arrive on stdin")
	}
	// Anywhere else it is in a process list, the container's or the host's.
	for _, e := range p.execs {
		if strings.Contains(strings.Join(e.cmd, " "), token) {
			t.Errorf("token in an exec's arguments: %v", e.cmd)
		}
		for _, v := range e.env {
			if strings.Contains(v.Value, token) {
				t.Errorf("token in an exec's environment: %s", v.Key)
			}
		}
	}
	// Written then renamed, so a session reading mid-refresh never sees half.
	script := strings.Join(write.cmd, " ")
	for _, want := range []string{"umask 077", "mv "} {
		if !strings.Contains(script, want) {
			t.Errorf("write script lacks %q: %s", want, script)
		}
	}
}

func TestInjectKubeTokenWriteFails(t *testing.T) {
	a, _ := newTestApp(t)
	p := &recordingProvider{failExec: true}
	tg := &target{container: model.Container{Name: "api"}, provider: p}
	mint := func(context.Context, kubetoken.Request) (kubetoken.Token, error) {
		return kubetoken.Token{Kubeconfig: []byte("x")}, nil
	}
	if _, err := a.injectKubeTokenWith(context.Background(), tg, kubetoken.Request{}, mint); err == nil {
		t.Error("a failed write was not reported")
	}
}

// No kubectl in the container is worth saying and not worth failing over:
// another client there may read the file.
func TestInjectKubeTokenNoKubectlInContainer(t *testing.T) {
	a, _ := newTestApp(t)
	p := &recordingProvider{noKubectl: true}
	tg := &target{container: model.Container{Name: "api"}, provider: p}
	mint := func(context.Context, kubetoken.Request) (kubetoken.Token, error) {
		return kubetoken.Token{Kubeconfig: []byte("x")}, nil
	}
	if _, err := a.injectKubeTokenWith(context.Background(), tg, kubetoken.Request{}, mint); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestWithKubeconfigIsLast(t *testing.T) {
	in := []provider.EnvVar{{Key: "KUBECONFIG", Value: "/elsewhere"}, {Key: "A", Value: "b"}}
	got := withEnv(in, provider.EnvVar{Key: "KUBECONFIG", Value: kubeconfigPath})
	if !slices.Equal(got, []provider.EnvVar{{Key: "A", Value: "b"}, {Key: "KUBECONFIG", Value: kubeconfigPath}}) {
		t.Errorf("env = %v", got)
	}
}

// fakeKind is a provider kind registered only in this test binary, so the real
// command functions can run against a provider that records what it is asked.
const fakeKind model.ProviderKind = "kubetoken-fake"

// fake is the one instance the factory hands out; each test resets it.
var fake *fakeProvider

func init() {
	provider.Register(fakeKind, func(model.Provider) (provider.Provider, error) { return fake, nil })
}

// fakeProvider records calls in order, so a test can assert what happened
// before what.
type fakeProvider struct {
	recordingProvider
	status model.Status
	log    []string
}

func (p *fakeProvider) Status(context.Context, model.Container) (model.Status, error) {
	p.log = append(p.log, "status")
	return p.status, nil
}

func (p *fakeProvider) Up(context.Context, model.Container, []provider.EnvVar) error {
	p.log = append(p.log, "up")
	p.status = model.StatusRunning
	return nil
}

func (p *fakeProvider) Exec(ctx context.Context, c model.Container, cmd []string, opts provider.ExecOpts) error {
	p.log = append(p.log, "exec "+strings.Join(cmd, " "))
	return p.recordingProvider.Exec(ctx, c, cmd, opts)
}

// seedFake makes workspace ws on the fake provider, with one container.
func seedFake(t *testing.T, status model.Status) *app {
	t.Helper()
	a, _ := newTestApp(t)
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutProvider(model.Provider{Name: "f", Kind: fakeKind}); err != nil {
		t.Fatal(err)
	}
	if err := runWorkspaceInit(a, "ws", "f", false); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateContainer(model.Container{
		Name: "api", WorkspaceName: "ws", SourceKind: model.SourceFolder, Source: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	fake = &fakeProvider{status: status}
	return a
}

func envHas(env []provider.EnvVar, key string) bool {
	return slices.ContainsFunc(env, func(e provider.EnvVar) bool { return e.Key == key })
}

// Without the flag nothing changes: no question, no write, no KUBECONFIG.
func TestExecWithoutKubeToken(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	if err := runContainerExec(t.Context(), a, "", "api", []string{"true"}, false, nil); err != nil {
		t.Fatal(err)
	}
	for _, e := range fake.execs {
		if e.stdin != "" || envHas(e.env, kubeconfigKey) {
			t.Errorf("exec touched the kubeconfig without the flag: %v", e.cmd)
		}
	}
}

// A stopped container is refused before any question, so nobody answers four
// of them for nothing.
func TestExecKubeTokenStoppedRefusesFirst(t *testing.T) {
	noTerminal(t)
	a := seedFake(t, model.StatusStopped)
	err := runContainerExec(t.Context(), a, "", "api", []string{"true"}, true, nil)
	if exitCodeOf(err) != exitNotFound {
		t.Errorf("err = %v (exit %d), want the not-running refusal", err, exitCodeOf(err))
	}
}

// With the flag and no terminal, exec refuses with exit 2 and runs nothing.
func TestExecKubeTokenNoTerminal(t *testing.T) {
	noTerminal(t)
	a := seedFake(t, model.StatusRunning)
	err := runContainerExec(t.Context(), a, "", "api", []string{"true"}, true, nil)
	if exitCodeOf(err) != exitUsage {
		t.Errorf("err = %v (exit %d)", err, exitCodeOf(err))
	}
	if len(fake.execs) != 0 {
		t.Errorf("ran %d execs after refusing", len(fake.execs))
	}
}

// start-agent asks before it starts anything: a refusal leaves a stopped
// container stopped.
func TestStartAgentAsksBeforeUp(t *testing.T) {
	noTerminal(t)
	a := seedFake(t, model.StatusStopped)
	err := runContainerStartAgent(t.Context(), a, "", "api", "claude", nil, true, nil)
	if exitCodeOf(err) != exitUsage {
		t.Errorf("err = %v (exit %d)", err, exitCodeOf(err))
	}
	if slices.Contains(fake.log, "up") {
		t.Errorf("started the container before asking: %v", fake.log)
	}
}

// applyKubeToken is what every command runs after asking; with a request the
// command's environment gains KUBECONFIG, without one it is untouched.
func TestApplyKubeToken(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	tg, err := a.resolve("", "api")
	if err != nil {
		t.Fatal(err)
	}
	defer tg.release()

	in := []provider.EnvVar{{Key: "A", Value: "b"}}
	out, err := a.applyKubeToken(t.Context(), tg, nil, in)
	if err != nil || !slices.Equal(out, in) || len(fake.execs) != 0 {
		t.Errorf("no request: env %v, %d execs, %v", out, len(fake.execs), err)
	}
}
