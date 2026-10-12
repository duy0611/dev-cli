package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/gitwt"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider/k8s"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// initRepo builds a repository with one commit and returns its root.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "."},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "f"}, {"commit", "-qm", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// worktreeWithContainer makes a checkout and a container on it, both named
// name.
func worktreeWithContainer(t *testing.T, a *app, name, repo, path string, opts createOpts) {
	t.Helper()
	wo := checkoutOpts(repo, path)
	wo.branch = name
	if err := runWorktreeCreate(t.Context(), a, "", name, wo); err != nil {
		t.Fatalf("worktree create %s: %v", name, err)
	}
	containerOn(t, a, name, path, opts)
}

// The repository is mounted at its own host path, or git fails inside the
// container.
func TestContainerOnAWorktreeBindsTheRepository(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")
	worktreeWithContainer(t, a, "feat", repo, path, createOpts{})

	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", "feat")
	if err != nil {
		t.Fatalf("container row: %v", err)
	}
	w, err := st.GetWorktree("ws", "feat")
	if err != nil {
		t.Fatalf("worktree row: %v", err)
	}
	if w.Branch != "feat" {
		t.Errorf("branch = %q, want feat", w.Branch)
	}
	// The stored path is git's resolved one, which the mount must match.
	if w.Path != c.Source {
		t.Errorf("worktree path %q and container source %q disagree", w.Path, c.Source)
	}
	// The common directory is what gets mounted, never <repo>/.git.
	if !strings.HasSuffix(w.Repo, ".git") || c.WorktreeRepo != w.Repo {
		t.Errorf("repo = %q, container's = %q; want git's common directory on both", w.Repo, c.WorktreeRepo)
	}
	if !strings.Contains(c.GeneratedConfig, w.Repo) {
		t.Errorf("generated config does not bind the repository:\n%s", c.GeneratedConfig)
	}
}

func TestContainerOnAWorktreeUsesTheProjectsOwnConfig(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)

	// Committed, so the checkout of the branch carries it.
	dc := filepath.Join(repo, ".devcontainer")
	if err := os.MkdirAll(dc, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dc, "devcontainer.json"),
		[]byte(`{"name":"project","image":"ubuntu"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", "add devcontainer"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	path := filepath.Join(t.TempDir(), "wt")
	if err := runWorktreeCreate(t.Context(), a, "", "feat", checkoutOpts(repo, path)); err != nil {
		t.Fatalf("worktree create: %v", err)
	}
	if err := runContainerCreate(t.Context(), a, "", "feat", path, createOpts{noStart: true}); err != nil {
		t.Fatalf("container create: %v", err)
	}

	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", "feat")
	if err != nil {
		t.Fatal(err)
	}
	if c.GeneratedConfig != "" {
		t.Errorf("a project-owned checkout got a generated document:\n%s", c.GeneratedConfig)
	}
	if c.ConfigPath == "" {
		t.Error("the project's own config was not recorded")
	}
	// materialise reads the repository from the row.
	if c.WorktreeRepo == "" {
		t.Error("no repository recorded for a project-owned worktree container")
	}
}

func TestWorktreeCreateOutsideARepository(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := checkoutOpts(t.TempDir(), filepath.Join(t.TempDir(), "wt"))

	err := runWorktreeCreate(t.Context(), a, "", "feat", opts)
	if err == nil {
		t.Fatal("worktree create succeeded outside a repository")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
	if !strings.Contains(err.Error(), "--repo") {
		t.Errorf("error %q does not mention --repo", err)
	}
}

func TestWorktreeCreateOutsideARepositoryWritesNothing(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := checkoutOpts(t.TempDir(), filepath.Join(t.TempDir(), "wt"))
	_ = runWorktreeCreate(t.Context(), a, "", "feat", opts)

	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetWorktree("ws", "feat"); err == nil {
		t.Error("a worktree row was written for a failed create")
	}
}

func TestWorktreeCreateRefusesAnExistingPath(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	path := t.TempDir() // exists

	err := runWorktreeCreate(t.Context(), a, "", "feat", checkoutOpts(repo, path))
	if err == nil {
		t.Fatal("worktree create succeeded onto an existing directory")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
}

func TestWorktreeCreateRejectsBaseWithAnExistingBranch(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	cmd := exec.Command("git", "branch", "existing")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch: %v\n%s", err, out)
	}

	opts := checkoutOpts(repo, filepath.Join(t.TempDir(), "wt"))
	opts.branch = "existing"
	opts.base = "HEAD"

	err := runWorktreeCreate(t.Context(), a, "", "feat", opts)
	if err == nil {
		t.Fatal("worktree create accepted --base for an existing branch")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
}

// k8s has no host bind mounts, so a worktree's absolute paths cannot resolve in
// a pod.
func TestContainerOnAWorktreeRefusesK8s(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	if err := runProviderConfigure(a, "kp", "k8s", k8s.Config{Context: "c", Registry: "r"}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if err := runWorkspaceInit(a, "kws", "kp", false); err != nil {
		t.Fatalf("workspace init: %v", err)
	}
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")
	if err := runWorktreeCreate(t.Context(), a, "kws", "feat", checkoutOpts(repo, path)); err != nil {
		t.Fatalf("worktree create on a k8s workspace: %v", err)
	}

	err := runContainerCreate(t.Context(), a, "kws", "feat", path,
		createOpts{generate: true, noStart: true, tools: []string{"yq"}})
	if got := exitCodeOf(err); got != exitUsage {
		t.Fatalf("exit code = %d (%v), want %d", got, err, exitUsage)
	}
	if !strings.Contains(err.Error(), "local") {
		t.Errorf("error %q does not say the provider must be local", err)
	}
}

func TestWorktreeCreateRefusesATakenName(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	if err := runWorktreeCreate(t.Context(), a, "", "feat",
		checkoutOpts(repo, filepath.Join(t.TempDir(), "wt1"))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "wt2")
	opts := checkoutOpts(repo, path)
	opts.branch = "other"

	err := runWorktreeCreate(t.Context(), a, "", "feat", opts)
	if got := exitCodeOf(err); got != exitUsage {
		t.Fatalf("exit code = %d (%v), want %d", got, err, exitUsage)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the checkout at %s outlived a refused create", path)
	}
}

// A worktree container is SourceFolder, so Remove touches only the state
// volume, never the checkout.
func TestWorktreeContainerIsNotRemovedAsFolderless(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")
	worktreeWithContainer(t, a, "feat", repo, path, createOpts{})

	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", "feat")
	if err != nil {
		t.Fatal(err)
	}
	if c.SourceKind != model.SourceFolder {
		t.Errorf("SourceKind = %q, want folder", c.SourceKind)
	}
}

func TestWorktreeRemoveTakesTheCheckoutAndItsRecord(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")
	if err := runWorktreeCreate(t.Context(), a, "", "feat", checkoutOpts(repo, path)); err != nil {
		t.Fatal(err)
	}

	if err := runWorktreeRemove(t.Context(), a, "", "feat", false); err != nil {
		t.Fatalf("worktree remove: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the checkout at %s survived remove", path)
	}
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetWorktree("ws", "feat"); err == nil {
		t.Error("the worktree row survived remove")
	}
}

func TestWorktreeRemoveKeepsTheBranch(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	if err := runWorktreeCreate(t.Context(), a, "", "feat",
		checkoutOpts(repo, filepath.Join(t.TempDir(), "wt"))); err != nil {
		t.Fatal(err)
	}
	if err := runWorktreeRemove(t.Context(), a, "", "feat", false); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("git", "branch", "--list", "feat")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "feat") {
		t.Error("remove deleted the branch")
	}
}

// The row is removed last, so a refused git remove stays retryable.
func TestWorktreeRemoveKeepsTheRowWhenGitRefuses(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")
	if err := runWorktreeCreate(t.Context(), a, "", "feat", checkoutOpts(repo, path)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "unsaved"), []byte("work"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runWorktreeRemove(t.Context(), a, "", "feat", false); err == nil {
		t.Fatal("remove succeeded on a dirty checkout")
	}
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetWorktree("ws", "feat"); err != nil {
		t.Errorf("the worktree row was dropped despite the refusal: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the dirty checkout was destroyed: %v", err)
	}

	if err := runWorktreeRemove(t.Context(), a, "", "feat", true); err != nil {
		t.Errorf("remove --force on a dirty checkout: %v", err)
	}
}

func TestWorktreeRemoveUnknown(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	err := runWorktreeRemove(t.Context(), a, "", "nope", false)
	if got := exitCodeOf(err); got != exitNotFound {
		t.Errorf("exit code = %d, want %d", got, exitNotFound)
	}
}

// rewriteGeneratedTools re-renders the whole document, so the .git binds are
// rebuilt from the row.
func TestRebuildToolsKeepsTheWorktreeMount(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)

	worktreeWithContainer(t, a, "feat", repo, filepath.Join(t.TempDir(), "wt"), createOpts{})
	if err := rewriteGeneratedTools(t.Context(), a, "", "feat", []string{"+node"}); err != nil {
		t.Fatalf("rebuild --tools: %v", err)
	}

	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", "feat")
	if err != nil {
		t.Fatal(err)
	}
	w, err := st.GetWorktree("ws", "feat")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.GeneratedConfig, w.Repo) {
		t.Errorf("rebuild --tools dropped the repository bind:\n%s", c.GeneratedConfig)
	}
	if !strings.Contains(c.GeneratedConfig, `"workspaceFolder": "`+w.Path+`"`) {
		t.Errorf("rebuild --tools dropped the checkout's host path:\n%s", c.GeneratedConfig)
	}
	if !strings.Contains(c.GeneratedConfig, "node") {
		t.Errorf("rebuild --tools did not add node:\n%s", c.GeneratedConfig)
	}
}

func TestRebuildToolsInventsNoWorktreeMount(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	folder := t.TempDir()
	if err := runContainerCreate(t.Context(), a, "", "plain", folder,
		createOpts{generate: true, noStart: true, tools: []string{"yq"}}); err != nil {
		t.Fatal(err)
	}
	if err := rewriteGeneratedTools(t.Context(), a, "", "plain", []string{"+node"}); err != nil {
		t.Fatalf("rebuild --tools: %v", err)
	}

	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", "plain")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.GeneratedConfig, "workspaceMount") {
		t.Errorf("a plain container grew a workspaceMount:\n%s", c.GeneratedConfig)
	}
}

func TestWorktreeRemoveRefusesAPlainContainer(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateContainer(model.Container{
		Name: "plain", WorkspaceName: "ws",
		SourceKind: model.SourceFolder, Source: "/projects/plain",
	}); err != nil {
		t.Fatal(err)
	}

	err = runWorktreeRemove(t.Context(), a, "", "plain", false)
	if got := exitCodeOf(err); got != exitNotFound {
		t.Errorf("exit code = %d, want %d", got, exitNotFound)
	}
}

func TestContainerRemoveLeavesTheWorktree(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")
	worktreeWithContainer(t, a, "feat", repo, path, createOpts{})

	if err := runContainerRemove(t.Context(), a, "", "feat", true); err != nil {
		t.Fatalf("container remove: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("container remove deleted the checkout: %v", err)
	}
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetWorktree("ws", "feat"); err != nil {
		t.Errorf("container remove took the worktree record: %v", err)
	}
}

// captureStderr returns what warnf wrote to os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()

	fn()
	w.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// --- carrying ignored files over ---------------------------------------------

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// seededRepo is a repository with an ignored .env and node_modules.
func seededRepo(t *testing.T) string {
	t.Helper()
	repo := initRepo(t)
	writeFile(t, filepath.Join(repo, ".gitignore"), ".env\nnode_modules/\n")
	gitIn(t, repo, "add", ".gitignore")
	gitIn(t, repo, "commit", "-qm", "ignore")
	writeFile(t, filepath.Join(repo, ".env"), "TOKEN=1")
	writeFile(t, filepath.Join(repo, "node_modules", "a", "index.js"), "a")
	return repo
}

func TestWorktreeCreateCopiesTheListedIgnoredFiles(t *testing.T) {
	requireGit(t)
	a, out := newTestApp(t)
	seedWorkspace(t, a)
	repo := seededRepo(t)
	// Uncommitted: the manifest is read from the source checkout.
	writeFile(t, filepath.Join(repo, ".worktreeinclude"), ".env\nnode_modules/\n")
	path := filepath.Join(t.TempDir(), "wt")

	if err := runWorktreeCreate(t.Context(), a, "", "feat", checkoutOpts(repo, path)); err != nil {
		t.Fatalf("worktree create: %v", err)
	}

	if b, err := os.ReadFile(filepath.Join(path, ".env")); err != nil || string(b) != "TOKEN=1" {
		t.Errorf(".env in the checkout = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(path, "node_modules", "a", "index.js")); err != nil {
		t.Errorf("node_modules was not carried over: %v", err)
	}
	if !strings.Contains(out.String(), "copied 2 ignored paths") {
		t.Errorf("output does not report the copy:\n%s", out)
	}
}

func TestWorktreeCreateCopiesNothingUnasked(t *testing.T) {
	requireGit(t)
	a, out := newTestApp(t)
	seedWorkspace(t, a)
	repo := seededRepo(t)
	path := filepath.Join(t.TempDir(), "wt")

	if err := runWorktreeCreate(t.Context(), a, "", "feat", checkoutOpts(repo, path)); err != nil {
		t.Fatalf("worktree create: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, ".env")); !os.IsNotExist(err) {
		t.Errorf(".env was copied with nothing asking for it")
	}
	if strings.Contains(out.String(), "copied") {
		t.Errorf("output mentions a copy that did not happen:\n%s", out)
	}
}

func TestWorktreeCreateIncludeFileAddsToTheManifest(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := seededRepo(t)
	writeFile(t, filepath.Join(repo, ".worktreeinclude"), ".env\n")
	extra := filepath.Join(t.TempDir(), "extra")
	writeFile(t, extra, "node_modules/\n")
	path := filepath.Join(t.TempDir(), "wt")

	opts := checkoutOpts(repo, path)
	opts.includeFile = extra
	if err := runWorktreeCreate(t.Context(), a, "", "feat", opts); err != nil {
		t.Fatalf("worktree create: %v", err)
	}
	for _, p := range []string{".env", "node_modules/a/index.js"} {
		if _, err := os.Stat(filepath.Join(path, p)); err != nil {
			t.Errorf("%s was not carried over: %v", p, err)
		}
	}
}

func TestWorktreeCreateRefusesAMissingIncludeFile(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := seededRepo(t)
	path := filepath.Join(t.TempDir(), "wt")

	opts := checkoutOpts(repo, path)
	opts.includeFile = filepath.Join(t.TempDir(), "nope")
	err := runWorktreeCreate(t.Context(), a, "", "feat", opts)
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d (%v), want %d", got, err, exitUsage)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a checkout was made before the flag was checked")
	}
}

func TestWorktreeCreateRollsBackAFailedCopy(t *testing.T) {
	requireGit(t)
	if os.Getuid() == 0 {
		t.Skip("root reads unreadable files")
	}
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := seededRepo(t)
	writeFile(t, filepath.Join(repo, ".worktreeinclude"), ".env\n")
	if err := os.Chmod(filepath.Join(repo, ".env"), 0o000); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "wt")

	err := runWorktreeCreate(t.Context(), a, "", "feat", checkoutOpts(repo, path))
	if err == nil || !strings.Contains(err.Error(), ".env") {
		t.Fatalf("worktree create = %v, want an error naming .env", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the checkout at %s outlived a failed copy", path)
	}
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetWorktree("ws", "feat"); err == nil {
		t.Error("a worktree row outlived a failed copy")
	}
}

func TestWorktreeCreateCopiesAnIgnoredDevcontainer(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	writeFile(t, filepath.Join(repo, ".gitignore"), ".devcontainer/\n")
	gitIn(t, repo, "add", ".gitignore")
	gitIn(t, repo, "commit", "-qm", "ignore")
	writeFile(t, filepath.Join(repo, ".devcontainer", "devcontainer.json"),
		`{"name":"project","image":"ubuntu"}`)
	writeFile(t, filepath.Join(repo, ".worktreeinclude"), ".devcontainer/\n")

	path := filepath.Join(t.TempDir(), "wt")
	if err := runWorktreeCreate(t.Context(), a, "", "feat", checkoutOpts(repo, path)); err != nil {
		t.Fatalf("worktree create: %v", err)
	}
	if err := runContainerCreate(t.Context(), a, "", "feat", path, createOpts{noStart: true}); err != nil {
		t.Fatalf("container create: %v", err)
	}
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", "feat")
	if err != nil {
		t.Fatal(err)
	}
	if c.ConfigPath == "" || c.GeneratedConfig != "" {
		t.Errorf("the copied config was not used: path %q, generated %q",
			c.ConfigPath, c.GeneratedConfig)
	}
}

func TestWorktreeCreateIncludeFileFromABareRepository(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	bare := filepath.Join(t.TempDir(), "bare.git")
	gitIn(t, t.TempDir(), "clone", "-q", "--bare", initRepo(t), bare)
	extra := filepath.Join(t.TempDir(), "extra")
	writeFile(t, extra, ".env\n")

	opts := checkoutOpts(bare, filepath.Join(t.TempDir(), "wt"))
	opts.includeFile = extra
	err := runWorktreeCreate(t.Context(), a, "", "feat", opts)
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d (%v), want %d", got, err, exitUsage)
	}

	opts = checkoutOpts(bare, filepath.Join(t.TempDir(), "wt"))
	if err := runWorktreeCreate(t.Context(), a, "", "feat", opts); err != nil {
		t.Errorf("worktree create from a bare repository without the flag: %v", err)
	}
}

// Copied files are ignored, and git removes ignored files with the checkout.
func TestWorktreeRemoveNeedsNoForceAfterACopy(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := seededRepo(t)
	writeFile(t, filepath.Join(repo, ".worktreeinclude"), ".env\nnode_modules/\n")
	path := filepath.Join(t.TempDir(), "wt")
	if err := runWorktreeCreate(t.Context(), a, "", "feat", checkoutOpts(repo, path)); err != nil {
		t.Fatalf("worktree create: %v", err)
	}

	if err := gitwt.Remove(t.Context(), repo, path, false); err != nil {
		t.Errorf("removing a seeded checkout without --force: %v", err)
	}
}

// --- container create on a worktree folder ----------------------------------

func TestContainerCreateOnAWorktreeFolderRecordsItsRepository(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "feat", path)

	if err := runContainerCreate(t.Context(), a, "", "feat", path,
		createOpts{generate: true, noStart: true, tools: []string{"yq"}}); err != nil {
		t.Fatalf("container create on a worktree: %v", err)
	}

	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", "feat")
	if err != nil {
		t.Fatal(err)
	}
	want, err := gitwt.CommonDir(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if c.WorktreeRepo != want {
		t.Errorf("worktree_repo = %q, want the repository's common directory %q", c.WorktreeRepo, want)
	}
	if !strings.Contains(c.GeneratedConfig, want) {
		t.Errorf("generated config does not bind the repository:\n%s", c.GeneratedConfig)
	}
	if !strings.Contains(c.GeneratedConfig, `"workspaceFolder": "`+c.Source+`"`) {
		t.Errorf("generated config does not mount the checkout at its host path:\n%s", c.GeneratedConfig)
	}
}

func TestContainerCreateOnAPlainCheckoutRecordsNoRepository(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)

	if err := runContainerCreate(t.Context(), a, "", "api", repo,
		createOpts{generate: true, noStart: true, tools: []string{"yq"}}); err != nil {
		t.Fatalf("container create: %v", err)
	}
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", "api")
	if err != nil {
		t.Fatal(err)
	}
	if c.WorktreeRepo != "" {
		t.Errorf("worktree_repo = %q, want empty for an ordinary checkout", c.WorktreeRepo)
	}
}

// --- worktree create makes the checkout only -------------------------------

func TestWorktreeCreateMakesOnlyTheCheckout(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")

	if err := runWorktreeCreate(t.Context(), a, "", "feat", checkoutOpts(repo, path)); err != nil {
		t.Fatalf("worktree create: %v", err)
	}
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetContainer("ws", "feat"); err == nil {
		t.Error("worktree create made a container; that is container create's job now")
	}
	w, err := st.GetWorktree("ws", "feat")
	if err != nil {
		t.Fatalf("worktree row: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.Path, ".git")); err != nil {
		t.Errorf("no checkout at %s: %v", w.Path, err)
	}
}

// Removing under a running container would pull its folder out from under it.
func TestWorktreeRemoveRefusesWhileAContainerUsesIt(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")
	if err := runWorktreeCreate(t.Context(), a, "", "feat", checkoutOpts(repo, path)); err != nil {
		t.Fatal(err)
	}
	containerOn(t, a, "agent", path, createOpts{})

	err := runWorktreeRemove(t.Context(), a, "", "feat", true)
	if got := exitCodeOf(err); got != exitUsage {
		t.Fatalf("exit code = %d (%v), want %d", got, err, exitUsage)
	}
	if !strings.Contains(err.Error(), "agent") {
		t.Errorf("error %q does not name the container", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the checkout went despite the refusal: %v", err)
	}
}

// A cascade delete would leave checkouts on disk with no record.
func TestWorkspaceRemoveRefusesWhileItHasWorktrees(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	if err := runWorktreeCreate(t.Context(), a, "", "feat",
		checkoutOpts(repo, filepath.Join(t.TempDir(), "wt"))); err != nil {
		t.Fatal(err)
	}

	err := runWorkspaceRemove(a, "ws")
	if got := exitCodeOf(err); got != exitUsage {
		t.Fatalf("exit code = %d (%v), want %d", got, err, exitUsage)
	}
	if !strings.Contains(err.Error(), "feat") {
		t.Errorf("error %q does not name the worktree", err)
	}
}

func TestWorktreeListNamesTheContainersOnIt(t *testing.T) {
	requireGit(t)
	a, out := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")
	if err := runWorktreeCreate(t.Context(), a, "", "feat", checkoutOpts(repo, path)); err != nil {
		t.Fatal(err)
	}
	containerOn(t, a, "agent-one", path, createOpts{})
	out.Reset()

	if err := runWorktreeList(t.Context(), a, "", false); err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	if !strings.Contains(out.String(), "agent-one") {
		t.Errorf("list does not name the container on the checkout:\n%s", out)
	}
}

// checkoutOpts is worktreeOpts with herdr kept out of the test.
func checkoutOpts(repo, path string) worktreeOpts {
	return worktreeOpts{repo: repo, branch: "feat", path: path, noHerdr: true}
}

// containerOn creates a generated, unstarted container on path, so no project
// config or engine is needed.
func containerOn(t *testing.T, a *app, name, path string, opts createOpts) model.Container {
	t.Helper()
	opts.noStart = true
	if opts.from == "" && !opts.generate && len(opts.tools) == 0 {
		opts.generate, opts.tools = true, []string{"yq"}
	}
	if err := runContainerCreate(t.Context(), a, "", name, path, opts); err != nil {
		t.Fatalf("container create %s on %s: %v", name, path, err)
	}
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", name)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
