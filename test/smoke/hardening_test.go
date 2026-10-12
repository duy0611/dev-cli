//go:build smoke

package smoke

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runExpectFail runs dev and returns its combined output, failing the test if
// the command succeeded.
func runExpectFail(t *testing.T, bin string, args ...string) string {
	t.Helper()
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err == nil {
		t.Fatalf("dev %s succeeded; want a refusal\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// gitFixture is the alpine fixture project, made a git repository with one
// hook, so the git guard has something to guard.
func gitFixture(t *testing.T) string {
	t.Helper()
	requireBinaries(t, "git")
	dir := fixtureProject(t)
	for _, args := range [][]string{
		{"init", "-q", "."},
		{"config", "user.email", "smoke@example.com"},
		{"config", "user.name", "smoke"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestSmokeGitGuard drives guarded mode against a real engine and the real
// devcontainer CLI: .git is read-only whole, by any name, before and after host
// git writes its config, and host git keeps working beside the container.
//
// The host writes are the point. The single-file guard this replaced held until
// host git renamed a new config over the old one, which detaches a file mount
// on Linux at once and on a macOS podman machine about 1.5s later; a directory
// mount is never renamed over. Each check after a host write is therefore
// watched for a few seconds rather than made once.
func TestSmokeGitGuard(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker", "git")
	t.Setenv("DEV_STATE", t.TempDir())
	bin := buildBinary(t)
	project := gitFixture(t)
	hostGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = project
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("host git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(project, "f.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hostGit("add", "f.txt")
	hostGit("commit", "-q", "-m", "init")

	const name = "dev-smoke-gitguard"
	dev := func(args ...string) string { t.Helper(); return run(t, bin, args...) }
	t.Cleanup(func() { _ = exec.Command(bin, "container", "remove", name, "--force").Run() })

	dev("provider", "configure", providerName, "--kind", "local")
	dev("workspace", "init", workspaceName, "--provider", providerName)
	t.Log("creating a guarded container; the first run pulls an image")
	dev("container", "create", name, "--folder", project)

	ws := "/workspaces/" + filepath.Base(project)
	gitdir := ws + "/.git"
	sh := func(script string) (string, error) {
		t.Helper()
		out, err := exec.Command(bin, "container", "exec", name, "--", "sh", "-c", script).CombinedOutput()
		return string(out), err
	}

	// The cost of the mode: reading works, writing to the repository does not.
	// alpine has no git, so the read side is checked with a plain read; git's
	// own refusal is the same EROFS any writer gets.
	if out, err := sh("cat " + gitdir + "/HEAD"); err != nil {
		t.Errorf("reading .git inside failed: %v\n%s", err, out)
	}

	// Every route into .git, by any spelling: the exact names, a new file, and
	// the case variants that got past single-file mounts on macOS.
	probes := map[string]string{
		"config":           gitdir + "/config",
		"info/attributes":  gitdir + "/info/attributes",
		"a new hook":       gitdir + "/hooks/smoke-probe",
		"commondir":        gitdir + "/commondir",
		"CONFIG":           gitdir + "/CONFIG",
		".GIT/config":      ws + "/.GIT/config",
		"HOOKS/a new hook": gitdir + "/HOOKS/smoke-probe-upper",
	}
	tryAll := func(when string) {
		t.Helper()
		for label, path := range probes {
			if out, err := sh("echo '# smoke-probe' >> " + path); err == nil {
				t.Errorf("%s: a write to %s succeeded inside the container: %s", when, label, out)
			}
		}
		for _, f := range []string{"commondir", filepath.Join("hooks", "smoke-probe"), filepath.Join("hooks", "smoke-probe-upper")} {
			if _, err := os.Stat(filepath.Join(project, ".git", f)); err == nil {
				t.Errorf("%s: the container created .git/%s on the host", when, f)
			}
		}
		b, err := os.ReadFile(filepath.Join(project, ".git", "config"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "smoke-probe") {
			t.Errorf("%s: a write from the container reached the host's .git/config:\n%s", when, b)
		}
	}
	tryAll("before any host write")

	// .git cannot be renamed away from under the guard.
	if out, err := sh("mv " + gitdir + " " + gitdir + ".old"); err == nil {
		t.Errorf("renaming .git inside succeeded: %s", out)
	}

	// What broke the single-file guard: host git rewriting its config, setting
	// an upstream, committing. Each must succeed, and the guard must hold.
	for _, args := range [][]string{
		{"config", "smoke.host", "1"},
		{"branch", "smoke-side"},
		{"branch", "-u", strings.TrimSpace(gitOut(t, project, "rev-parse", "--abbrev-ref", "HEAD")), "smoke-side"},
		{"commit", "-q", "--allow-empty", "-m", "host commit while the container runs"},
	} {
		hostGit(args...)
		label := "after host git " + strings.Join(args, " ")
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := sh("( : >> " + gitdir + "/config ) 2>/dev/null"); err == nil {
				t.Errorf("%s: .git/config opens for write inside the container", label)
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		tryAll(label)
	}

	// A core.hooksPath set on the host after create — `make hooks`, husky —
	// points host git into the workspace. The running container predates it,
	// so it is refused until a rebuild gives it the read-only mount.
	hooksDir := filepath.Join(project, ".githooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "commit-msg"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	hostGit("config", "core.hooksPath", ".githooks")
	if out := runExpectFail(t, bin, "container", "exec", name, "--", "true"); !strings.Contains(out, "rebuild") {
		t.Errorf("a container predating core.hooksPath was not refused:\n%s", out)
	}
	dev("container", "rebuild", name)
	tree := ws + "/.githooks"
	if out, err := sh("echo pwned >> " + tree + "/commit-msg"); err == nil {
		t.Errorf("a write to the tree hooks succeeded: %s", out)
	}
	if out, err := sh("mv " + tree + " " + tree + ".old"); err == nil {
		t.Errorf("renaming the tree hooks succeeded: %s", out)
	}

	// The audit log holds the run, and no setting value.
	if out := dev("audit", "--container", name); !strings.Contains(out, "create") || !strings.Contains(out, "exec") {
		t.Errorf("audit log incomplete:\n%s", out)
	}
}

// TestSmokeEscapeGuardAndDrift checks the escape guard and the drift check
// against the real CLI's merged configuration.
func TestSmokeEscapeGuardAndDrift(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker")
	t.Setenv("DEV_STATE", t.TempDir())
	bin := buildBinary(t)
	const name = "dev-smoke-escape"
	dev := func(args ...string) string { t.Helper(); return run(t, bin, args...) }
	t.Cleanup(func() { _ = exec.Command(bin, "container", "remove", name, "--force").Run() })

	dev("provider", "configure", providerName, "--kind", "local")
	dev("workspace", "init", workspaceName, "--provider", providerName)

	project := fixtureProject(t)
	config := filepath.Join(project, ".devcontainer", "devcontainer.json")
	privileged := strings.Replace(fixtureConfig, `"name"`, `"privileged": true, "name"`, 1)
	if err := os.WriteFile(config, []byte(privileged), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := runExpectFail(t, bin, "container", "create", name, "--folder", project); !strings.Contains(out, "privileged") {
		t.Errorf("privileged config not refused:\n%s", out)
	}
	if out := dev("container", "list"); strings.Contains(out, name) {
		t.Errorf("a refused create left a row:\n%s", out)
	}

	// Back to the plain fixture, created; then an edit makes rebuild refuse.
	if err := os.WriteFile(config, []byte(fixtureConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Log("creating the container; the first run pulls an image")
	dev("container", "create", name, "--folder", project)
	edited := strings.Replace(fixtureConfig, `"name"`, `"initializeCommand": "true", "name"`, 1)
	if err := os.WriteFile(config, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := runExpectFail(t, bin, "container", "rebuild", name); !strings.Contains(out, "initializeCommand") {
		t.Errorf("drift not refused naming the field:\n%s", out)
	}
	dev("container", "rebuild", name, "--accept-config")
}
