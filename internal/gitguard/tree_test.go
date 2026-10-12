package gitguard

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// treeRepo is a repository with no global or system config, so FindTree sees
// only what the test sets.
func treeRepo(t *testing.T) (string, []Root) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := repo(t)
	return dir, []Root{{Host: dir, Container: "/workspaces/r"}}
}

func shieldedOf(dir string) []string {
	return []string{filepath.Join(dir, ".git", "config"), filepath.Join(dir, ".git", "hooks")}
}

func find(t *testing.T, dir string, roots []Root) []TreePath {
	t.Helper()
	got, err := FindTree(t.Context(), dir, roots, shieldedOf(dir))
	if err != nil {
		t.Fatalf("FindTree: %v", err)
	}
	return got
}

// An ordinary checkout runs nothing from its working tree.
func TestFindTreeOfAPlainRepositoryIsEmpty(t *testing.T) {
	dir, roots := treeRepo(t)
	if got := find(t, dir, roots); len(got) != 0 {
		t.Errorf("found %+v", got)
	}
}

// core.hooksPath into the checkout is the case this guards against.
func TestFindTreeFindsAHooksPathInTheCheckout(t *testing.T) {
	dir, roots := treeRepo(t)
	writeFile(t, filepath.Join(dir, ".githooks", "commit-msg"), "#!/bin/sh\n")
	git(t, dir, "config", "core.hooksPath", ".githooks")

	got := find(t, dir, roots)
	if len(got) != 1 {
		t.Fatalf("found %+v", got)
	}
	p := got[0]
	if p.Host != filepath.Join(dir, ".githooks") || p.Container != "/workspaces/r/.githooks" ||
		p.Why != "core.hooksPath" || p.Unmountable() != "" {
		t.Errorf("got %+v", p)
	}
}

// FindTree asks from the top so a relative hooksPath means the same thing.
func TestFindTreeFromASubfolder(t *testing.T) {
	dir, roots := treeRepo(t)
	writeFile(t, filepath.Join(dir, ".githooks", "commit-msg"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(dir, "sub", "f"), "")
	git(t, dir, "config", "core.hooksPath", ".githooks")

	got, err := FindTree(t.Context(), filepath.Join(dir, "sub"), roots, shieldedOf(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Host != filepath.Join(dir, ".githooks") {
		t.Errorf("found %+v", got)
	}
}

// A hooksPath outside the workspace is not reachable from the container.
func TestFindTreeIgnoresAHooksPathOutside(t *testing.T) {
	dir, roots := treeRepo(t)
	elsewhere, _ := filepath.EvalSymlinks(t.TempDir())
	git(t, dir, "config", "core.hooksPath", elsewhere)
	if got := find(t, dir, roots); len(got) != 0 {
		t.Errorf("found %+v", got)
	}
}

// A missing path is found and unmountable.
func TestFindTreeReportsAMissingHooksPath(t *testing.T) {
	dir, roots := treeRepo(t)
	git(t, dir, "config", "core.hooksPath", ".husky")
	got := find(t, dir, roots)
	if len(got) != 1 || !got[0].Missing || got[0].Unmountable() == "" {
		t.Errorf("found %+v", got)
	}
}

// A symlink in the checkout is reported as reached through, not followed.
func TestFindTreeReportsASymlinkInTheCheckout(t *testing.T) {
	dir, roots := treeRepo(t)
	writeFile(t, filepath.Join(dir, "real", "commit-msg"), "#!/bin/sh\n")
	if err := os.Symlink("real", filepath.Join(dir, "hooks-link")); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "config", "core.hooksPath", "hooks-link")
	got := find(t, dir, roots)
	if len(got) != 1 || got[0].Symlink != filepath.Join(dir, "hooks-link") || got[0].Unmountable() == "" {
		t.Errorf("found %+v", got)
	}
}

// A symlink outside every root is followed, as a TMPDIR under /var on macOS is.
func TestFindTreeFollowsASymlinkOutside(t *testing.T) {
	dir, roots := treeRepo(t)
	writeFile(t, filepath.Join(dir, ".githooks", "x"), "")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "config", "core.hooksPath", filepath.Join(link, ".githooks"))
	got := find(t, dir, roots)
	if len(got) != 1 || got[0].Host != filepath.Join(dir, ".githooks") || got[0].Unmountable() != "" {
		t.Errorf("found %+v", got)
	}
}

// An include in the checkout is a route host git reads; every condition counts.
func TestFindTreeFindsIncludes(t *testing.T) {
	dir, roots := treeRepo(t)
	writeFile(t, filepath.Join(dir, "a.inc"), "")
	writeFile(t, filepath.Join(dir, "b.inc"), "")
	git(t, dir, "config", "include.path", "../a.inc")
	git(t, dir, "config", "includeIf.onbranch:never.path", filepath.Join(dir, "b.inc"))

	var hosts []string
	for _, p := range find(t, dir, roots) {
		hosts = append(hosts, p.Host)
		if !strings.Contains(p.Why, ".path in ") {
			t.Errorf("why = %q", p.Why)
		}
	}
	slices.Sort(hosts)
	want := []string{filepath.Join(dir, "a.inc"), filepath.Join(dir, "b.inc")}
	if !slices.Equal(hosts, want) {
		t.Errorf("found %v, want %v", hosts, want)
	}
}

// A symlinked hook's target is found wherever the link sits.
func TestFindTreeFollowsHookSymlinksIntoTheCheckout(t *testing.T) {
	dir, roots := treeRepo(t)
	writeFile(t, filepath.Join(dir, "scripts", "pre-commit"), "#!/bin/sh\n")
	if err := os.Symlink(filepath.Join(dir, "scripts", "pre-commit"), filepath.Join(dir, ".git", "hooks", "pre-commit")); err != nil {
		t.Fatal(err)
	}
	got := find(t, dir, roots)
	if len(got) != 1 || got[0].Host != filepath.Join(dir, "scripts", "pre-commit") || got[0].Why != "hook pre-commit" {
		t.Errorf("found %+v", got)
	}
}

// Two settings naming one path are one mount with both reasons.
func TestFindTreeMergesReasons(t *testing.T) {
	dir, roots := treeRepo(t)
	writeFile(t, filepath.Join(dir, ".githooks", "pre-commit"), "#!/bin/sh\n")
	git(t, dir, "config", "core.hooksPath", ".githooks")
	if err := os.Symlink(filepath.Join(dir, ".githooks", "pre-commit"), filepath.Join(dir, ".githooks", "pre-push")); err != nil {
		t.Fatal(err)
	}
	got := find(t, dir, roots)
	if len(got) != 2 {
		t.Fatalf("found %+v", got)
	}
}

// No git on the host means nothing to guard.
func TestFindTreeWithoutGit(t *testing.T) {
	dir, roots := treeRepo(t)
	old := gitBin
	gitBin = filepath.Join(t.TempDir(), "no-git")
	t.Cleanup(func() { gitBin = old })
	got, err := FindTree(t.Context(), dir, roots, nil)
	if err != nil || len(got) != 0 {
		t.Errorf("got %+v, %v", got, err)
	}
}

// Each directory between the root and the target is pinned, parents first.
func TestTreeBindsPinEveryParent(t *testing.T) {
	root := Root{Host: "/h/r", Container: "/workspaces/r"}
	got := TreeBinds([]TreePath{{
		Why: "core.hooksPath", Host: "/h/r/tools/git/hooks", Container: "/workspaces/r/tools/git/hooks", Root: root,
	}}, nil)
	want := []Bind{
		{Host: "/h/r/tools", Container: "/workspaces/r/tools", Why: "core.hooksPath"},
		{Host: "/h/r/tools/git", Container: "/workspaces/r/tools/git", Why: "core.hooksPath"},
		{Host: "/h/r/tools/git/hooks", Container: "/workspaces/r/tools/git/hooks", ReadOnly: true, Why: "core.hooksPath"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// A parent already mounted is not mounted twice: docker refuses a duplicate
// destination.
func TestTreeBindsSkipExistingMounts(t *testing.T) {
	root := Root{Host: "/h/r", Container: "/workspaces/r"}
	got := TreeBinds([]TreePath{{
		Why: "include.path", Host: "/h/r/.git/extra.inc", Container: "/workspaces/r/.git/extra.inc", Root: root,
	}}, []string{"source=/h/r/.git,target=/workspaces/r/.git,type=bind"})
	if len(got) != 1 || got[0].Container != "/workspaces/r/.git/extra.inc" || !got[0].ReadOnly {
		t.Errorf("got %+v", got)
	}
}

// Unmountable paths are left for the caller to refuse.
func TestTreeBindsSkipUnmountable(t *testing.T) {
	root := Root{Host: "/h/r", Container: "/workspaces/r"}
	got := TreeBinds([]TreePath{
		{Host: "/h/r/.husky", Container: "/workspaces/r/.husky", Root: root, Missing: true},
		{Host: "/h/r", Container: "/workspaces/r", Root: root},
	}, nil)
	if len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}

// The fallback notices a hook edited, added, or created.
func TestSnapshotPaths(t *testing.T) {
	dir := t.TempDir()
	hooks := filepath.Join(dir, ".githooks")
	missing := filepath.Join(dir, ".husky")
	writeFile(t, filepath.Join(hooks, "pre-commit"), "a")
	before, err := SnapshotPaths([]string{hooks, missing})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(hooks, "pre-commit"), "b")
	writeFile(t, filepath.Join(missing, "pre-push"), "c")
	after, err := SnapshotPaths([]string{hooks, missing})
	if err != nil {
		t.Fatal(err)
	}
	got := Changed(before, after)
	want := []string{filepath.Join(hooks, "pre-commit"), filepath.Join(missing, "pre-push")}
	if !slices.Equal(got, want) {
		t.Errorf("changed %v, want %v", got, want)
	}
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
