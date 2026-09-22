package herdr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHerdr writes a stub `herdr` onto a PATH that *replaces* the host's.
//
// Replaced rather than prepended, as the k8s harness does and for the same
// reason: a host with a real herdr installed would otherwise have its daemon
// probed, and its sidebar written to, from a unit test.
func fakeHerdr(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	log := filepath.Join(dir, "herdr.argv")
	body := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> '" + log + "'; done\n" + script
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return log
}

func TestAvailableWhenTheDaemonAnswers(t *testing.T) {
	fakeHerdr(t, "exit 0\n")
	if !Available(t.Context()) {
		t.Error("Available = false with a daemon that answers")
	}
}

// Installed but not running is the case that matters: without the status probe
// every worktree create would hang or fail on a machine where herdr is present
// and the daemon is not.
func TestUnavailableWhenTheDaemonIsDown(t *testing.T) {
	fakeHerdr(t, "exit 1\n")
	if Available(t.Context()) {
		t.Error("Available = true with a daemon that does not answer")
	}
}

func TestUnavailableWhenNotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if Available(t.Context()) {
		t.Error("Available = true with no herdr on PATH")
	}
}

func TestOpenReturnsTheWorkspaceID(t *testing.T) {
	log := fakeHerdr(t, `printf '{"workspace_id":"7f2a"}\n'`+"\nexit 0\n")

	id, err := Open(t.Context(), "/home/u/wt/feat")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if id != "7f2a" {
		t.Errorf("id = %q, want 7f2a", id)
	}
	argv := readLog(t, log)
	for _, want := range []string{"worktree", "open", "--path", "/home/u/wt/feat"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q is missing %q", argv, want)
		}
	}
}

// Output herdr does not promise is not an error: the ID is a convenience for
// the removal path, and a checkout that opened is worth keeping either way.
func TestOpenToleratesUnparsableOutput(t *testing.T) {
	fakeHerdr(t, "printf 'opened\\n'\nexit 0\n")

	id, err := Open(t.Context(), "/home/u/wt/feat")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if id != "" {
		t.Errorf("id = %q, want empty", id)
	}
}

func TestCloseRemovesTheWorkspace(t *testing.T) {
	log := fakeHerdr(t, "exit 0\n")

	if err := Close(t.Context(), "7f2a"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	argv := readLog(t, log)
	for _, want := range []string{"worktree", "remove", "--workspace", "7f2a", "--force"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q is missing %q", argv, want)
		}
	}
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("herdr was never called: %v", err)
	}
	return string(b)
}
