//go:build smoke

package smoke

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// idleScript is the watchdog as installed in the container; `count` prints the
// number of sessions it sees.
const idleScript = "/var/dev-state/idle/watchdog.sh"

// dockerRunning reads the engine's view of a container, not dev's record.
func dockerRunning(t *testing.T, id string) bool {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", id).Output()
	if err != nil {
		t.Fatalf("docker inspect %s: %v", id, err)
	}
	return strings.TrimSpace(string(out)) == "true"
}

// TestSmokeIdleStop checks the idle watchdog on a real engine: an exec session
// keeps the container up, a detached process does not, and the stop is
// reported by the next start and recorded in the audit log.
//
// DEV_IDLE_AFTER and DEV_IDLE_TICK are the smoke-only overrides read when dev
// starts the watchdog; the production values are 30m and 60s.
func TestSmokeIdleStop(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker")
	t.Setenv("DEV_STATE", t.TempDir())
	t.Setenv("DEV_IDLE_AFTER", "20s")
	t.Setenv("DEV_IDLE_TICK", "2s")

	bin := buildBinary(t)
	dev := func(args ...string) string {
		t.Helper()
		return run(t, bin, args...)
	}

	const (
		name = "dev-smoke-idle"
		ws   = "dev-smoke-idle-ws"
		prov = "dev-smoke-idle-local"
	)

	// Killed first, so a failed assertion does not leave the session exec
	// holding the container open for the next run.
	holdCtx, cancelHold := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancelHold()
		_ = exec.Command(bin, "container", "remove", name, "--force").Run()
	})

	dev("provider", "configure", prov, "--kind", "local")
	dev("workspace", "init", ws, "--provider", prov)

	t.Log("creating a folderless generated container; the first run pulls a base image and installs yq")
	dev("container", "create", name, "--no-folder", "--generate", "--tools", "yq")

	ids := dockerPS(t, "-q",
		"--filter", "label=dev.workspace="+ws,
		"--filter", "label=dev.container="+name)
	if len(ids) != 1 {
		t.Fatalf("docker matched %d containers on the dev.* labels, want 1: %v", len(ids), ids)
	}
	id := ids[0]

	// Taken with no other exec open: the probing exec is the one session. This
	// is what shows the ppid-0 rule holds on a real engine.
	if out, err := exec.Command("docker", "exec", id, "sh", idleScript, "count").Output(); err != nil {
		t.Fatalf("watchdog count: %v", err)
	} else if got := strings.TrimSpace(string(out)); got != "1" {
		t.Errorf("watchdog counted %q sessions with only the probe open, want 1", got)
	}

	// A session: an exec that stays open. It must hold the container past the
	// 20s threshold.
	hold := exec.CommandContext(holdCtx, "docker", "exec", id, "sleep", "40")
	if err := hold.Start(); err != nil {
		t.Fatalf("starting the session exec: %v", err)
	}
	holdDone := make(chan error, 1)
	go func() { holdDone <- hold.Wait() }()

	time.Sleep(30 * time.Second)
	if !dockerRunning(t, id) {
		t.Fatal("container stopped while an exec session was open")
	}

	// A detached process, not a session: it is reparented to PID 1 with ppid
	// other than 0, so it must not keep the container alive.
	detached := exec.Command("docker", "exec", id, "sh", "-c", "nohup sleep 600 >/dev/null 2>&1 &")
	if out, err := detached.CombinedOutput(); err != nil {
		t.Fatalf("starting the detached process: %v\n%s", err, out)
	}

	select {
	case <-holdDone:
	case <-time.After(60 * time.Second):
		t.Fatal("the session exec did not end after its 40s sleep")
	}

	// Idle from the moment the session ended: the threshold is 20s, so the
	// stop is well inside the 40s allowance.
	deadline := time.Now().Add(40 * time.Second)
	for dockerRunning(t, id) {
		if time.Now().After(deadline) {
			t.Fatal("container still running 40s after its last session ended; the detached process must not count")
		}
		time.Sleep(time.Second)
	}

	out := dev("container", "start", name)
	if !strings.Contains(out, "container "+name+" was stopped") &&
		!strings.Contains(out, "container "+name+" had its processes stopped") {
		t.Errorf("start did not report the idle stop:\n%s", out)
	}

	if audit := dev("audit", "--container", name, "--event", "idle-stop"); !strings.Contains(audit, "idle-stop") {
		t.Errorf("audit log has no idle-stop record for %s:\n%s", name, audit)
	}
}
