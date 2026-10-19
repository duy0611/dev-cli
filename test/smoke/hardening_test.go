//go:build smoke

package smoke

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runExpectFail returns dev's combined output, failing the test if dev
// succeeded.
func runExpectFail(t *testing.T, bin string, args ...string) string {
	t.Helper()
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err == nil {
		t.Fatalf("dev %s succeeded; want a refusal\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// gitFixture is the alpine fixture as a git repository with one hook to guard.
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

// TestSmokeGitGuard checks .git stays read-only inside the container, before
// and after host git writes to it. Host writes are watched for seconds, not
// read once: host git renames a new config over the old, which detaches a
// single-file mount a moment later (at once on Linux, about 1.5s on a macOS
// podman machine).
// gitProbes are the paths under ws that must refuse a write, by every spelling
// of .git a case-insensitive disk could honour.
func gitProbes(ws string) map[string]string {
	gitdir := ws + "/.git"
	return map[string]string{
		"config":           gitdir + "/config",
		"info/attributes":  gitdir + "/info/attributes",
		"a new hook":       gitdir + "/hooks/smoke-probe",
		"commondir":        gitdir + "/commondir",
		"CONFIG":           gitdir + "/CONFIG",
		".GIT/config":      ws + "/.GIT/config",
		"HOOKS/a new hook": gitdir + "/HOOKS/smoke-probe-upper",
	}
}

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

	// Reads work; alpine has no git, so a plain read stands in for it.
	if out, err := sh("cat " + gitdir + "/HEAD"); err != nil {
		t.Errorf("reading .git inside failed: %v\n%s", err, out)
	}

	// Every route into .git, including case variants that got past single-file
	// mounts on macOS.
	probes := gitProbes(ws)
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

	// .git must not be renameable out from under the guard.
	if out, err := sh("mv " + gitdir + " " + gitdir + ".old"); err == nil {
		t.Errorf("renaming .git inside succeeded: %s", out)
	}

	// Host git writes that break a single-file mount; each must succeed and the
	// guard must hold.
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

	// A core.hooksPath set after create points host git into the workspace; the
	// running container predates the read-only mount, so it is refused until
	// rebuilt.
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

	// The audit log records the run without setting values.
	if out := dev("audit", "--container", name); !strings.Contains(out, "create") || !strings.Contains(out, "exec") {
		t.Errorf("audit log incomplete:\n%s", out)
	}
}

// TestSmokeEscapeGuardAndDrift checks the escape guard and drift check against
// the real CLI's merged config.
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

	// Edit the config after create; rebuild must refuse.
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

// TestSmokeIsolated checks isolated mode: the folder is copied into a volume
// with uncommitted and untracked files, the agent's commits stay in the
// container until fetched, and remove refuses until then.
func TestSmokeIsolated(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker", "git")
	t.Setenv("DEV_STATE", t.TempDir())
	bin := buildBinary(t)
	project := gitFixture(t)
	// The alpine fixture has no git, which the seed needs; removed before the
	// first commit.
	if err := os.RemoveAll(filepath.Join(project, ".devcontainer")); err != nil {
		t.Fatal(err)
	}
	hostGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = project
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("host git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.WriteFile(filepath.Join(project, "f.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hostGit("add", "f.txt")
	hostGit("commit", "-q", "-m", "init")
	// Uncommitted and untracked, to show the copy carries both.
	if err := os.WriteFile(filepath.Join(project, "f.txt"), []byte("one\nwip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The identity dev passes into the container, distinct from the fixture's.
	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(globalConfig, []byte("[user]\n\tname = agent\n\temail = a@example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)

	const name = "dev-smoke-isolated"
	dev := func(args ...string) string { t.Helper(); return run(t, bin, args...) }
	t.Cleanup(func() { _ = exec.Command(bin, "container", "remove", name, "--force").Run() })

	dev("provider", "configure", providerName, "--kind", "local")
	dev("workspace", "init", workspaceName, "--provider", providerName)
	// The generated config (Ubuntu base) provides the git the seed script
	// needs.
	t.Log("creating an isolated container; the first run pulls a base image")
	dev("container", "create", name, "--folder", project, "--mode", "isolated", "--generate", "--tools", "yq")

	ws := "/workspaces/" + filepath.Base(project)
	sh := func(script string) string {
		t.Helper()
		return dev("container", "exec", name, "--", "sh", "-c", "cd "+ws+" && "+script)
	}
	if got := strings.TrimSpace(sh("git status --porcelain")); !strings.Contains(got, "M f.txt") || !strings.Contains(got, "?? untracked.txt") {
		t.Errorf("the copy's status is %q; want f.txt modified and untracked.txt untracked", got)
	}
	if got := strings.TrimSpace(sh("test -d .git && echo dir")); got != "dir" {
		t.Errorf("the copy's .git is not a directory: %q", got)
	}

	// The agent commits on its own branch, inside the container, as the host's
	// identity and with no gitconfig of its own.
	sh("git switch -q -c agent-work && git add -A && git commit -qm 'from the agent'")
	if got := strings.TrimSpace(sh("git log -1 --format='%an <%ae> / %cn <%ce>'")); got != "agent <a@example.com> / agent <a@example.com>" {
		t.Errorf("the container committed as %q; want the host's identity", got)
	}

	// The host folder is unchanged.
	if got := hostGit("status", "--porcelain"); !strings.Contains(got, "M f.txt") {
		t.Errorf("the host folder changed: %q", got)
	}
	if strings.Contains(hostGit("log", "--all", "--format=%s"), "from the agent") {
		t.Error("the agent's commit reached the host before any fetch")
	}

	// remove refuses while the branch exists only in the container.
	if out := runExpectFail(t, bin, "container", "remove", name); !strings.Contains(out, "fetch") {
		t.Errorf("remove did not refuse unfetched work:\n%s", out)
	}

	dev("container", "fetch", name)
	if got := hostGit("log", "--format=%s", "-1", "refs/remotes/"+name+"/agent-work"); got != "from the agent" {
		t.Errorf("fetch did not bring the agent's branch home: %q", got)
	}

	// Fetched is not kept: the remote-tracking ref is the only copy until
	// the branch is merged or pushed.
	if out := runExpectFail(t, bin, "container", "remove", name); !strings.Contains(out, "agent-work") {
		t.Errorf("remove did not refuse the unmerged branch:\n%s", out)
	}

	hostGit("branch", "agent-work", name+"/agent-work")
	dev("container", "remove", name)
	if got := hostGit("for-each-ref", "refs/remotes/"+name+"/"); got != "" {
		t.Errorf("remove left the fetched refs behind: %q", got)
	}
}

// modeFixture is a committed git repository with no devcontainer config, so a
// generated one (Ubuntu, with the git these tests need) is used.
func modeFixture(t *testing.T) (project string, hostGit func(args ...string) string) {
	t.Helper()
	project = gitFixture(t)
	if err := os.RemoveAll(filepath.Join(project, ".devcontainer")); err != nil {
		t.Fatal(err)
	}
	hostGit = func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = project
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("host git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.WriteFile(filepath.Join(project, "f.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hostGit("add", "f.txt")
	hostGit("commit", "-q", "-m", "init")
	return project, hostGit
}

// TestSmokeUnprotectedRoundTrip checks a container's .git is read-only while
// guarded, writable while unprotected, and read-only again once guarded.
func TestSmokeUnprotectedRoundTrip(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker", "git")
	t.Setenv("DEV_STATE", t.TempDir())
	bin := buildBinary(t)
	project, hostGit := modeFixture(t)

	const name = "dev-smoke-unprotected"
	dev := func(args ...string) string { t.Helper(); return run(t, bin, args...) }
	t.Cleanup(func() { _ = exec.Command(bin, "container", "remove", name, "--force").Run() })

	dev("provider", "configure", providerName, "--kind", "local")
	dev("workspace", "init", workspaceName, "--provider", providerName)
	t.Log("creating a guarded container; the first run pulls a base image")
	dev("container", "create", name, "--folder", project, "--generate", "--tools", "yq")

	ws := "/workspaces/" + filepath.Base(project)
	sh := func(script string) (string, error) {
		t.Helper()
		out, err := exec.Command(bin, "container", "exec", name, "--", "sh", "-c", "cd "+ws+" && "+script).CombinedOutput()
		return string(out), err
	}
	requireReadOnly := func(when string) {
		t.Helper()
		out, err := sh("git commit -q --allow-empty -m guarded-attempt")
		if err == nil || !strings.Contains(out, "Read-only file system") {
			t.Errorf("%s: a commit inside was not refused as read-only: %v\n%s", when, err, out)
		}
		for label, path := range gitProbes(".") {
			if out, err := sh("echo '# smoke-probe' >> " + path); err == nil {
				t.Errorf("%s: a write to %s succeeded inside the container: %s", when, label, out)
			}
		}
	}
	requireReadOnly("guarded")

	dev("container", "rebuild", name, "--mode", "unprotected")
	if out, err := sh("git commit -q --allow-empty -m from-unprotected"); err != nil {
		t.Fatalf("a commit inside an unprotected container failed: %v\n%s", err, out)
	}
	if got := hostGit("log", "-1", "--format=%s"); got != "from-unprotected" {
		t.Errorf("host git log -1 is %q; want the commit made inside", got)
	}

	dev("container", "rebuild", name, "--mode", "guarded")
	requireReadOnly("guarded again")
}

// TestSmokeGuardedToIsolated checks a guarded container becomes isolated with
// the folder's uncommitted and untracked files, that fetch brings the work
// home, and that leaving isolated is refused with exit 2.
func TestSmokeGuardedToIsolated(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker", "git")
	t.Setenv("DEV_STATE", t.TempDir())
	bin := buildBinary(t)
	project, hostGit := modeFixture(t)
	if err := os.WriteFile(filepath.Join(project, "f.txt"), []byte("one\nwip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(globalConfig, []byte("[user]\n\tname = agent\n\temail = a@example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)

	const name = "dev-smoke-to-isolated"
	dev := func(args ...string) string { t.Helper(); return run(t, bin, args...) }
	t.Cleanup(func() { _ = exec.Command(bin, "container", "remove", name, "--force").Run() })

	dev("provider", "configure", providerName, "--kind", "local")
	dev("workspace", "init", workspaceName, "--provider", providerName)
	t.Log("creating a guarded container; the first run pulls a base image")
	dev("container", "create", name, "--folder", project, "--generate", "--tools", "yq")

	dev("container", "rebuild", name, "--mode", "isolated")

	ws := "/workspaces/" + filepath.Base(project)
	sh := func(script string) string {
		t.Helper()
		return dev("container", "exec", name, "--", "sh", "-c", "cd "+ws+" && "+script)
	}
	if got := strings.TrimSpace(sh("git status --porcelain")); !strings.Contains(got, "M f.txt") || !strings.Contains(got, "?? untracked.txt") {
		t.Errorf("the copy's status is %q; want f.txt modified and untracked.txt untracked", got)
	}

	sh("git switch -q -c agent-work && git add -A && git commit -qm 'from the agent'")
	dev("container", "fetch", name)
	if got := hostGit("log", "--format=%s", "-1", "refs/remotes/"+name+"/agent-work"); got != "from the agent" {
		t.Errorf("fetch did not bring the agent's branch home: %q", got)
	}

	cmd := exec.Command(bin, "container", "rebuild", name, "--mode", "guarded")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Errorf("leaving isolated: err %v; want exit 2\n%s", err, out)
	}
	if got := strings.TrimSpace(sh("echo still-running")); got != "still-running" {
		t.Errorf("the container did not survive the refused rebuild: %q", got)
	}
}
