//go:build smoke

package smoke

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// smokeRepo makes a repository with one commit at parent/name.
func smokeRepo(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "."},
		{"config", "user.email", "smoke@example.com"},
		{"config", "user.name", "smoke"},
		{"config", "commit.gpgsign", "false"},
		{"add", "f.txt"},
		{"commit", "-q", "-m", "init"},
	} {
		gitRun(t, dir, args...)
	}
	return dir
}

// TestSmokeGuardedFolders checks every repository folder's .git is read-only
// inside, an edit inside reaches the host, and an added folder is guarded from
// the rebuilt container's first command.
func TestSmokeGuardedFolders(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker", "git")
	t.Setenv("DEV_STATE", t.TempDir())
	bin := buildBinary(t)
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	repo1 := smokeRepo(t, parent, "repo-1")
	repo2 := smokeRepo(t, parent, "repo-2")
	repo3 := smokeRepo(t, parent, "repo-3")
	if err := os.CopyFS(filepath.Join(repo1, ".devcontainer"), os.DirFS(filepath.Join(fixtureProject(t), ".devcontainer"))); err != nil {
		t.Fatal(err)
	}

	const name = "dev-smoke-folders"
	dev := func(args ...string) string { t.Helper(); return run(t, bin, args...) }
	t.Cleanup(func() { _ = exec.Command(bin, "container", "remove", name, "--force").Run() })

	dev("provider", "configure", providerName, "--kind", "local")
	dev("workspace", "init", workspaceName, "--provider", providerName)
	dev("container", "create", name, "--folder", repo1, "--folder", repo2)

	sh := func(script string) (string, error) {
		t.Helper()
		out, err := exec.Command(bin, "container", "exec", name, "--", "sh", "-c", script).CombinedOutput()
		return string(out), err
	}
	readOnly := func(repo string) {
		t.Helper()
		config := "/workspaces/" + filepath.Base(repo) + "/.git/config"
		out, err := sh("echo '# smoke-probe' >> " + config)
		if err == nil {
			t.Errorf("a write to %s succeeded inside the container", config)
		} else if !strings.Contains(out, "Read-only file system") {
			t.Errorf("a write to %s failed, but not with EROFS: %s", config, out)
		}
	}
	readOnly(repo1)
	readOnly(repo2)

	if out, err := sh("echo inside >> /workspaces/repo-2/f.txt"); err != nil {
		t.Fatalf("editing repo-2 inside: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(repo2, "f.txt")); !strings.Contains(string(b), "inside") {
		t.Errorf("the edit inside did not reach the host: %q", b)
	}

	dev("container", "rebuild", name, "--add-folder", repo3)
	readOnly(repo3)
}

// TestSmokeIsolatedFolders checks each folder is copied, and that fetch and
// remove handle each repository on its own.
func TestSmokeIsolatedFolders(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker", "git")
	t.Setenv("DEV_STATE", t.TempDir())
	bin := buildBinary(t)
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	repo1 := smokeRepo(t, parent, "repo-1")
	repo2 := smokeRepo(t, parent, "repo-2")

	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(globalConfig, []byte("[user]\n\tname = agent\n\temail = a@example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)

	const name = "dev-smoke-isolated-folders"
	dev := func(args ...string) string { t.Helper(); return run(t, bin, args...) }
	t.Cleanup(func() { _ = exec.Command(bin, "container", "remove", name, "--force").Run() })

	dev("provider", "configure", providerName, "--kind", "local")
	dev("workspace", "init", workspaceName, "--provider", providerName)
	dev("container", "create", name, "--folder", repo1, "--folder", repo2,
		"--mode", "isolated", "--generate", "--tools", "yq")

	for _, repo := range []string{repo1, repo2} {
		base := filepath.Base(repo)
		dev("container", "exec", name, "--", "sh", "-c",
			"cd /workspaces/"+base+" && git switch -q -c work-"+base+" && git commit -q --allow-empty -m 'in "+base+"'")
	}

	if out := runExpectFail(t, bin, "container", "remove", name); !strings.Contains(out, "repo-1") || !strings.Contains(out, "repo-2") {
		t.Errorf("remove did not refuse naming both folders:\n%s", out)
	}

	dev("container", "fetch", name)
	for _, repo := range []string{repo1, repo2} {
		base := filepath.Base(repo)
		if got := gitOut(t, repo, "log", "--format=%s", "-1", "refs/remotes/"+name+"/work-"+base); strings.TrimSpace(got) != "in "+base {
			t.Errorf("fetch did not bring %s's branch home: %q", base, got)
		}
	}
	if out := runExpectFail(t, bin, "container", "remove", name); !strings.Contains(out, "is fetched") {
		t.Errorf("remove did not refuse the unmerged branches:\n%s", out)
	}
	for _, repo := range []string{repo1, repo2} {
		gitRun(t, repo, "branch", "kept", name+"/work-"+filepath.Base(repo))
	}
	dev("container", "remove", name)
}
