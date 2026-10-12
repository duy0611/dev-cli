package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/audit"
)

// stubCapturingDevcontainer keeps a copy of each --config and --override-config
// it is given.
func stubCapturingDevcontainer(t *testing.T) (calls, captured string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	captured = filepath.Join(dir, "captured")
	if err := os.MkdirAll(captured, 0o755); err != nil {
		t.Fatal(err)
	}
	// BSD wc pads its count with spaces, so the count is tr-stripped to match
	// the file name.
	script := `#!/bin/sh
printf '%s\n' "$*" >> '` + calls + `'
n=$(wc -l < '` + calls + `' | tr -d ' ')
prev=""
for a in "$@"; do
  case "$prev" in --override-config|--config) cp "$a" '` + captured + `'/"$n-$(basename "$(dirname "$a")")-$(basename "$a")" 2>/dev/null ;; esac
  prev="$a"
done
case "$1" in read-configuration) echo '{"mergedConfiguration":{}}';; esac
exit 0
`
	writeFile(t, filepath.Join(dir, "devcontainer"), script)
	if err := os.Chmod(filepath.Join(dir, "devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls, captured
}

// mountsUsedForUp returns the mounts in the document the last up received.
func mountsUsedForUp(t *testing.T, calls, captured string) []string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(callsIn(t, calls)), "\n")
	upLine := 0
	for i, l := range lines {
		if strings.HasPrefix(l, "up ") {
			upLine = i + 1
		}
	}
	if upLine == 0 {
		t.Fatalf("up never ran:\n%s", callsIn(t, calls))
	}
	files, _ := filepath.Glob(filepath.Join(captured, itoa(upLine)+"-*"))
	var doc struct {
		Mounts []string `json:"mounts"`
	}
	slices.Sort(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var d struct {
			Mounts []string `json:"mounts"`
		}
		if json.Unmarshal(b, &d) == nil && len(d.Mounts) > len(doc.Mounts) {
			doc = d
		}
	}
	return doc.Mounts
}

func gitRepoWithConfig(t *testing.T, devcontainer string) string {
	t.Helper()
	requireGit(t)
	root := initRepo(t)
	root, _ = filepath.EvalSymlinks(root)
	writeFile(t, filepath.Join(root, ".devcontainer", "devcontainer.json"), devcontainer)
	return root
}

func hasMount(mounts []string, target string, readonly bool) bool {
	for _, m := range mounts {
		if strings.Contains(m, ",target="+target+",") || strings.HasSuffix(m, ",target="+target) {
			return strings.Contains(m, "readonly") == readonly
		}
	}
	return false
}

func TestCreateOnAGitRepositoryMountsTheGuard(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, captured := stubCapturingDevcontainer(t)
	root := gitRepoWithConfig(t, `{"image":"ubuntu"}`)
	writeFile(t, filepath.Join(root, ".git", "hooks", "pre-commit"), "#!/bin/sh\nexit 0\n")

	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{legacy: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if !containerRow(t, a, "api").GitGuard {
		t.Fatal("GitGuard not set for a repository")
	}
	gitdir := "/workspaces/" + filepath.Base(root) + "/.git"
	mounts := mountsUsedForUp(t, calls, captured)
	if !hasMount(mounts, gitdir, false) || !hasMount(mounts, gitdir+"/config", true) || !hasMount(mounts, gitdir+"/hooks", false) {
		t.Errorf("guard mounts missing at %s:\n  %s", gitdir, strings.Join(mounts, "\n  "))
	}

	hooks, _ := hooksCopyDir("ws", "api")
	if _, err := os.Stat(filepath.Join(hooks, "pre-commit")); err != nil {
		t.Errorf("hooks copy not seeded: %v", err)
	}
}

func TestGeneratedConfigGetsTheGuardWithoutStoringIt(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, captured := stubCapturingDevcontainer(t)
	requireGit(t)
	root, _ := filepath.EvalSymlinks(initRepo(t))

	if err := runContainerCreate(t.Context(), a, "", "api", root,
		createOpts{generate: true, tools: []string{"yq"}, legacy: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	gitdir := "/workspaces/" + filepath.Base(root) + "/.git"
	if !hasMount(mountsUsedForUp(t, calls, captured), gitdir+"/config", true) {
		t.Error("generated container not guarded")
	}
	if strings.Contains(containerRow(t, a, "api").GeneratedConfig, "/.git") {
		t.Error("the guard's mounts were stored in the generated document")
	}
}

func TestFolderThatIsNotARepositoryIsNotGuarded(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	if err := runContainerCreate(t.Context(), a, "", "api", projectWithConfig(t), createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if containerRow(t, a, "api").GitGuard {
		t.Error("a plain folder was marked guarded")
	}
}

func TestCustomWorkspaceMountFallsBackToFingerprinting(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, captured := stubCapturingDevcontainer(t)
	root := gitRepoWithConfig(t, `{"image":"ubuntu","workspaceFolder":"/src"}`)
	stubEngineDocker(t)

	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{legacy: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, m := range mountsUsedForUp(t, calls, captured) {
		if strings.Contains(m, ".git") {
			t.Errorf("a mount was aimed at a layout dev cannot know: %s", m)
		}
	}

	err := runContainerExec(t.Context(), a, "", "api",
		[]string{"sh", "-c", "true"}, false, nil)
	if err != nil {
		t.Fatalf("a clean exec was reported: %v", err)
	}
	stubTamperingExec(t, filepath.Join(root, ".git", "config"))
	err = runContainerExec(t.Context(), a, "", "api", []string{"tamper"}, false, nil)
	if err == nil || !strings.Contains(err.Error(), "config") {
		t.Fatalf("tampering not reported: %v", err)
	}
	refused := auditEntries(t, audit.Filter{Events: []string{"refused"}})
	if len(refused) != 1 || refused[0].Fields["reason"] != "git" {
		t.Errorf("refused records = %+v", refused)
	}
}

func TestPlantedCommondirRefusesTheNextCommand(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	stubEngineDocker(t)
	root := gitRepoWithConfig(t, `{"image":"ubuntu"}`)
	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{legacy: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	writeFile(t, filepath.Join(root, ".git", "commondir"), "/elsewhere\n")

	err := runContainerExec(t.Context(), a, "", "api", []string{"true"}, false, nil)
	if err == nil || !strings.Contains(err.Error(), "commondir") {
		t.Fatalf("exec ran with a planted commondir: %v", err)
	}
}

func TestRebuildReseedsTheHooksCopy(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	root := gitRepoWithConfig(t, `{"image":"ubuntu"}`)
	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{legacy: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	hooks, _ := hooksCopyDir("ws", "api")
	writeFile(t, filepath.Join(hooks, "post-checkout"), "agent wrote this")

	if err := runIn(t, a, "container", "rebuild", "api"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooks, "post-checkout")); !os.IsNotExist(err) {
		t.Error("what the container installed survived a rebuild")
	}
}

func TestRemoveDeletesTheHooksCopy(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	stubEngineDocker(t)
	root := gitRepoWithConfig(t, `{"image":"ubuntu"}`)
	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{legacy: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	hooks, _ := hooksCopyDir("ws", "api")
	if err := runIn(t, a, "container", "remove", "api"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(hooks); !os.IsNotExist(err) {
		t.Errorf("hooks copy left behind: %v", err)
	}
}

func TestWorktreeContainerMountsTheGuard(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, captured := stubCapturingDevcontainer(t)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")

	if err := runWorktreeCreate(t.Context(), a, "", "feat", checkoutOpts(repo, path)); err != nil {
		t.Fatalf("worktree create: %v", err)
	}
	if err := runContainerCreate(t.Context(), a, "", "feat", path,
		createOpts{generate: true, tools: []string{"yq"}, legacy: true}); err != nil {
		t.Fatalf("container create: %v", err)
	}
	st, _ := a.store()
	w, err := st.GetWorktree("ws", "feat")
	if err != nil {
		t.Fatal(err)
	}
	mounts := mountsUsedForUp(t, calls, captured)
	for target, ro := range map[string]bool{
		filepath.Join(w.Repo, "config"): true,
		filepath.Join(w.Repo, "hooks"):  false,
		filepath.Join(w.Path, ".git"):   true,
	} {
		if !hasMount(mounts, target, ro) {
			t.Errorf("no mount at %s (readonly %v):\n  %s", target, ro, strings.Join(mounts, "\n  "))
		}
	}
}

// stubEngineDocker reports one running container, so exec and remove pass their
// status checks.
func stubEngineDocker(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "docker"),
		"#!/bin/sh\ncase \"$*\" in\n  *State*) echo running ;;\n  ps*) echo abc123 ;;\nesac\nexit 0\n")
	if err := os.Chmod(filepath.Join(dir, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// stubTamperingExec makes the devcontainer exec append a setting to path, as an
// agent would.
func stubTamperingExec(t *testing.T, path string) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "devcontainer"), "#!/bin/sh\ncase \"$1\" in\n"+
		"  exec) printf '[core]\\n\\tfsmonitor = evil\\n' >> '"+path+"' ;;\n"+
		"  read-configuration) echo '{\"mergedConfiguration\":{}}' ;;\nesac\nexit 0\n")
	if err := os.Chmod(filepath.Join(dir, "devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// isolateGit keeps the operator's git config out, so the tree paths reported
// are only the test's.
func isolateGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// stubEngineWithMounts is stubEngineDocker reporting the given mounts, as
// docker inspect prints them.
func stubEngineWithMounts(t *testing.T, mounts string) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "mounts.json"), mounts)
	writeFile(t, filepath.Join(dir, "docker"),
		"#!/bin/sh\n[ -s '"+filepath.Join(dir, "mounts.json")+"' ] || exit 0\ncase \"$*\" in\n  *State*) echo running ;;\n  inspect*) cat '"+filepath.Join(dir, "mounts.json")+"' ;;\n  ps*) echo abc123 ;;\nesac\nexit 0\n")
	if err := os.Chmod(filepath.Join(dir, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestHooksPathInTheCheckoutIsMountedReadOnly(t *testing.T) {
	isolateGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, captured := stubCapturingDevcontainer(t)
	root := gitRepoWithConfig(t, `{"image":"ubuntu"}`)
	writeFile(t, filepath.Join(root, "tools", "hooks", "commit-msg"), "#!/bin/sh\n")
	gitIn(t, root, "config", "core.hooksPath", "tools/hooks")
	stubEngineWithMounts(t, "")

	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	ws := "/workspaces/" + filepath.Base(root)
	mounts := mountsUsedForUp(t, calls, captured)
	if !hasMount(mounts, ws+"/tools/hooks", true) || !hasMount(mounts, ws+"/tools", false) {
		t.Errorf("tree mounts missing:\n  %s", strings.Join(mounts, "\n  "))
	}
}

func TestMissingHooksPathInTheCheckoutIsRefused(t *testing.T) {
	isolateGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	root := gitRepoWithConfig(t, `{"image":"ubuntu"}`)
	gitIn(t, root, "config", "core.hooksPath", ".husky")

	err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{})
	if err == nil || !strings.Contains(err.Error(), ".husky") || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("create = %v", err)
	}
	refused := auditEntries(t, audit.Filter{Events: []string{"refused"}})
	if len(refused) != 1 || refused[0].Fields["reason"] != "git" {
		t.Errorf("refused records = %+v", refused)
	}
}

func TestContainerPredatingTheHooksPathIsRefused(t *testing.T) {
	isolateGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	root := gitRepoWithConfig(t, `{"image":"ubuntu"}`)
	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	ws := "/workspaces/" + filepath.Base(root)
	stubEngineWithMounts(t, `[{"Destination":"`+ws+`","RW":true}]`)
	if err := runContainerExec(t.Context(), a, "", "api", []string{"true"}, false, nil); err != nil {
		t.Fatalf("exec before the setting: %v", err)
	}

	writeFile(t, filepath.Join(root, ".githooks", "commit-msg"), "#!/bin/sh\n")
	gitIn(t, root, "config", "core.hooksPath", ".githooks")
	err := runContainerExec(t.Context(), a, "", "api", []string{"true"}, false, nil)
	if err == nil || !strings.Contains(err.Error(), "rebuild") || !strings.Contains(err.Error(), ws+"/.githooks") {
		t.Fatalf("exec = %v", err)
	}

	stubEngineWithMounts(t, `[{"Destination":"`+ws+`","RW":true},{"Destination":"`+ws+`/.githooks","RW":false}]`)
	if err := runContainerExec(t.Context(), a, "", "api", []string{"true"}, false, nil); err != nil {
		t.Fatalf("exec with the mount: %v", err)
	}
}

func TestCustomWorkspaceMountFingerprintsTheTreeHooks(t *testing.T) {
	isolateGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	root := gitRepoWithConfig(t, `{"image":"ubuntu","workspaceFolder":"/src"}`)
	hook := filepath.Join(root, ".githooks", "commit-msg")
	writeFile(t, hook, "#!/bin/sh\n")
	gitIn(t, root, "config", "core.hooksPath", ".githooks")
	stubEngineDocker(t)
	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{legacy: true}); err != nil {
		t.Fatalf("create: %v", err)
	}

	stubTamperingExec(t, hook)
	err := runContainerExec(t.Context(), a, "", "api", []string{"tamper"}, false, nil)
	if err == nil || !strings.Contains(err.Error(), "commit-msg") {
		t.Fatalf("tampering not reported: %v", err)
	}
}
