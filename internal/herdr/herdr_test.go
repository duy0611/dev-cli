package herdr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeHerdr writes a stub herdr onto a PATH that replaces the host's, so no
// real daemon is touched.
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

// Installed but not running: the status probe prevents a hang.
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

// Unparsable output is not an error.
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

func TestAgentName(t *testing.T) {
	for in, want := range map[string]string{
		"api":                                "api",
		"Fix-Header":                         "fix-header",
		"web.v2_beta":                        "web-v2_beta",
		"2fa":                                "",
		"_tmp":                               "",
		"a-very-long-container-name-past-32": "a-very-long-container-name-past-",
	} {
		if got := AgentName(in); got != want {
			t.Errorf("AgentName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidAgentName(t *testing.T) {
	for in, want := range map[string]bool{
		"api":                               true,
		"fix_header-2":                      true,
		"Api":                               false,
		"web.v2":                            false,
		"2fa":                               false,
		"":                                  false,
		"a-name-that-is-exactly-33-chars-x": false,
		"a-name-that-is-exactly-32-chars-":  true,
	} {
		if got := ValidAgentName(in); got != want {
			t.Errorf("ValidAgentName(%q) = %v, want %v", in, got, want)
		}
	}
}

// Herdr refuses the rename until it has detected the agent in the pane.
func TestNameAgentRetriesUntilDetected(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "w1:p2")
	retryEvery = time.Millisecond
	t.Cleanup(func() { retryEvery = 500 * time.Millisecond })
	dir := t.TempDir()
	// Builtins only: the stub's PATH holds nothing else.
	log := fakeHerdr(t, "echo x >> '"+dir+"/n'\n"+
		"[ -e '"+dir+"/2' ] && { : > '"+dir+"/ok'; exit 0; }\n"+
		"[ -e '"+dir+"/1' ] && : > '"+dir+"/2'\n"+
		": > '"+dir+"/1'\nexit 1\n")

	stop := NameAgent(t.Context(), "api")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "ok")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the rename never succeeded")
		}
		time.Sleep(time.Millisecond)
	}
	stop()

	n, _ := os.ReadFile(filepath.Join(dir, "n"))
	if c := strings.Count(string(n), "x"); c != 3 {
		t.Errorf("herdr called %d times, want 3: it should stop once the rename succeeds", c)
	}
	argv := readLog(t, log)
	for _, want := range []string{"agent", "rename", "w1:p2", "api"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q is missing %q", argv, want)
		}
	}
}

func TestNameAgentOutsideAPaneDoesNothing(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	log := fakeHerdr(t, "exit 0\n")

	NameAgent(t.Context(), "api")()

	if _, err := os.Stat(log); err == nil {
		t.Error("herdr was called outside a Herdr pane")
	}
}

func TestNameAgentWithoutANameDoesNothing(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "w1:p2")
	log := fakeHerdr(t, "exit 0\n")

	NameAgent(t.Context(), "")()

	if _, err := os.Stat(log); err == nil {
		t.Error("herdr was called with no name")
	}
}
