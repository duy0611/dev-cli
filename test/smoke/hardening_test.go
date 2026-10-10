//go:build smoke

package smoke

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

// TestSmokeGitGuard drives the git guard against a real engine and the real
// devcontainer CLI, which is the only place the unit tests' assumptions about
// file bind mounts and the CLI's git-root layout get checked.
func TestSmokeGitGuard(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker", "git")
	t.Setenv("DEV_STATE", t.TempDir())
	bin := buildBinary(t)
	project := gitFixture(t)
	const name = "dev-smoke-gitguard"
	dev := func(args ...string) string { t.Helper(); return run(t, bin, args...) }
	t.Cleanup(func() { _ = exec.Command(bin, "container", "remove", name, "--force").Run() })

	dev("provider", "configure", providerName, "--kind", "local")
	dev("workspace", "init", workspaceName, "--provider", providerName)
	t.Log("creating a guarded container; the first run pulls an image")
	dev("container", "create", name, "--folder", project)

	gitdir := "/workspaces/" + filepath.Base(project) + "/.git"
	sh := func(script string) string {
		t.Helper()
		return dev("container", "exec", name, "--", "sh", "-c", script)
	}

	// config is read-only: a write from inside fails, and the host file is
	// untouched.
	sh("if echo '[core]' >> " + gitdir + "/config 2>/dev/null; then echo WROTE; else echo refused; fi | grep -q refused")
	host, err := os.ReadFile(filepath.Join(project, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(host), "fsmonitor") {
		t.Errorf("host .git/config changed: %s", host)
	}

	// hooks are a copy: the project's hook is there, and a hook written inside
	// never reaches the host.
	sh("test -x " + gitdir + "/hooks/pre-commit")
	sh("printf '#!/bin/sh\\necho pwned\\n' > " + gitdir + "/hooks/post-checkout && chmod +x " + gitdir + "/hooks/post-checkout")
	if _, err := os.Stat(filepath.Join(project, ".git", "hooks", "post-checkout")); !os.IsNotExist(err) {
		t.Error("a hook written in the container reached the host")
	}

	// .git cannot be renamed away from under the guard.
	sh("if mv " + gitdir + " " + gitdir + ".old 2>/dev/null; then echo MOVED; else echo refused; fi | grep -q refused")

	// rebuild resets the hooks copy to the project's.
	dev("container", "rebuild", name)
	sh("test ! -e " + gitdir + "/hooks/post-checkout")

	// A planted commondir makes the next command refuse to run.
	if err := os.WriteFile(filepath.Join(project, ".git", "commondir"), []byte("/elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := runExpectFail(t, bin, "container", "exec", name, "--", "true"); !strings.Contains(out, "commondir") {
		t.Errorf("planted commondir not reported:\n%s", out)
	}
	_ = os.Remove(filepath.Join(project, ".git", "commondir"))

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
	gitHost := exec.Command("git", "config", "core.hooksPath", ".githooks")
	gitHost.Dir = project
	if out, err := gitHost.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}
	if out := runExpectFail(t, bin, "container", "exec", name, "--", "true"); !strings.Contains(out, "rebuild") {
		t.Errorf("a container predating core.hooksPath was not refused:\n%s", out)
	}
	dev("container", "rebuild", name)
	tree := "/workspaces/" + filepath.Base(project) + "/.githooks"
	sh("if echo pwned >> " + tree + "/commit-msg 2>/dev/null; then echo WROTE; else echo refused; fi | grep -q refused")
	sh("if mv " + tree + " " + tree + ".old 2>/dev/null; then echo MOVED; else echo refused; fi | grep -q refused")

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
