package k8s

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// kubectl exec starts as root with HOME=/root, and su -p keeps it. The switched
// command must see the remote user's home instead.
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
	stub("id", `if [ "$1" = -u ]; then echo 0; else echo root; fi`+"\n")
	// $4 is CMD in `su -p USER -c CMD`.
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

// pam_env resets PATH to /etc/environment even under su -p, dropping feature
// bins such as nvm's. The pod's PATH must survive the switch.
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
	// PAM resets PATH, dropping the stub directory.
	stub("su", `PATH=/nonexistent; export PATH; exec `+sh+` -c "$4"`+"\n")
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
