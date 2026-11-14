package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

func TestPassedEnvIsAnAllowlist(t *testing.T) {
	environ := []string{
		"BUZZ_RELAY_URL=ws://localhost:3000",
		"BUZZ_PRIVATE_KEY=nsec1x",
		"BUZZ_AUTH_TAG=tag",
		"BUZZ_OUTPUT_FORMAT=json",
		"BUZZ_S3_SECRET=nope",
		"BUZZ_ACP_LAUNCH_PREFIX=[\"/x\"]",
		"HOME=/Users/me",
		"OTHER=a=b",
	}
	got := passedEnv(environ, buzzIntegration(t), nil)
	want := []provider.EnvVar{
		{Key: "BUZZ_AUTH_TAG", Value: "tag"},
		{Key: "BUZZ_OUTPUT_FORMAT", Value: "json"},
		{Key: "BUZZ_PRIVATE_KEY", Value: "nsec1x"},
		{Key: "BUZZ_RELAY_URL", Value: "ws://localhost:3000"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("default: got %v\nwant %v", got, want)
	}
	got = passedEnv(environ, buzzIntegration(t), []string{"OTHER", "BUZZ_RELAY_URL", "ABSENT"})
	if len(got) != 5 || !slices.Contains(got, provider.EnvVar{Key: "OTHER", Value: "a=b"}) {
		t.Errorf("--pass-env: got %v", got)
	}
	if n := len(passedEnv(environ, buzzIntegration(t), []string{"BUZZ_RELAY_URL"})); n != 4 {
		t.Errorf("a repeated name was passed twice: %d", n)
	}
}

func TestValidatePassEnvRefusesPatternsAndBadNames(t *testing.T) {
	for _, bad := range []string{"BUZZ_*", "*", "1X", "A-B", "", "A B"} {
		err := validatePassEnv([]string{"OK", bad})
		if err == nil || exitCodeOf(err) != exitUsage {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	if err := validatePassEnv([]string{"OK_1", "_x"}); err != nil {
		t.Error(err)
	}
}

// fakeACPAgent answers every request with the cwd and path it saw, as the agent
// inside the container would, and exits when its stdin ends.
func fakeACPAgent(_ context.Context, cmd []string, opts provider.ExecOpts) (bool, error) {
	// Behind the env wrapper the adapter is everything after its fixed prefix.
	if len(cmd) > 5 && cmd[0] == "sh" && cmd[1] == "-c" && strings.Contains(cmd[2], `exec "$@"`) {
		cmd = cmd[5:]
	}
	switch strings.Join(cmd, " ") {
	case "sh -c pwd":
		_, _ = io.WriteString(opts.Stdout, "/workspaces/api\n")
		return true, nil
	case "fake-acp":
	default:
		return false, nil
	}
	sc := bufio.NewScanner(opts.Stdin)
	for sc.Scan() {
		var req struct {
			ID     int            `json:"id"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			return true, err
		}
		cwd, _ := req.Params["cwd"].(string)
		fmt.Fprintf(opts.Stdout, `{"jsonrpc":"2.0","id":%d,"result":{"seen":%q,"file":%q}}`+"\n",
			req.ID, cwd, cwd+"/main.go")
	}
	return true, nil
}

func TestACPBridgesThroughTheContainer(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	fake.execFn = fakeACPAgent
	st, _ := a.store()
	c, err := st.GetContainer("ws", "api")
	if err != nil {
		t.Fatal(err)
	}

	in := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":%q,"mcpServers":[]}}`+"\n"+
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/elsewhere","mcpServers":[]}}`+"\n", c.Source+"/sub")
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runContainerACP(t.Context(), a, "", "api", "claude", []string{"fake-acp"}, nil, "", nil, nil,
			strings.NewReader(in), &out)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the bridge did not return after the client closed stdin")
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 responses, got %q", out.String())
	}
	if want := `"seen":"` + c.Source + `/sub"`; !strings.Contains(lines[0], want) {
		t.Errorf("mapped cwd not mapped back: %s", lines[0])
	}
	if want := `"file":"` + c.Source + `/sub/main.go"`; !strings.Contains(lines[0], want) {
		t.Errorf("agent path not mapped to the host: %s", lines[0])
	}
	if want := `"seen":"` + c.Source + `"`; !strings.Contains(lines[1], want) {
		t.Errorf("unmapped cwd did not fall back to the workspace folder: %s", lines[1])
	}
}

func TestACPReturnsWhenTheAgentDiesFirst(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	fake.execFn = func(ctx context.Context, cmd []string, opts provider.ExecOpts) (bool, error) {
		if cmd[0] == "fake-acp" {
			// As a dying process releases the stdin it inherited; the
			// recording provider reads stdin to EOF after the exec.
			if f, ok := opts.Stdin.(*os.File); ok {
				_ = f.Close()
			}
			return true, errors.New("agent crashed")
		}
		return fakeACPAgent(ctx, cmd, opts)
	}
	clientIn, _ := io.Pipe() // never closed: the client is still connected
	done := make(chan error, 1)
	go func() {
		done <- runContainerACP(t.Context(), a, "", "api", "claude", []string{"fake-acp"}, nil, "", nil, nil,
			clientIn, io.Discard)
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("the agent's failure was lost")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the bridge hung after the agent exited")
	}
}

func TestACPStdoutCarriesOnlyProtocol(t *testing.T) {
	a := seedFake(t, model.StatusStopped)
	fake.execFn = fakeACPAgent
	var host, errw bytes.Buffer
	a.out, a.errw = &host, &errw

	in := `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/x","mcpServers":[{"name":"m"}]}}` + "\n"
	var out bytes.Buffer
	if err := runContainerACP(t.Context(), a, "", "api", "claude", []string{"fake-acp"}, nil, "", nil, nil,
		strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	if host.Len() != 0 {
		t.Errorf("dev printed on the host's stdout: %q", host.String())
	}
	if !strings.Contains(strings.Join(fake.log, "\n"), "up") {
		t.Error("the stopped container was not started")
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("stdout = %q", out.String())
	}
	var msg map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &msg); err != nil || msg["jsonrpc"] != "2.0" {
		t.Errorf("stdout is not a JSON-RPC message: %q", lines[0])
	}
	if !strings.Contains(errw.String(), "dropping 1 MCP") {
		t.Errorf("stderr = %q", errw.String())
	}
}

func TestACPMissingAdapterFailsWithoutARebuildOffer(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	var host, errw bytes.Buffer
	a.out, a.errw = &host, &errw
	fake.failCmd = func(cmd []string) error {
		if len(cmd) == 5 && cmd[3] == "sh" && cmd[4] == "claude-agent-acp" {
			return errors.New("exit status 1")
		}
		return nil
	}
	err := runContainerACP(t.Context(), a, "", "api", "claude", nil, nil, "", nil, nil,
		strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "claude-agent-acp") || !strings.Contains(err.Error(), "rebuild") {
		t.Fatalf("err = %v, want one naming the adapter", err)
	}
	if host.Len() != 0 || strings.Contains(errw.String(), "rebuild the container") {
		t.Errorf("a prompt was shown: stdout %q stderr %q", host.String(), errw.String())
	}
	for _, line := range fake.log {
		if line == "rebuild" || strings.Contains(line, "claude ") {
			t.Errorf("unexpected %q", line)
		}
	}
}

func TestACPChecksTheAdapterNotTheAgent(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	fake.execFn = fakeACPAgent
	if err := runContainerACP(t.Context(), a, "", "api", "codex", []string{"fake-acp", "--flag"}, nil, "", nil, nil,
		strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	log := strings.Join(fake.log, "\n")
	if !strings.Contains(log, "exec sh -c command -v \"$1\" sh fake-acp") || strings.Contains(log, "command -v codex") {
		t.Errorf("log:\n%s", log)
	}
}

func TestACPEnvIsExecEnvAndSurvivesACommand(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	fake.execFn = fakeACPAgent
	if err := runContainerACP(t.Context(), a, "", "api", "claude", []string{"fake-acp"}, nil, "", nil, buzzIntegration(t),
		strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, e := range fake.execs {
		if e.cmd[0] != "fake-acp" {
			continue
		}
		if !slices.Contains(e.env, provider.EnvVar{Key: "CLAUDE_AGENT_ACP_PROTOCOL", Value: "v1"}) {
			t.Errorf("env = %v", e.env)
		}
		return
	}
	t.Fatal("the adapter never ran")
}

func TestACPAuditsTheArgvOnly(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	fake.execFn = fakeACPAgent
	env := []provider.EnvVar{{Key: "BUZZ_PRIVATE_KEY", Value: "nsec-secret"}}
	if err := runContainerACP(t.Context(), a, "", "api", "claude", []string{"fake-acp"}, env, "", nil, nil,
		strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	events := auditBytes(t)
	if !strings.Contains(events, `"acp"`) || !strings.Contains(events, "fake-acp") || strings.Contains(events, "nsec-secret") {
		t.Errorf("audit = %s", events)
	}
}

func execOf(t *testing.T, first string) recordedExec {
	t.Helper()
	for _, e := range fake.execs {
		if e.cmd[0] == first {
			return e
		}
	}
	t.Fatalf("no exec of %q", first)
	return recordedExec{}
}

func TestACPSecretsTravelByStdinNotEnvOrArgv(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	fake.execFn = fakeACPAgent
	secrets := []provider.EnvVar{
		{Key: "BUZZ_PRIVATE_KEY", Value: "nsec-secret"},
		{Key: "BUZZ_RELAY_URL", Value: "ws://127.0.0.1:3000"},
	}
	if err := runContainerACP(t.Context(), a, "", "api", "claude", []string{"fake-acp", "--flag"}, secrets, "", nil, buzzIntegration(t),
		strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	var writer, adapter *recordedExec
	for i, e := range fake.execs {
		for _, kv := range e.env {
			if strings.Contains(kv.Key+kv.Value, "nsec-secret") || kv.Key == "BUZZ_PRIVATE_KEY" {
				t.Errorf("secret in the exec env of %q", e.cmd)
			}
		}
		if strings.Contains(strings.Join(e.cmd, " "), "nsec-secret") {
			t.Errorf("secret in argv %q", e.cmd)
		}
		if strings.Contains(e.stdin, "nsec-secret") {
			writer = &fake.execs[i]
		}
		if slices.Contains(e.cmd, "fake-acp") && len(e.cmd) > 5 {
			adapter = &fake.execs[i]
		}
	}
	if writer == nil || !strings.Contains(writer.stdin, "BUZZ_PRIVATE_KEY='nsec-secret'\n") ||
		!strings.Contains(writer.stdin, "BUZZ_RELAY_URL='ws://127.0.0.1:3000'\n") {
		t.Fatalf("no exec carried the variables on stdin: %+v", writer)
	}
	if adapter == nil || adapter.cmd[4] != writer.cmd[4] || strings.Join(adapter.cmd[5:], " ") != "fake-acp --flag" {
		t.Fatalf("adapter exec = %+v, writer = %+v", adapter, writer)
	}
	if !slices.Contains(adapter.env, provider.EnvVar{Key: "CLAUDE_AGENT_ACP_PROTOCOL", Value: "v1"}) {
		t.Errorf("the agent's own env is missing: %v", adapter.env)
	}
	log := strings.Join(fake.log, "\n")
	if !strings.Contains(log, "command -v \"$1\" sh fake-acp") {
		t.Errorf("the presence check does not name the adapter:\n%s", log)
	}
	if !strings.Contains(log, "rm -f "+writer.cmd[4]) {
		t.Errorf("the env file is not removed after the run:\n%s", log)
	}
	events := auditBytes(t)
	if strings.Contains(events, "nsec-secret") || strings.Contains(events, "eval") || !strings.Contains(events, `"fake-acp","--flag"`) {
		t.Errorf("audit = %s", events)
	}
}

func TestACPWithoutPassedVariablesWritesNoFile(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	fake.execFn = fakeACPAgent
	if err := runContainerACP(t.Context(), a, "", "api", "claude", []string{"fake-acp"}, nil, "", nil, nil,
		strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, e := range fake.execs {
		if strings.Contains(strings.Join(e.cmd, " "), "set -C") || strings.Contains(strings.Join(e.cmd, " "), "rm -f") {
			t.Errorf("unexpected exec %q", e.cmd)
		}
	}
	if e := execOf(t, "fake-acp"); len(e.cmd) != 1 {
		t.Errorf("adapter was wrapped: %q", e.cmd)
	}
}

func TestACPWarnsWhenBuzzIsMissing(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	fake.execFn = fakeACPAgent
	var errw bytes.Buffer
	a.errw = &errw
	fake.failCmd = func(cmd []string) error {
		if len(cmd) == 5 && cmd[4] == "buzz" {
			return errors.New("exit status 1")
		}
		return nil
	}
	if err := runContainerACP(t.Context(), a, "", "api", "claude", []string{"fake-acp"}, nil, "", nil, buzzIntegration(t),
		strings.NewReader(""), io.Discard); err != nil {
		t.Fatalf("a missing buzz must not fail the launch: %v", err)
	}
	if n := strings.Count(errw.String(), "buzz"); n != 1 || !strings.Contains(errw.String(), "warning") {
		t.Errorf("stderr = %q", errw.String())
	}
}

func writeStubDev(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "dev")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nfor a; do printf '<%s>\\n' \"$a\"; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestACPHarnessScript(t *testing.T) {
	exe := writeStubDev(t, filepath.Join(t.TempDir(), "it's a dir"))
	script := acpHarness(exe, "my ws", "api", "claude",
		[]string{"npx", "-y", "x y"}, []string{"FOO"}, "/tmp/a b.log", boolPtr(false), "buzz")
	if !strings.HasPrefix(script, "#!/bin/sh\nexec ") || strings.Contains(script, "PATH") {
		t.Errorf("script:\n%s", script)
	}
	out, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("%v\n%s", err, script)
	}
	want := "<container>\n<acp>\n<api>\n<--workspace>\n<my ws>\n<--agent>\n<claude>\n<--integration>\n<buzz>\n<--pass-env>\n<FOO>\n" +
		"<--trace>\n</tmp/a b.log>\n<--no-ssh-agent>\n<-->\n<npx>\n<-y>\n<x y>\n"
	if string(out) != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
}

func runGeneric(t *testing.T, script string, env ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String() + errb.String(), code
}

func TestACPGenericHarness(t *testing.T) {
	exe := writeStubDev(t, filepath.Join(t.TempDir(), "it's a dir"))
	script := acpGenericHarness(exe, "claude", "", nil, nil, "buzz")
	if !strings.HasPrefix(script, "#!/bin/sh\n") || strings.Contains(script, "PATH") {
		t.Errorf("script:\n%s", script)
	}

	out, code := runGeneric(t, script, "DEV_ACP_WORKSPACE=ws")
	if code != 2 || !strings.Contains(out, "dev-acp: DEV_ACP_CONTAINER is not set in this agent's environment") {
		t.Errorf("no container: code %d, %q", code, out)
	}
	out, code = runGeneric(t, script, "DEV_ACP_CONTAINER=api")
	if code != 2 || !strings.Contains(out, "dev-acp: DEV_ACP_WORKSPACE is not set in this agent's environment") {
		t.Errorf("no workspace: code %d, %q", code, out)
	}

	out, code = runGeneric(t, script, "DEV_ACP_CONTAINER=api", "DEV_ACP_WORKSPACE=my ws")
	want := "<container>\n<acp>\n<api>\n<--workspace>\n<my ws>\n<--agent>\n<claude>\n<--trace>\n</tmp/acp-api.log>\n" +
		"<--integration>\n<buzz>\n"
	if code != 0 || out != want {
		t.Errorf("defaults: code %d\ngot  %q\nwant %q", code, out, want)
	}
	out, _ = runGeneric(t, script, "DEV_ACP_CONTAINER=api", "DEV_ACP_WORKSPACE=w", "DEV_ACP_AGENT=codex", "DEV_ACP_TRACE=/x/t.log")
	if !strings.Contains(out, "<--agent>\n<codex>\n<--trace>\n</x/t.log>\n") {
		t.Errorf("overrides: %q", out)
	}
}

func TestACPGenericHarnessFlagsAndDefaults(t *testing.T) {
	exe := writeStubDev(t, t.TempDir())
	script := acpGenericHarness(exe, "codex", "/var/t log", []string{"FOO"}, boolPtr(true), "")
	out, code := runGeneric(t, script, "DEV_ACP_CONTAINER=api", "DEV_ACP_WORKSPACE=w")
	want := "<container>\n<acp>\n<api>\n<--workspace>\n<w>\n<--agent>\n<codex>\n<--trace>\n</var/t log>\n" +
		"<--pass-env>\n<FOO>\n<--ssh-agent>\n"
	if code != 0 || out != want {
		t.Errorf("code %d\ngot  %q\nwant %q", code, out, want)
	}
	if strings.Contains(out, "--integration") {
		t.Error("integration passed without being asked")
	}
}

func TestACPPrintHarnessWithoutNameTouchesNothing(t *testing.T) {
	a := seedFake(t, model.StatusStopped)
	cmd := newContainerACPCmd(a)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--print-harness", "--integration", "buzz", "--pass-env", "FOO", "--agent", "codex"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, w := range []string{"DEV_ACP_CONTAINER", "${DEV_ACP_AGENT:-codex}", "--integration 'buzz'", "--pass-env 'FOO'"} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in:\n%s", w, s)
		}
	}
	if len(fake.log) != 0 || len(fake.execs) != 0 {
		t.Errorf("touched the container: %v", fake.log)
	}
	// Without a name and without --print-harness it is a usage error.
	cmd = newContainerACPCmd(a)
	cmd.SetArgs(nil)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil || exitCodeOf(err) != exitUsage {
		t.Errorf("err = %v", err)
	}
}

func TestACPIntegrationFlag(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	cmd := newContainerACPCmd(a)
	cmd.SetArgs([]string{"api", "--integration", "slack"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil || exitCodeOf(err) != exitUsage || !strings.Contains(err.Error(), "buzz") {
		t.Errorf("err = %v, want a usage error listing buzz", err)
	}
	if len(fake.log) != 0 {
		t.Errorf("touched the container: %v", fake.log)
	}
}

func TestACPPrintHarnessTouchesNoContainer(t *testing.T) {
	a := seedFake(t, model.StatusStopped)
	var out bytes.Buffer
	if err := printACPHarness(t.Context(), a, &out, "", "api", "claude", nil, []string{"BUZZ_X"}, "", nil, "buzz", "/bin/dev"); err != nil {
		t.Fatal(err)
	}
	if len(fake.log) != 0 || len(fake.execs) != 0 {
		t.Errorf("touched the container: %v", fake.log)
	}
	if !strings.HasPrefix(out.String(), "#!/bin/sh\n") ||
		!strings.Contains(out.String(), "'--workspace' 'ws' '--agent' 'claude' '--integration' 'buzz' '--pass-env' 'BUZZ_X'") {
		t.Errorf("script:\n%s", out.String())
	}
	err := printACPHarness(t.Context(), a, &out, "", "missing", "claude", nil, nil, "", nil, "", "/bin/dev")
	if err == nil || exitCodeOf(err) != exitNotFound {
		t.Errorf("err = %v", err)
	}
	err = printACPHarness(t.Context(), a, &out, "", "api", "claude", nil, []string{"A*"}, "", nil, "", "/bin/dev")
	if err == nil || exitCodeOf(err) != exitUsage {
		t.Errorf("err = %v", err)
	}
}

func TestACPHarnessSSHAgentFlag(t *testing.T) {
	on := true
	script := acpHarness("/bin/dev", "ws", "api", "claude", nil, nil, "", &on, "")
	if !strings.Contains(script, "'--ssh-agent'") || strings.Contains(script, "no-ssh") || strings.Contains(script, "--integration") {
		t.Errorf("script:\n%s", script)
	}
}

func buzzIntegration(t *testing.T) *acpIntegration {
	t.Helper()
	integ, err := lookupACPIntegration("buzz")
	if err != nil || integ == nil {
		t.Fatalf("buzz integration: %v", err)
	}
	return integ
}

func TestACPNoIntegrationPassesNothingByDefault(t *testing.T) {
	environ := []string{"BUZZ_PRIVATE_KEY=k", "BUZZ_RELAY_URL=u", "FOO=1"}
	if got := passedEnv(environ, nil, nil); len(got) != 0 {
		t.Errorf("got %v", got)
	}
	if got := passedEnv(environ, nil, []string{"FOO"}); !reflect.DeepEqual(got, []provider.EnvVar{{Key: "FOO", Value: "1"}}) {
		t.Errorf("got %v", got)
	}
}

func TestLookupACPIntegration(t *testing.T) {
	if integ, err := lookupACPIntegration(""); err != nil || integ != nil {
		t.Errorf("empty: %v, %v", integ, err)
	}
	if _, err := lookupACPIntegration("nope"); err == nil || exitCodeOf(err) != exitUsage || !strings.Contains(err.Error(), "buzz") {
		t.Errorf("err = %v", err)
	}
}

func TestACPPinsClaudeProtocolOnlyWithBuzz(t *testing.T) {
	pin := provider.EnvVar{Key: "CLAUDE_AGENT_ACP_PROTOCOL", Value: "v1"}
	for _, tc := range []struct {
		agent string
		integ *acpIntegration
		want  bool
	}{{"claude", buzzIntegration(t), true}, {"claude", nil, false}, {"codex", buzzIntegration(t), false}} {
		a := seedFake(t, model.StatusRunning)
		fake.execFn = fakeACPAgent
		if err := runContainerACP(t.Context(), a, "", "api", tc.agent, []string{"fake-acp"}, nil, "", nil, tc.integ,
			strings.NewReader(""), io.Discard); err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(execOf(t, "fake-acp").env, pin); got != tc.want {
			t.Errorf("%s integ=%v: pinned = %v", tc.agent, tc.integ != nil, got)
		}
	}
}

func TestACPNoBuzzWarningWithoutIntegration(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	fake.execFn = fakeACPAgent
	var errw bytes.Buffer
	a.errw = &errw
	fake.failCmd = func(cmd []string) error {
		if len(cmd) == 5 && cmd[4] == "buzz" {
			return errors.New("exit status 1")
		}
		return nil
	}
	if err := runContainerACP(t.Context(), a, "", "api", "claude", []string{"fake-acp"}, nil, "", nil, nil,
		strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errw.String(), "buzz") {
		t.Errorf("stderr = %q", errw.String())
	}
	for _, l := range fake.log {
		if strings.Contains(l, "buzz") {
			t.Errorf("probed for buzz: %q", l)
		}
	}
}

func boolPtr(b bool) *bool { return &b }

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("client went away") }

func TestACPReturnsWhenTheClientStopsReading(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	fake.execFn = func(ctx context.Context, cmd []string, opts provider.ExecOpts) (bool, error) {
		if cmd[0] == "fake-acp" {
			// Keeps writing after the bridge has stopped reading, then exits.
			for range 5 {
				_, _ = io.WriteString(opts.Stdout, `{"jsonrpc":"2.0","method":"session/update"}`+"\n")
			}
			if f, ok := opts.Stdin.(*os.File); ok {
				_ = f.Close()
			}
			return true, nil
		}
		return fakeACPAgent(ctx, cmd, opts)
	}
	clientIn, _ := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- runContainerACP(t.Context(), a, "", "api", "claude", []string{"fake-acp"}, nil, "", nil, nil,
			clientIn, failingWriter{})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the command hung after writing to the client failed")
	}
}

func TestACPRefusesKubeToken(t *testing.T) {
	a := seedFake(t, model.StatusRunning)
	for _, args := range [][]string{{"api", "--kube-token"}, {"api", "--kube-token", "--print-harness"}} {
		cmd := newContainerACPCmd(a)
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		err := cmd.Execute()
		if err == nil || exitCodeOf(err) != exitUsage || !strings.Contains(err.Error(), "terminal") {
			t.Errorf("%v: err = %v, want a usage error naming the terminal", args, err)
		}
	}
	if len(fake.log) != 0 {
		t.Errorf("touched the container: %v", fake.log)
	}
}
