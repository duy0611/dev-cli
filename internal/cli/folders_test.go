package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider/k8s"
	"github.com/duy0611/dev-cli/internal/provider/local"
)

// twoRepos makes workspace/something/repo-1 and repo-2, each a repository,
// resolved.
func twoRepos(t *testing.T) (string, string) {
	t.Helper()
	requireGit(t)
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	var out []string
	for _, name := range []string{"repo-1", "repo-2"} {
		dir := filepath.Join(parent, name)
		if err := os.Rename(initRepo(t), dir); err != nil {
			t.Fatal(err)
		}
		out = append(out, dir)
	}
	return out[0], out[1]
}

func TestCreateRecordsExtraFolders(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)
	repo1, repo2 := twoRepos(t)

	if err := runContainerCreate(t.Context(), a, "", "multi", repo1,
		createOpts{generate: true, tools: []string{"yq"}, noStart: true, extraFolders: []string{repo2}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	c := containerRow(t, a, "multi")
	want := []model.Folder{{HostPath: repo2, ContainerPath: "/workspaces/repo-2"}}
	if c.Source != repo1 || !slices.Equal(c.Folders, want) {
		t.Errorf("source=%q folders=%v, want %q and %v", c.Source, c.Folders, repo1, want)
	}
	if !strings.Contains(out.String(), "these repositories") || !strings.Contains(out.String(), "repo-2") {
		t.Errorf("create did not name both guarded repositories:\n%s", out)
	}
}

func TestCreateWithRepeatedFolderFlag(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo1, repo2 := twoRepos(t)
	if err := runIn(t, a, "container", "create", "multi", "--folder", repo1, "--folder", repo2,
		"--generate", "--tools", "yq", "--no-start"); err != nil {
		t.Fatalf("create: %v", err)
	}
	c := containerRow(t, a, "multi")
	if c.Source != repo1 || len(c.Folders) != 1 || c.Folders[0].HostPath != repo2 {
		t.Errorf("source=%q folders=%v", c.Source, c.Folders)
	}
}

func TestCreateRefusesBadFolderSets(t *testing.T) {
	requireGit(t)
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	mk := func(rel string) string {
		p := filepath.Join(parent, rel)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	repo := filepath.Join(parent, "repo")
	if err := os.Rename(initRepo(t), repo); err != nil {
		t.Fatal(err)
	}
	for label, tc := range map[string]struct {
		primary string
		extras  []string
		names   []string
	}{
		"a folder twice":                      {mk("a"), []string{mk("b"), mk("b")}, []string{"b"}},
		"the primary again":                   {mk("c"), []string{mk("c")}, []string{"c"}},
		"a folder inside the primary":         {mk("d"), []string{mk("d/e")}, []string{"d/e"}},
		"the primary inside a folder":         {mk("f/g"), []string{mk("f")}, []string{"f/g"}},
		"elsewhere in the primary repository": {mk("repo/svc"), []string{mk("repo/lib")}, []string{"repo/lib", "repo"}},
		"two folders with one name":           {mk("h"), []string{mk("x/api"), mk("y/api")}, []string{"x/api", "y/api"}},
		"a folder named like the primary":     {mk("p/same"), []string{mk("q/same")}, []string{"p/same", "q/same"}},
		"a missing folder":                    {mk("i"), []string{filepath.Join(parent, "nope")}, []string{"nope"}},
	} {
		t.Run(label, func(t *testing.T) {
			a, _ := newTestApp(t)
			seedWorkspace(t, a)
			err := runContainerCreate(t.Context(), a, "", "multi", tc.primary,
				createOpts{generate: true, tools: []string{"yq"}, noStart: true, extraFolders: tc.extras})
			if got := exitCodeOf(err); got != exitUsage {
				t.Fatalf("exit code = %d (%v), want %d", got, err, exitUsage)
			}
			for _, n := range tc.names {
				if !strings.Contains(err.Error(), n) {
					t.Errorf("error %q does not name %s", err, n)
				}
			}
			st, _ := a.store()
			if _, err := st.GetContainer("ws", "multi"); err == nil {
				t.Error("a refused create left a row")
			}
		})
	}
}

func TestCreateRefusesFoldersWithNoFolder(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	err := runContainerCreate(t.Context(), a, "", "x", "",
		createOpts{noFolder: true, noStart: true, extraFolders: []string{t.TempDir()}})
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d (%v), want %d", got, err, exitUsage)
	}
}

func TestK8sRefusesASecondFolder(t *testing.T) {
	a, _ := newTestApp(t)
	if err := runProviderConfigure(a, "kp", "k8s", k8s.Config{Context: "c", Registry: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := runWorkspaceInit(a, "ws", "kp", false); err != nil {
		t.Fatal(err)
	}
	err := runContainerCreate(t.Context(), a, "", "api", projectWithConfig(t),
		createOpts{noStart: true, extraFolders: []string{t.TempDir()}})
	if got := exitCodeOf(err); got != exitUsage {
		t.Fatalf("exit code = %d (%v), want %d", got, err, exitUsage)
	}
	if !strings.Contains(err.Error(), "experimental") {
		t.Errorf("error %q does not name the provider as experimental", err)
	}
}

// Each repository's .git is read-only, after the folder's own mount.
func TestGuardedExtraFoldersAreMountedAndGuarded(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, captured := stubCapturingDevcontainer(t)
	stubEngineWithMounts(t, "")
	repo1, repo2 := twoRepos(t)
	plain, _ := filepath.EvalSymlinks(t.TempDir())
	wtRepo := initRepo(t)
	wt := filepath.Join(filepath.Dir(repo1), "wt")
	gitIn(t, wtRepo, "worktree", "add", "-q", "-b", "feat", wt)

	if err := runContainerCreate(t.Context(), a, "", "multi", repo1, createOpts{generate: true,
		tools: []string{"yq"}, extraFolders: []string{repo2, plain, wt}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	mounts := mountsUsedForUp(t, calls, captured)
	at := func(target string) int {
		return slices.IndexFunc(mounts, func(m string) bool { return strings.Contains(m, ",target="+target+",") })
	}
	for target, readonly := range map[string]bool{
		"/workspaces/repo-1/.git":             true,
		"/workspaces/repo-2":                  false,
		"/workspaces/repo-2/.git":             true,
		"/workspaces/" + filepath.Base(plain): false,
		wt:                                    false,
	} {
		if !hasMount(mounts, target, readonly) {
			t.Errorf("no mount at %s (readonly %v):\n  %s", target, readonly, strings.Join(mounts, "\n  "))
		}
	}
	if at("/workspaces/repo-2/.git") < at("/workspaces/repo-2") {
		t.Errorf("repo-2's .git is mounted before repo-2:\n  %s", strings.Join(mounts, "\n  "))
	}
	c := containerRow(t, a, "multi")
	var wtFolder model.Folder
	for _, f := range c.Folders {
		if f.HostPath == wt {
			wtFolder = f
		}
	}
	if wtFolder.WorktreeRepo == "" || !hasMount(mounts, wtFolder.WorktreeRepo, true) {
		t.Errorf("the worktree's repository %q is not mounted read-only:\n  %s",
			wtFolder.WorktreeRepo, strings.Join(mounts, "\n  "))
	}
	if hasMountAt(mounts, "/workspaces/"+filepath.Base(plain)+"/.git") {
		t.Error("a plain folder got a .git mount")
	}

	log := callsIn(t, calls)
	for _, dir := range []string{"/workspaces/repo-1", "/workspaces/repo-2", wt, wtFolder.WorktreeRepo} {
		if !strings.Contains(log, "safe.directory' 'trust' '"+dir+"'") && !strings.Contains(log, "trust "+dir+"\n") {
			t.Errorf("%s was not marked safe inside the container:\n%s", dir, log)
		}
	}
}

// A project's own config gets every folder's mounts, after the workspace.
func TestGuardedProjectConfigGetsTheFolderMounts(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, captured := stubCapturingDevcontainer(t)
	stubEngineWithMounts(t, "")
	repo1 := gitRepoWithConfig(t, `{"image":"ubuntu","mounts":["source=cache,target=/cache,type=volume"]}`)
	_, repo2 := twoRepos(t)

	if err := runContainerCreate(t.Context(), a, "", "multi", repo1,
		createOpts{extraFolders: []string{repo2}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	mounts := mountsUsedForUp(t, calls, captured)
	if len(mounts) == 0 || !strings.Contains(mounts[0], "/cache") {
		t.Errorf("the project's own mount is not first:\n  %s", strings.Join(mounts, "\n  "))
	}
	if !hasMount(mounts, "/workspaces/repo-2", false) || !hasMount(mounts, "/workspaces/repo-2/.git", true) {
		t.Errorf("repo-2 is not mounted and guarded:\n  %s", strings.Join(mounts, "\n  "))
	}
}

// repo-1's hooksPath can name a directory in repo-2.
func TestTreeSearchCrossesFolders(t *testing.T) {
	isolateGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, captured := stubCapturingDevcontainer(t)
	stubEngineWithMounts(t, "")
	repo1, repo2 := twoRepos(t)
	writeFile(t, filepath.Join(repo2, "hooks", "pre-commit"), "#!/bin/sh\n")
	gitIn(t, repo1, "config", "core.hooksPath", filepath.Join(repo2, "hooks"))

	if err := runContainerCreate(t.Context(), a, "", "multi", repo1,
		createOpts{generate: true, tools: []string{"yq"}, extraFolders: []string{repo2}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	mounts := mountsUsedForUp(t, calls, captured)
	if !hasMount(mounts, "/workspaces/repo-2/hooks", true) {
		t.Errorf("repo-1's hooks in repo-2 are not held read-only:\n  %s", strings.Join(mounts, "\n  "))
	}
}

// A non-repository primary still guards its repository extras.
func TestGuardedPlainPrimaryStillGuardsItsFolders(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, captured := stubCapturingDevcontainer(t)
	stubEngineWithMounts(t, "")
	_, repo2 := twoRepos(t)
	primary, _ := filepath.EvalSymlinks(t.TempDir())

	if err := runContainerCreate(t.Context(), a, "", "multi", primary,
		createOpts{generate: true, tools: []string{"yq"}, extraFolders: []string{repo2}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if !hasMount(mountsUsedForUp(t, calls, captured), "/workspaces/repo-2/.git", true) {
		t.Error("repo-2's .git is not read-only under a plain primary")
	}
}

func TestStartRefusesAGoneFolder(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	stubEngineWithMounts(t, "")
	repo1, repo2 := twoRepos(t)
	if err := runContainerCreate(t.Context(), a, "", "multi", repo1,
		createOpts{generate: true, tools: []string{"yq"}, noStart: true, extraFolders: []string{repo2}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.RemoveAll(repo2); err != nil {
		t.Fatal(err)
	}
	err := runIn(t, a, "container", "start", "multi")
	if got := exitCodeOf(err); got != exitError {
		t.Fatalf("exit code = %d (%v), want %d", got, err, exitError)
	}
	if !strings.Contains(err.Error(), "--remove-folder "+repo2) {
		t.Errorf("error %q does not say how to drop the folder", err)
	}
}

func TestListShowsExtraFolders(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)
	st, _ := a.store()
	if err := st.CreateContainer(model.Container{Name: "m", WorkspaceName: "ws", SourceKind: model.SourceFolder,
		Source: "/src/m", Mode: model.ModeGuarded, Folders: []model.Folder{
			{HostPath: "/src/a", ContainerPath: "/workspaces/a"},
			{HostPath: "/src/b", ContainerPath: "/workspaces/b"},
		}}); err != nil {
		t.Fatal(err)
	}
	if err := runContainerList(t.Context(), a, "", false, time.Second); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "/src/m +2") {
		t.Errorf("list does not show the primary and +2:\n%s", out)
	}
}

func TestRebuildAddsAndRemovesFolders(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, captured := stubCapturingDevcontainer(t)
	stubEngineWithMounts(t, "")
	repo1, repo2 := twoRepos(t)
	third := filepath.Join(filepath.Dir(repo1), "repo-3")
	if err := os.Rename(initRepo(t), third); err != nil {
		t.Fatal(err)
	}
	if err := runContainerCreate(t.Context(), a, "", "multi", repo1,
		createOpts{generate: true, tools: []string{"yq"}, extraFolders: []string{repo2}}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := runIn(t, a, "container", "rebuild", "multi", "--add-folder", third, "--remove-folder", repo2); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	mounts := mountsUsedForUp(t, calls, captured)
	if !hasMount(mounts, "/workspaces/repo-3/.git", true) {
		t.Errorf("the added folder's .git is not read-only in the rebuilt container:\n  %s", strings.Join(mounts, "\n  "))
	}
	if hasMountAt(mounts, "/workspaces/repo-2") {
		t.Errorf("the removed folder is still mounted:\n  %s", strings.Join(mounts, "\n  "))
	}
	c := containerRow(t, a, "multi")
	if len(c.Folders) != 1 || c.Folders[0].HostPath != third {
		t.Errorf("folders = %v, want only %s", c.Folders, third)
	}
}

// A folder gone from the host can still be removed by its stored path.
func TestRebuildRemovesAGoneFolder(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	stubEngineWithMounts(t, "")
	repo1, repo2 := twoRepos(t)
	if err := runContainerCreate(t.Context(), a, "", "multi", repo1,
		createOpts{generate: true, tools: []string{"yq"}, noStart: true, extraFolders: []string{repo2}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.RemoveAll(repo2); err != nil {
		t.Fatal(err)
	}
	if err := runIn(t, a, "container", "rebuild", "multi", "--remove-folder", repo2); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if c := containerRow(t, a, "multi"); len(c.Folders) != 0 {
		t.Errorf("folders = %v, want none", c.Folders)
	}
}

func TestRebuildRefusesBadFolderChanges(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	stubEngineWithMounts(t, "")
	repo1, repo2 := twoRepos(t)
	if err := runContainerCreate(t.Context(), a, "", "g", repo1,
		createOpts{generate: true, tools: []string{"yq"}, noStart: true, extraFolders: []string{repo2}}); err != nil {
		t.Fatal(err)
	}
	isolatedOn(t, a, "iso", t.TempDir())
	if err := runContainerCreate(t.Context(), a, "", "old", t.TempDir(),
		createOpts{generate: true, tools: []string{"yq"}, noStart: true, legacy: true}); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()

	for label, tc := range map[string]struct {
		argv []string
		want string
	}{
		"remove the primary":     {[]string{"g", "--remove-folder", repo1}, "primary"},
		"add one already there":  {[]string{"g", "--add-folder", repo2}, "already"},
		"remove one not there":   {[]string{"g", "--remove-folder", other}, "not one of"},
		"add to isolated":        {[]string{"iso", "--add-folder", other}, "isolated; its folders are fixed"},
		"remove from isolated":   {[]string{"iso", "--remove-folder", other}, "isolated; its folders are fixed"},
		"add to legacy":          {[]string{"old", "--add-folder", other}, "legacy"},
		"add inside the primary": {[]string{"g", "--add-folder", filepath.Join(repo1, ".git")}, "inside"},
	} {
		t.Run(label, func(t *testing.T) {
			err := runIn(t, a, append([]string{"container", "rebuild"}, tc.argv...)...)
			if got := exitCodeOf(err); got != exitUsage {
				t.Fatalf("exit code = %d (%v), want %d", got, err, exitUsage)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not say %q", err, tc.want)
			}
		})
	}
	if c := containerRow(t, a, "g"); len(c.Folders) != 1 || c.Folders[0].HostPath != repo2 {
		t.Errorf("a refused rebuild changed the folders: %v", c.Folders)
	}
}

func TestIsolatedExtraFoldersGetVolumes(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, captured := stubCapturingDevcontainer(t)
	stubEngineWithMounts(t, "")
	repo1, repo2 := twoRepos(t)

	if err := runContainerCreate(t.Context(), a, "", "iso", repo1, createOpts{generate: true,
		tools: []string{"yq"}, mode: model.ModeIsolated, extraFolders: []string{repo2}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	c := containerRow(t, a, "iso")
	if c.WorkspacePath != "/workspaces/repo-1" {
		t.Errorf("workspace_path = %q, want /workspaces/repo-1", c.WorkspacePath)
	}
	if !strings.Contains(c.GeneratedConfig, "source=dev-ws-iso,target=/workspaces/repo-1,type=volume") {
		t.Errorf("the primary's volume is not at /workspaces/repo-1:\n%s", c.GeneratedConfig)
	}
	vol := local.FolderVolumeName("ws", "iso", repo2)
	mounts := mountsUsedForUp(t, calls, captured)
	want := "source=" + vol + ",target=/workspaces/repo-2,type=volume"
	if !slices.Contains(mounts, want) {
		t.Errorf("no %s:\n  %s", want, strings.Join(mounts, "\n  "))
	}
	for _, m := range mounts {
		if strings.Contains(m, repo1) || strings.Contains(m, repo2) {
			t.Errorf("an isolated container mounts a host folder: %s", m)
		}
	}
}

// stubContainerFolders runs the container's commands on the host, with each
// container path mapped to a host directory.
func stubContainerFolders(t *testing.T, dirs map[string]string) {
	t.Helper()
	stubContainerRepo(t, "")
	inContainer = func(_ model.Container, dir string) string { return dirs[dir] }
}

func TestFetchAndRemoveLoopOverFolders(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)
	repo1, repo2 := twoRepos(t)
	if err := runContainerCreate(t.Context(), a, "", "iso", repo1, createOpts{generate: true,
		tools: []string{"yq"}, noStart: true, mode: model.ModeIsolated, extraFolders: []string{repo2}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	inside := map[string]string{}
	for path, host := range map[string]string{"/workspaces/repo-1": repo1, "/workspaces/repo-2": repo2} {
		dir := t.TempDir()
		gitIn(t, dir, "clone", "-q", host, ".")
		gitIn(t, dir, "switch", "-q", "-c", "work-"+filepath.Base(host))
		gitIn(t, dir, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "in "+filepath.Base(host))
		inside[path] = dir
	}
	stubContainerFolders(t, inside)

	err := runContainerRemove(t.Context(), a, "", "iso", false)
	if got := exitCodeOf(err); got != exitUsage {
		t.Fatalf("remove before fetch: exit code = %d (%v), want %d", got, err, exitUsage)
	}
	for _, r := range []string{"repo-1: branch work-repo-1", "repo-2: branch work-repo-2"} {
		if !strings.Contains(err.Error(), r) {
			t.Errorf("remove's refusal does not group %q by folder:\n%v", r, err)
		}
	}

	if err := runContainerFetch(t.Context(), a, "", "iso"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for _, host := range []string{repo1, repo2} {
		ref := "refs/remotes/iso/work-" + filepath.Base(host)
		if got := gitOutput(t, host, "log", "--format=%s", "-1", ref); got != "in "+filepath.Base(host) {
			t.Errorf("%s in %s = %q", ref, host, got)
		}
		if !strings.Contains(out.String(), "iso/work-"+filepath.Base(host)) {
			t.Errorf("fetch output does not name %s's refs:\n%s", host, out)
		}
	}
	if n := strings.Count(out.String(), "fetched into"); n != 2 {
		t.Errorf("fetch printed %d lines, want one per folder:\n%s", n, out)
	}

	// Fetched but not merged: still refused, naming the folder.
	err = runContainerRemove(t.Context(), a, "", "iso", false)
	if exitCodeOf(err) != exitUsage || !strings.Contains(err.Error(), "repo-2: branch work-repo-2 is fetched") {
		t.Fatalf("remove after fetch = %v, want a refusal naming repo-2", err)
	}
	for _, host := range []string{repo1, repo2} {
		gitIn(t, host, "branch", "kept", "refs/remotes/iso/work-"+filepath.Base(host))
	}
	if err := runContainerRemove(t.Context(), a, "", "iso", false); err != nil {
		t.Fatalf("remove after merge: %v", err)
	}
	for _, host := range []string{repo1, repo2} {
		if refs := gitOutput(t, host, "for-each-ref", "refs/remotes/iso/"); refs != "" {
			t.Errorf("remove left the fetched refs in %s:\n%s", host, refs)
		}
	}
}

func TestFetchSkipsAFolderThatIsNotARepository(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)
	requireGit(t)
	repo := initRepo(t)
	plain, _ := filepath.EvalSymlinks(t.TempDir())
	if err := runContainerCreate(t.Context(), a, "", "iso", repo, createOpts{generate: true,
		tools: []string{"yq"}, noStart: true, mode: model.ModeIsolated, extraFolders: []string{plain}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	inside := t.TempDir()
	gitIn(t, inside, "clone", "-q", repo, ".")
	resolved, _ := filepath.EvalSymlinks(repo)
	stubContainerFolders(t, map[string]string{"/workspaces/" + filepath.Base(resolved): inside})

	if err := runContainerFetch(t.Context(), a, "", "iso"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(out.String(), "skipping "+filepath.Base(plain)) && !strings.Contains(out.String(), "skipping") {
		t.Errorf("no line for the skipped folder:\n%s", out)
	}
}

// A failed rebuild leaves the record matching the container still running.
func TestFailedRebuildKeepsTheFolders(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubEngineWithMounts(t, "")
	repo1, repo2 := twoRepos(t)
	stubCapturingDevcontainer(t)
	if err := runContainerCreate(t.Context(), a, "", "multi", repo1,
		createOpts{generate: true, tools: []string{"yq"}, noStart: true, extraFolders: []string{repo2}}); err != nil {
		t.Fatal(err)
	}
	stubDevcontainer(t, "--remove-existing-container")
	if err := runIn(t, a, "container", "rebuild", "multi", "--remove-folder", repo2); err == nil {
		t.Fatal("rebuild succeeded against a failing engine")
	}
	if c := containerRow(t, a, "multi"); len(c.Folders) != 1 {
		t.Errorf("folders = %v after a failed rebuild, want repo-2 kept", c.Folders)
	}
}

func TestCreateNoIdleStop(t *testing.T) {
	for name, tc := range map[string]struct {
		flags []string
		want  bool
	}{"x": {nil, true}, "y": {[]string{"--no-idle-stop"}, false}} {
		a, _ := newTestApp(t)
		seedWorkspace(t, a)
		argv := append([]string{"container", "create", name, "--no-folder", "--generate",
			"--tools", "yq", "--no-start"}, tc.flags...)
		if err := runIn(t, a, argv...); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if got := containerRow(t, a, name).IdleStop; got != tc.want {
			t.Errorf("%s %v: IdleStop = %v, want %v", name, tc.flags, got, tc.want)
		}
	}
}
