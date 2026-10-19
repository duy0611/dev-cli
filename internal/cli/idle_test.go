package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/duy0611/dev-cli/internal/audit"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/provider/local"
)

// stubIdleDevcontainer is stubDevcontainer plus an answer for the watchdog
// call: it logs one IDLE-EXEC line per call, drains stdin and prints answer,
// or exits 1 when answer is "fail".
func stubIdleDevcontainer(t *testing.T, answer string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\n" +
		"case \"$1\" in read-configuration) echo '{\"mergedConfiguration\":{}}'; exit 0;; esac\n" +
		"case \"$*\" in *' dev-idle '*)\n  echo IDLE-EXEC >> '" + log + "'\n  cat >/dev/null\n"
	if answer == "fail" {
		script += "  echo boom >&2; exit 1;;\nesac\n"
	} else {
		script += "  printf '" + answer + "\\n'; exit 0;;\nesac\n"
	}
	script += "exit 0\n"
	writeFile(t, filepath.Join(dir, "devcontainer"), script)
	if err := os.Chmod(filepath.Join(dir, "devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func createIdle(t *testing.T, a *app, name string, extra ...string) error {
	argv := append([]string{"container", "create", name, "--no-folder", "--generate", "--tools", "yq"}, extra...)
	return runIn(t, a, argv...)
}

func TestStartRunsTheIdleWatchdog(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubIdleDevcontainer(t, "started")
	if err := createIdle(t, a, "x"); err != nil {
		t.Fatal(err)
	}
	calls := callsIn(t, log)
	if n := strings.Count(calls, "IDLE-EXEC"); n != 1 {
		t.Fatalf("watchdog calls = %d, want 1:\n%s", n, calls)
	}
	if strings.Index(calls, "IDLE-EXEC") < strings.Index(calls, "up ") {
		t.Errorf("watchdog ran before up:\n%s", calls)
	}
}

func TestStartSkipsIdleWatchdogWhenOptedOut(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubIdleDevcontainer(t, "started")
	if err := createIdle(t, a, "x", "--no-idle-stop"); err != nil {
		t.Fatal(err)
	}
	if calls := callsIn(t, log); strings.Contains(calls, "IDLE-EXEC") {
		t.Errorf("opted-out container got a watchdog:\n%s", calls)
	}
}

func TestStartWarnsWhenIdleStopUnavailable(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubIdleDevcontainer(t, "unavailable")
	var err error
	got := captureStderr(t, func() { err = createIdle(t, a, "x") })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "idle stop is unavailable in x") || !strings.Contains(got, "passwordless sudo") {
		t.Errorf("warning = %q", got)
	}
}

func TestStartRecordsTheLastIdleStop(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)
	stubIdleDevcontainer(t, `marker stopped 1760000000 1860\nstarted`)
	if err := createIdle(t, a, "x"); err != nil {
		t.Fatal(err)
	}
	es := auditEntries(t, audit.Filter{Events: []string{"idle-stop"}})
	if len(es) != 1 {
		t.Fatalf("idle-stop records = %d, want 1", len(es))
	}
	if es[0].Fields["outcome"] != "stopped" || es[0].Fields["idle_minutes"] != float64(31) {
		t.Errorf("fields = %v", es[0].Fields)
	}
	if !strings.Contains(out.String(), "container x was stopped at ") || !strings.Contains(out.String(), "after 31 idle minutes") {
		t.Errorf("out = %q", out)
	}
}

func TestIdleErrorsNeverFailACommand(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubIdleDevcontainer(t, "fail")
	var err error
	got := captureStderr(t, func() { err = createIdle(t, a, "x") })
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(got, "idle stop for x") {
		t.Errorf("no warning: %q", got)
	}
}

func TestRebuildRunsTheIdleWatchdog(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubIdleDevcontainer(t, "started")
	if err := createIdle(t, a, "x", "--no-start"); err != nil {
		t.Fatal(err)
	}
	if err := runIn(t, a, "container", "rebuild", "x"); err != nil {
		t.Fatal(err)
	}
	calls := callsIn(t, log)
	if n := strings.Count(calls, "IDLE-EXEC"); n != 1 {
		t.Fatalf("watchdog calls = %d, want 1:\n%s", n, calls)
	}
	if strings.Index(calls, "IDLE-EXEC") < strings.Index(calls, "--remove-existing-container") {
		t.Errorf("watchdog ran before the rebuild's up:\n%s", calls)
	}
}

// noExecProvider fails the test on any Exec, and stands in for a provider
// that is not an IdleStopper.
type noExecProvider struct {
	provider.Provider
	t *testing.T
}

func (p noExecProvider) Exec(context.Context, model.Container, []string, provider.ExecOpts) error {
	p.t.Error("Exec on a provider with no idle stop")
	return errors.New("exec")
}

func TestK8sGetsNoIdleWatchdog(t *testing.T) {
	a, _ := newTestApp(t)
	a.ensureIdle(t.Context(), noExecProvider{t: t}, model.Container{Name: "x", IdleStop: true})
}

func TestUnavailableWarnsOncePerRun(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubIdleDevcontainer(t, "unavailable")
	if err := createIdle(t, a, "x", "--no-start"); err != nil {
		t.Fatal(err)
	}
	c := containerRow(t, a, "x")
	p := &local.Provider{}
	got := captureStderr(t, func() {
		a.ensureIdle(t.Context(), p, c)
		a.ensureIdle(t.Context(), p, c)
	})
	if n := strings.Count(got, "idle stop is unavailable"); n != 1 {
		t.Errorf("warnings = %d, want 1: %q", n, got)
	}
	if n := strings.Count(callsIn(t, log), "IDLE-EXEC"); n != 2 {
		t.Errorf("watchdog calls = %d, want 2 (each rebuild needs one)", n)
	}
}

func TestStartAgentOnARunningContainerRunsTheWatchdogOnce(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubIdleDevcontainer(t, "started")
	if err := createIdle(t, a, "x", "--no-start"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(log)
	writeFile(t, filepath.Join(dir, "docker"),
		"#!/bin/sh\ncase \"$*\" in\n  *State*) echo running ;;\n  ps*) echo abc123 ;;\nesac\nexit 0\n")
	if err := os.Chmod(filepath.Join(dir, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runContainerStartAgent(t.Context(), a, "", "x", "claude", "", nil, false, nil); err != nil {
		t.Fatal(err)
	}
	calls := callsIn(t, log)
	if n := strings.Count(calls, "IDLE-EXEC"); n != 1 {
		t.Errorf("watchdog calls = %d, want 1:\n%s", n, calls)
	}
	if strings.Contains(calls, " up ") {
		t.Errorf("a running container was brought up:\n%s", calls)
	}
}

// stubRunningDocker makes every container look running.
func stubRunningDocker(t *testing.T, dir string, probe ...string) {
	probe = append(probe, "ensure")
	probeAnswer := probe[0]
	t.Helper()
	writeFile(t, filepath.Join(dir, "docker"),
		"#!/bin/sh\necho \"$*\" >> '"+filepath.Join(dir, "docker-calls")+"'\ncase \"$*\" in\n  exec*) echo \""+probeAnswer+"\" ;;\n  *State*) echo running ;;\n  ps*) echo abc123 ;;\nesac\nexit 0\n")
	if err := os.Chmod(filepath.Join(dir, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestRepairRestartsAMissingWatchdog(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubIdleDevcontainer(t, "started")
	for _, n := range []string{"x", "y"} {
		if err := createIdle(t, a, n, "--no-start"); err != nil {
			t.Fatal(err)
		}
	}
	stubRunningDocker(t, filepath.Dir(log))
	if code := run(t.Context(), a, []string{"container", "list"}); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if n := strings.Count(callsIn(t, log), "IDLE-EXEC"); n != 2 {
		t.Errorf("watchdog calls = %d, want 2:\n%s", n, callsIn(t, log))
	}
}

func TestRepairSkipsStoppedAndOptedOut(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubIdleDevcontainer(t, "started")
	if err := createIdle(t, a, "stopped", "--no-start"); err != nil {
		t.Fatal(err)
	}
	if err := createIdle(t, a, "optout", "--no-start", "--no-idle-stop"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(log)
	// Only "optout" is running.
	writeFile(t, filepath.Join(dir, "docker"),
		"#!/bin/sh\ncase \"$*\" in\n  *optout*) case \"$*\" in *State*) echo running;; ps*) echo abc123;; esac;;\nesac\nexit 0\n")
	run(t.Context(), a, []string{"container", "list"})
	if calls := callsIn(t, log); strings.Contains(calls, "IDLE-EXEC") {
		t.Errorf("watchdog started:\n%s", calls)
	}
}

func TestRepairSkipsWhatThisRunAlreadyEnsured(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubIdleDevcontainer(t, "started")
	if err := createIdle(t, a, "x", "--no-start"); err != nil {
		t.Fatal(err)
	}
	stubRunningDocker(t, filepath.Dir(log))
	run(t.Context(), a, []string{"container", "start", "x"})
	if n := strings.Count(callsIn(t, log), "IDLE-EXEC"); n != 1 {
		t.Errorf("watchdog calls = %d, want 1:\n%s", n, callsIn(t, log))
	}
}

func TestRepairIsBoundedByItsTimeout(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubIdleDevcontainer(t, "started")
	if err := createIdle(t, a, "x", "--no-start"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(log)
	stubRunningDocker(t, dir)
	writeFile(t, filepath.Join(dir, "devcontainer"),
		"#!/bin/sh\ncase \"$1\" in read-configuration) echo '{\"mergedConfiguration\":{}}'; exit 0;; esac\ncase \"$*\" in *' dev-idle '*) : > '"+filepath.Join(dir, "reached")+"'; sleep 5;; esac\nexit 0\n")
	old := repairTimeout
	repairTimeout = 200 * time.Millisecond
	t.Cleanup(func() { repairTimeout = old })
	oldGrace := repairGrace
	repairGrace = 100 * time.Millisecond
	t.Cleanup(func() { repairGrace = oldGrace })
	start := time.Now()
	got := captureStderr(t, func() { run(t.Context(), a, []string{"container", "list"}) })
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("run took %v", d)
	}
	if _, err := os.Stat(filepath.Join(dir, "reached")); err != nil {
		t.Errorf("the stub's sleep was never reached: %v", err)
	}
	if !strings.Contains(got, "idle stop check timed out after 200ms") {
		t.Errorf("no timeout warning: %q", got)
	}
}

func TestRepairDoesNothingWithoutTheDatabase(t *testing.T) {
	a, _ := newTestApp(t)
	a.version = "test"
	log := stubIdleDevcontainer(t, "started")
	stubRunningDocker(t, filepath.Dir(log))
	run(t.Context(), a, []string{"--version"})
	if calls := callsIn(t, log); calls != "" {
		t.Errorf("devcontainer called:\n%s", calls)
	}
	if calls := callsIn(t, filepath.Join(filepath.Dir(log), "docker-calls")); calls != "" {
		t.Errorf("docker called:\n%s", calls)
	}
}

func TestRepairOutputIsSerialised(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)
	log := stubIdleDevcontainer(t, `marker stopped 1760000000 1860\nstarted`)
	for _, n := range []string{"x", "y"} {
		if err := createIdle(t, a, n, "--no-start"); err != nil {
			t.Fatal(err)
		}
	}
	stubRunningDocker(t, filepath.Dir(log))
	got := captureStderr(t, func() { run(t.Context(), a, []string{"container", "list"}) })
	if n := strings.Count(got, "after 31 idle minutes"); n != 2 {
		t.Errorf("reports = %d, want 2: %q", n, got)
	}
	for _, l := range strings.Split(strings.TrimSpace(got), "\n") {
		if !strings.HasPrefix(l, "dev: ") {
			t.Errorf("interleaved stderr line %q", l)
		}
	}
	if strings.Contains(out.String(), "idle minutes") {
		t.Errorf("a repair notice reached stdout: %q", out)
	}
}

func TestRepairOnAHealthyContainerOnlyProbes(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubIdleDevcontainer(t, "started")
	if err := createIdle(t, a, "x", "--no-start"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(log)
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	stubRunningDocker(t, dir, "ok")
	run(t.Context(), a, []string{"container", "list"})
	if calls := callsIn(t, log); strings.Contains(calls, "IDLE-EXEC") || strings.Contains(calls, "exec") {
		t.Errorf("devcontainer exec on a healthy container:\n%s", calls)
	}
	if d := callsIn(t, filepath.Join(dir, "docker-calls")); !strings.Contains(d, "exec -u root abc123 sh -c") {
		t.Errorf("no root probe:\n%s", d)
	}
}

func TestRepairWarnsOnAProbeError(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	log := stubIdleDevcontainer(t, "started")
	if err := createIdle(t, a, "x", "--no-start"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(log)
	stubRunningDocker(t, dir, "ok")
	writeFile(t, filepath.Join(dir, "docker"),
		"#!/bin/sh\ncase \"$*\" in\n  exec*) echo nope >&2; exit 1 ;;\n  *State*) echo running ;;\n  ps*) echo abc123 ;;\nesac\nexit 0\n")
	got := captureStderr(t, func() { run(t.Context(), a, []string{"container", "list"}) })
	if !strings.Contains(got, "idle stop for x") {
		t.Errorf("no warning: %q", got)
	}
	if strings.Contains(callsIn(t, log), "IDLE-EXEC") {
		t.Error("fell back to a full exec after a probe error")
	}
}
