package k8s

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The pod is root, so kubectl exec starts with HOME=/root, and su -p keeps it.
// A command switched to remoteUser must see that user's home instead: tools
// that write under ~ otherwise try /root and fail with permission denied, as
// opencode did creating ~/.local/share/opencode.
func TestAsRemoteUserSetsTheUsersHome(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	stub := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!"+sh+"\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A root pod: id reports uid 0 and user root.
	stub("id", `if [ "$1" = -u ]; then echo 0; else echo root; fi`+"\n")
	// su as root's environment reaches it, running its -c command the way
	// the real one does: `su -p USER -c CMD`.
	stub("su", `exec `+sh+` -c "$4"`+"\n")
	stub("whoami-env", `echo "$HOME $USER $LOGNAME"`+"\n")
	if err := os.Symlink(sh, filepath.Join(dir, "sh")); err != nil {
		t.Fatal(err)
	}

	argv := asRemoteUser("vscode", "/home/vscode", "", []string{"whoami-env"})
	cmd := exec.Command(sh, argv[1:]...)
	cmd.Env = []string{"PATH=" + dir, "HOME=/root", "USER=root", "LOGNAME=root"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	if got, want := strings.TrimSpace(string(out)), "/home/vscode vscode vscode"; got != want {
		t.Errorf("switched command sees %q, want %q", got, want)
	}
}

// su runs PAM, and pam_env replaces PATH with /etc/environment's even under -p.
// The devcontainer CLI rewrites that file with the image's PATH when it starts
// a container, which never happens on k8s — so the file keeps the distro
// default and anything a feature put on PATH (npm's global bin under nvm)
// vanishes for the switched command. The pod's PATH has to survive the switch.
func TestAsRemoteUserKeepsThePodsPath(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	stub := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!"+sh+"\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub("id", `if [ "$1" = -u ]; then echo 0; else echo root; fi`+"\n")
	// su as PAM leaves it: PATH reset to a default that lacks the stub
	// directory. Called as `su -p USER -c CMD`.
	stub("su", `PATH=/nonexistent; export PATH; exec `+sh+` -c "$4"`+"\n")
	// Only findable if the pod's PATH was restored.
	stub("showpath", `echo "$PATH"`+"\n")
	if err := os.Symlink(sh, filepath.Join(dir, "sh")); err != nil {
		t.Fatal(err)
	}

	argv := asRemoteUser("vscode", "/home/vscode", "", []string{"showpath"})
	cmd := exec.Command(sh, argv[1:]...)
	cmd.Env = []string{"PATH=" + dir, "HOME=/root"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != dir {
		t.Errorf("switched command sees PATH %q, want the pod's %q", got, dir)
	}
}
