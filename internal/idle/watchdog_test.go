package idle

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeProc struct {
	pid   int
	comm  string
	state string
	ppid  int
}

func writeProc(t *testing.T, proc string, p fakeProc) {
	t.Helper()
	d := filepath.Join(proc, strconv.Itoa(p.pid))
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf("%d (%s) %s %d 0 0\n", p.pid, p.comm, p.state, p.ppid)
	if err := os.WriteFile(filepath.Join(d, "stat"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
}

type rig struct {
	script, proc, dir, log string
	env                    []string
}

// newRig writes the embedded script and a stub kill that logs its arguments
// and, when removeOnTerm is set, deletes the fake PID 1 on "-TERM 1".
func newRig(t *testing.T, procs []fakeProc, removeOnTerm bool) *rig {
	t.Helper()
	root := t.TempDir()
	r := &rig{
		script: filepath.Join(root, "watchdog.sh"),
		proc:   filepath.Join(root, "proc"),
		dir:    filepath.Join(root, "state"),
		log:    filepath.Join(root, "kill.log"),
	}
	for _, d := range []string{r.proc, r.dir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(r.script, Script, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range procs {
		writeProc(t, r.proc, p)
	}
	stub := "#!/bin/sh\necho \"$*\" >> \"" + r.log + "\"\n"
	if removeOnTerm {
		stub += "[ \"$*\" = \"-TERM 1\" ] && rm -r \"$DEV_IDLE_PROC/1\"\nexit 0\n"
	}
	kill := filepath.Join(root, "kill")
	if err := os.WriteFile(kill, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	r.env = append(os.Environ(), "DEV_IDLE_PROC="+r.proc, "DEV_IDLE_KILL="+kill, "DEV_IDLE_GRACE=1")
	return r
}

func (r *rig) cmd(args ...string) *exec.Cmd {
	c := exec.Command("sh", append([]string{r.script}, args...)...)
	c.Env = r.env
	return c
}

func (r *rig) start(t *testing.T, after, tick string) (done chan error) {
	t.Helper()
	c := r.cmd("run", r.dir, after, tick)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill() })
	done = make(chan error, 1)
	go func() { done <- c.Wait() }()
	return done
}

func waitExit(t *testing.T, done chan error, d time.Duration) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watchdog exited with %v", err)
		}
	case <-time.After(d):
		t.Fatalf("watchdog still running after %s", d)
	}
}

func (r *rig) read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWatchdogCountsSessions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		procs []fakeProc
		want  string
	}{
		{"init only", []fakeProc{{1, "sh", "S", 0}}, "0"},
		{"exec'd shell with a child", []fakeProc{{1, "sh", "S", 0}, {40, "bash", "S", 0}, {41, "vim", "S", 40}}, "1"},
		{"orphan reparented to init", []fakeProc{{1, "sh", "S", 0}, {50, "tmux: server", "S", 1}}, "0"},
		{"zombie exec", []fakeProc{{1, "sh", "S", 0}, {60, "sh", "Z", 0}}, "0"},
		{"comm with parens", []fakeProc{{1, "sh", "S", 0}, {70, "a) b", "S", 0}}, "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, tc.procs, false)
			out, err := r.cmd("count").Output()
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Fatalf("count = %q, want %q", got, tc.want)
			}
		})
	}
}

var markerRE = regexp.MustCompile(`^(stopped|reaped) ([0-9]+) ([0-9]+)\n$`)

func TestWatchdogStopsAfterTheThreshold(t *testing.T) {
	r := newRig(t, []fakeProc{{1, "sh", "S", 0}}, true)
	done := r.start(t, "2", "1")
	waitExit(t, done, 10*time.Second)

	if got := r.read(t, r.log); got != "-TERM 1\n" {
		t.Fatalf("kill log = %q", got)
	}
	m := markerRE.FindStringSubmatch(r.read(t, filepath.Join(r.dir, "marker")))
	if m == nil || m[1] != "stopped" {
		t.Fatalf("marker = %q", r.read(t, filepath.Join(r.dir, "marker")))
	}
	if idle, _ := strconv.Atoi(m[3]); idle < 2 {
		t.Fatalf("idle = %d, want >= 2", idle)
	}
	if pid := strings.TrimSpace(r.read(t, filepath.Join(r.dir, "pid"))); pid == "" {
		t.Fatal("pid file empty")
	}
}

func TestWatchdogReapsWhenInitSurvives(t *testing.T) {
	r := newRig(t, []fakeProc{{1, "sh", "S", 0}}, false)
	done := r.start(t, "2", "1")
	waitExit(t, done, 10*time.Second)

	if got := r.read(t, r.log); got != "-TERM 1\n-KILL -1\n" {
		t.Fatalf("kill log = %q", got)
	}
	if got := r.read(t, filepath.Join(r.dir, "marker")); !strings.HasPrefix(got, "reaped ") || !markerRE.MatchString(got) {
		t.Fatalf("marker = %q", got)
	}
}

func TestWatchdogPidFileHoldsItsPid(t *testing.T) {
	r := newRig(t, []fakeProc{{1, "sh", "S", 0}, {40, "bash", "S", 0}}, true)
	c := r.cmd("run", r.dir, "100", "1")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Process.Kill(); _ = c.Wait() }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, _ := os.ReadFile(filepath.Join(r.dir, "pid"))
		if strings.TrimSpace(string(b)) == strconv.Itoa(c.Process.Pid) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid file = %q, want %d", b, c.Process.Pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestWatchdogResetsOnASession(t *testing.T) {
	r := newRig(t, []fakeProc{{1, "sh", "S", 0}, {40, "bash", "S", 0}}, true)
	done := r.start(t, "2", "1")

	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(r.dir, "marker")); err == nil {
		t.Fatal("stopped while a session was attached")
	}
	if err := os.RemoveAll(filepath.Join(r.proc, "40")); err != nil {
		t.Fatal(err)
	}
	waitExit(t, done, 6*time.Second)
	if got := r.read(t, filepath.Join(r.dir, "marker")); !strings.HasPrefix(got, "stopped ") {
		t.Fatalf("marker = %q", got)
	}
}
