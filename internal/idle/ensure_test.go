package idle

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

// hostProvider runs Exec's argv on the host.
type hostProvider struct {
	provider.Provider
	out string // when set, Exec prints this instead of running anything
}

func (h hostProvider) Exec(ctx context.Context, _ model.Container, cmd []string, o provider.ExecOpts) error {
	if h.out != "" {
		_, err := o.Stdout.Write([]byte(h.out))
		return err
	}
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	c.Stdin, c.Stdout, c.Stderr = o.Stdin, o.Stdout, o.Stderr
	return c.Run()
}

type ensureRig struct {
	state, fallback, idleDir string
}

func writeStub(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// newEnsureRig stubs id as root and the watchdog's kill as a logger, so a
// leaked watchdog can never signal a real process.
func newEnsureRig(t *testing.T) *ensureRig {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the alive check reads /proc")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStub(t, bin, "id", "echo 0")
	writeStub(t, bin, "killstub", `echo "$@" >> "`+filepath.Join(root, "kill.log")+`"`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DEV_IDLE_KILL", filepath.Join(bin, "killstub"))
	r := &ensureRig{state: filepath.Join(root, "state"), fallback: filepath.Join(root, "fallback")}
	if err := os.MkdirAll(r.state, 0o755); err != nil {
		t.Fatal(err)
	}
	r.idleDir = filepath.Join(r.state, "idle")
	t.Cleanup(func() { killPidfile(r.idleDir); killPidfile(r.fallback) })
	return r
}

func killPidfile(dir string) {
	b, err := os.ReadFile(filepath.Join(dir, "pid"))
	if err != nil {
		return
	}
	if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}

var quiet = Options{After: 24 * time.Hour, Tick: time.Hour}

func (r *ensureRig) ensure(t *testing.T) (State, *Stop) {
	t.Helper()
	s, stop, err := ensure(context.Background(), hostProvider{}, model.Container{Name: "box"}, quiet, r.state, r.fallback)
	if err != nil {
		t.Fatal(err)
	}
	return s, stop
}

func waitPid(t *testing.T, dir string) int {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		b, err := os.ReadFile(filepath.Join(dir, "pid"))
		if err == nil && strings.TrimSpace(string(b)) != "" {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			if syscall.Kill(pid, 0) == nil {
				return pid
			}
		}
	}
	t.Fatalf("no live pid in %s", dir)
	return 0
}

func TestEnsureStartsTheWatchdogOnce(t *testing.T) {
	r := newEnsureRig(t)
	s, stop := r.ensure(t)
	if s != Started || stop != nil {
		t.Fatalf("got %v %v", s, stop)
	}
	got, err := os.ReadFile(filepath.Join(r.idleDir, "watchdog.sh"))
	if err != nil || !bytes.Equal(got, Script) {
		t.Fatalf("watchdog.sh not the embedded script: %v", err)
	}
	waitPid(t, r.idleDir)
	before, _ := os.Stat(filepath.Join(r.idleDir, "watchdog.sh"))
	time.Sleep(20 * time.Millisecond)
	if s, _ := r.ensure(t); s != Running {
		t.Fatalf("second call: %v", s)
	}
	after, _ := os.Stat(filepath.Join(r.idleDir, "watchdog.sh"))
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("watchdog.sh rewritten")
	}
}

func TestEnsureLeavesARunningWatchdog(t *testing.T) {
	r := newEnsureRig(t)
	bin := filepath.Dir(os.Getenv("DEV_IDLE_KILL"))
	writeStub(t, bin, "kill", "exit 1")
	if err := os.MkdirAll(r.idleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A process whose cmdline contains watchdog.sh and does nothing.
	sh := filepath.Join(r.idleDir, "watchdog.sh")
	if err := os.WriteFile(sh, []byte("sleep 60\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", sh)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if err := os.WriteFile(filepath.Join(r.idleDir, "pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	if s, _ := r.ensure(t); s != Running {
		t.Fatalf("got %v", s)
	}
	if b, _ := os.ReadFile(sh); string(b) != "sleep 60\n" {
		t.Fatal("script was rewritten")
	}
}

func TestEnsureReplacesAStalePidfile(t *testing.T) {
	r := newEnsureRig(t)
	if err := os.MkdirAll(r.idleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if err := os.WriteFile(filepath.Join(r.idleDir, "pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	if s, _ := r.ensure(t); s != Started {
		t.Fatalf("got %v", s)
	}
	// The cleanup kills from the pidfile, so wait for the watchdog to replace the stale pid.
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if b, _ := os.ReadFile(filepath.Join(r.idleDir, "pid")); strings.TrimSpace(string(b)) != strconv.Itoa(cmd.Process.Pid) {
			return
		}
	}
	t.Fatal("pidfile still stale")
}

func TestEnsureReportsAndClearsTheMarker(t *testing.T) {
	r := newEnsureRig(t)
	if err := os.MkdirAll(r.idleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := filepath.Join(r.idleDir, "marker")
	if err := os.WriteFile(m, []byte("reaped 1760000000 1830\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stop := r.ensure(t)
	want := Stop{Outcome: "reaped", At: time.Unix(1760000000, 0), Idle: 1830 * time.Second}
	if stop == nil || stop.Outcome != want.Outcome || !stop.At.Equal(want.At) || stop.Idle != want.Idle {
		t.Fatalf("got %+v", stop)
	}
	if _, err := os.Stat(m); !os.IsNotExist(err) {
		t.Fatal("marker not removed")
	}
}

func TestEnsureUnavailableWithoutRoot(t *testing.T) {
	r := newEnsureRig(t)
	bin := filepath.Dir(os.Getenv("DEV_IDLE_KILL"))
	writeStub(t, bin, "id", "echo 1000")
	writeStub(t, bin, "sudo", "exit 1")
	s, stop := r.ensure(t)
	if s != Unavailable || stop != nil {
		t.Fatalf("got %v %v", s, stop)
	}
	if _, err := os.Stat(r.idleDir); !os.IsNotExist(err) {
		t.Fatal("something was written")
	}
}

func TestEnsureFallsBackToTmp(t *testing.T) {
	r := newEnsureRig(t)
	r.state = filepath.Join(filepath.Dir(r.state), "missing")
	if s, _ := r.ensure(t); s != Started {
		t.Fatalf("got %v", s)
	}
	waitPid(t, r.fallback)
}

func TestEnsureRejectsUnexpectedOutput(t *testing.T) {
	_, _, err := ensure(context.Background(), hostProvider{out: "hello\n"}, model.Container{Name: "box"}, quiet, "/x", "/y")
	if err == nil || !strings.Contains(err.Error(), "box") || !strings.Contains(err.Error(), "hello") {
		t.Fatalf("got %v", err)
	}
}

func TestEnsureFailsWhenTheDirCannotBeMade(t *testing.T) {
	r := newEnsureRig(t)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r.state = filepath.Join(t.TempDir(), "missing")
	r.fallback = filepath.Join(file, "sub")
	_, _, err := ensure(context.Background(), hostProvider{}, model.Container{Name: "box"}, quiet, r.state, r.fallback)
	if err == nil {
		t.Fatal("want an error")
	}
}

func TestSecondsClampsToOne(t *testing.T) {
	if seconds(500*time.Millisecond) != 1 || seconds(0) != 1 || seconds(90*time.Second) != 90 {
		t.Fatal("bad clamp")
	}
}
