package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider/k8s"
)

// With no --mode, a local container is guarded: the folder live, .git
// read-only.
func TestCreateDefaultsToGuardedOnLocal(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	if err := runContainerCreate(t.Context(), a, "", "api", projectWithConfig(t), createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := containerRow(t, a, "api").Mode; got != model.ModeGuarded {
		t.Errorf("mode = %q, want guarded", got)
	}
}

// A folderless container has no checkout for a mode to describe.
func TestCreateFolderlessHasNoMode(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	if err := runContainerCreate(t.Context(), a, "", "scratch", "", createOpts{noStart: true, noFolder: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := containerRow(t, a, "scratch").Mode; got != model.ModeNone {
		t.Errorf("mode = %q, want none", got)
	}
}

func TestCreateRefusesBadModes(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	for label, opts := range map[string]createOpts{
		"--mode with --no-folder":                 {noStart: true, noFolder: true, mode: model.ModeGuarded},
		"an unknown mode":                         {noStart: true, mode: "shared"},
		"legacy, which only the migration writes": {noStart: true, mode: model.ModeLegacy},
	} {
		t.Run(label, func(t *testing.T) {
			folder := projectWithConfig(t)
			if opts.noFolder {
				folder = ""
			}
			err := runContainerCreate(t.Context(), a, "", "api", folder, opts)
			if got := exitCodeOf(err); got != exitUsage {
				t.Errorf("exit code = %d (%v), want %d", got, err, exitUsage)
			}
		})
	}
}

// A pod has no host bind mounts, so a live folder is impossible on k8s:
// guarded is refused, and the default there is isolated — which is what the
// k8s provider has always done.
func TestK8sDefaultsToIsolatedAndRefusesGuarded(t *testing.T) {
	a, _ := newTestApp(t)
	if err := runProviderConfigure(a, "kp", "k8s", k8s.Config{Context: "c", Registry: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := runWorkspaceInit(a, "ws", "kp", false); err != nil {
		t.Fatal(err)
	}

	if err := runContainerCreate(t.Context(), a, "", "api", projectWithConfig(t), createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := containerRow(t, a, "api").Mode; got != model.ModeIsolated {
		t.Errorf("mode = %q, want isolated on k8s", got)
	}

	err := runContainerCreate(t.Context(), a, "", "web", projectWithConfig(t),
		createOpts{noStart: true, mode: model.ModeGuarded})
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("--mode guarded on k8s: exit code = %d (%v), want %d", got, err, exitUsage)
	}
}

// A guarded container gets .git as one read-only directory mount, and none of
// the single-file mounts: a host rename detaches a file mount, and on macOS a
// case variant goes around one, while a directory mount survives both.
func TestGuardedMountsGitReadOnlyAsADirectory(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, captured := stubCapturingDevcontainer(t)
	root := gitRepoWithConfig(t, `{"image":"ubuntu"}`)

	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	gitdir := "/workspaces/" + filepath.Base(root) + "/.git"
	mounts := mountsUsedForUp(t, calls, captured)
	if !hasMount(mounts, gitdir, true) {
		t.Errorf("no read-only .git mount at %s:\n  %s", gitdir, strings.Join(mounts, "\n  "))
	}
	for _, inside := range []string{"/config", "/hooks"} {
		if hasMountAt(mounts, gitdir+inside) {
			t.Errorf("a mount inside .git at %s%s; guarded mounts .git whole:\n  %s",
				gitdir, inside, strings.Join(mounts, "\n  "))
		}
	}
	// And no hooks copy: with .git read-only there is nothing to copy for.
	hooks, _ := hooksCopyDir("ws", "api")
	if entries, _ := os.ReadDir(hooks); len(entries) > 0 {
		t.Errorf("a guarded container got a hooks copy at %s", hooks)
	}
}

// The generated route gets the same mount, added per invocation.
func TestGuardedGeneratedConfigMountsGitReadOnly(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, captured := stubCapturingDevcontainer(t)
	requireGit(t)
	root, _ := filepath.EvalSymlinks(initRepo(t))

	if err := runContainerCreate(t.Context(), a, "", "api", root,
		createOpts{generate: true, tools: []string{"yq"}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	gitdir := "/workspaces/" + filepath.Base(root) + "/.git"
	if !hasMount(mountsUsedForUp(t, calls, captured), gitdir, true) {
		t.Error("generated guarded container has no read-only .git")
	}
}

// A guarded container on a worktree: the repository's common directory is
// read-only at its own path, and the checkout is mounted at its own.
func TestGuardedWorktreeMountsTheRepositoryReadOnly(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, captured := stubCapturingDevcontainer(t)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "feat", path)

	if err := runContainerCreate(t.Context(), a, "", "feat", path,
		createOpts{generate: true, tools: []string{"yq"}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	c := containerRow(t, a, "feat")
	mounts := mountsUsedForUp(t, calls, captured)
	if !hasMount(mounts, c.WorktreeRepo, true) {
		t.Errorf("the repository %s is not mounted read-only:\n  %s", c.WorktreeRepo, strings.Join(mounts, "\n  "))
	}
	if hasMountAt(mounts, filepath.Join(c.WorktreeRepo, "config")) {
		t.Errorf("a single-file mount on the repository's config:\n  %s", strings.Join(mounts, "\n  "))
	}
}

// A project that names its own workspace mount puts .git where dev cannot
// aim a mount. Guarded is a promise of prevention, so it is refused rather
// than fingerprinted.
func TestGuardedRefusesAProjectThatMovesTheWorkspace(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	root := gitRepoWithConfig(t, `{"image":"ubuntu","workspaceFolder":"/src"}`)

	err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{})
	if got := exitCodeOf(err); got != exitUsage {
		t.Fatalf("exit code = %d (%v), want %d", got, err, exitUsage)
	}
	if !strings.Contains(err.Error(), "workspaceFolder") || !strings.Contains(err.Error(), "isolated") {
		t.Errorf("error %q does not name the field and the way out", err)
	}
}

// hasMountAt reports whether any mount targets exactly target, read-only or
// not.
func hasMountAt(mounts []string, target string) bool {
	return hasMount(mounts, target, true) || hasMount(mounts, target, false)
}

// Refused at create even when not starting, so no row is left that every
// later start would refuse.
func TestGuardedRefusesAMovedWorkspaceWithNoStart(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	root := gitRepoWithConfig(t, `{"image":"ubuntu","workspaceMount":"source=x,target=/src,type=bind"}`)

	err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{noStart: true})
	if got := exitCodeOf(err); got != exitUsage {
		t.Fatalf("exit code = %d (%v), want %d", got, err, exitUsage)
	}
	st, _ := a.store()
	if _, err := st.GetContainer("ws", "api"); err == nil {
		t.Error("a refused create left a row")
	}
}

// Guarded cannot protect the .git files outside .git that point at a
// repository, so create says so where they exist.
func TestGuardedWarnsAboutSubmodulesAndWorktrees(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	withSubmodules := initRepo(t)
	writeFile(t, filepath.Join(withSubmodules, ".gitmodules"), "[submodule \"lib\"]\n\tpath = lib\n")
	warnings := captureStderr(t, func() {
		if err := runContainerCreate(t.Context(), a, "", "api", withSubmodules,
			createOpts{generate: true, tools: []string{"yq"}, noStart: true}); err != nil {
			t.Fatalf("create: %v", err)
		}
	})
	if !strings.Contains(warnings, "submodule") || !strings.Contains(warnings, "--mode isolated") {
		t.Errorf("no submodule warning naming isolated mode: %q", warnings)
	}

	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "feat", path)
	warnings = captureStderr(t, func() {
		if err := runContainerCreate(t.Context(), a, "", "feat", path,
			createOpts{generate: true, tools: []string{"yq"}, noStart: true}); err != nil {
			t.Fatalf("create: %v", err)
		}
	})
	if !strings.Contains(warnings, "worktree") || !strings.Contains(warnings, ".git file") {
		t.Errorf("no worktree warning naming the .git file: %q", warnings)
	}
}

// A plain repository gets neither warning.
func TestGuardedWarnsAboutNothingForAPlainRepository(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	warnings := captureStderr(t, func() {
		if err := runContainerCreate(t.Context(), a, "", "api", initRepo(t),
			createOpts{generate: true, tools: []string{"yq"}, noStart: true}); err != nil {
			t.Fatalf("create: %v", err)
		}
	})
	if strings.Contains(warnings, "submodule") || strings.Contains(warnings, ".git file") {
		t.Errorf("a plain repository was warned about: %q", warnings)
	}
}

// A file held read-only in the working tree — what an include.path names — is
// a single-file mount, and a host rename detaches it while inspect still
// reports it read-only. So before each command the file is probed from inside
// the container, by opening it for append and writing nothing; one that opens
// is refused, naming a restart.
func TestGuardedRefusesAHeldFileThatOpensForWrite(t *testing.T) {
	isolateGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, _ := stubCapturingDevcontainer(t)
	root := gitRepoWithConfig(t, `{"image":"ubuntu"}`)
	writeFile(t, filepath.Join(root, "shared.gitconfig"), "[user]\n\tname = x\n")
	gitIn(t, root, "config", "include.path", "../shared.gitconfig")
	// No container yet, so start creates one rather than checking its mounts.
	stubEngineWithMounts(t, "")
	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	held := "/workspaces/" + filepath.Base(root) + "/shared.gitconfig"
	stubEngineWithMounts(t, `[{"Destination":"/workspaces/`+filepath.Base(root)+`","RW":true},`+
		`{"Destination":"`+held+`","RW":false}]`)

	// The probe's exec answers "opens": the stub devcontainer exits 0 for
	// everything, which is what a write that succeeds looks like.
	err := runContainerExec(t.Context(), a, "", "api", []string{"true"}, false, nil)
	if err == nil || !strings.Contains(err.Error(), held) || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("exec = %v, want a refusal naming %s and a restart", err, held)
	}
	if !strings.Contains(callsIn(t, calls), ": >>") {
		t.Errorf("the held file was not probed from inside the container:\n%s", callsIn(t, calls))
	}
}

// And a file that refuses the write lets the command run.
func TestGuardedRunsWhenAHeldFileStaysReadOnly(t *testing.T) {
	isolateGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	root := gitRepoWithConfig(t, `{"image":"ubuntu"}`)
	writeFile(t, filepath.Join(root, "shared.gitconfig"), "[user]\n\tname = x\n")
	gitIn(t, root, "config", "include.path", "../shared.gitconfig")
	stubEngineWithMounts(t, "")
	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	held := "/workspaces/" + filepath.Base(root) + "/shared.gitconfig"
	stubEngineWithMounts(t, `[{"Destination":"/workspaces/`+filepath.Base(root)+`","RW":true},`+
		`{"Destination":"`+held+`","RW":false}]`)
	// The probe's write fails, as it does on a read-only mount; the command
	// itself succeeds.
	stubProbeRefused(t)

	if err := runContainerExec(t.Context(), a, "", "api", []string{"true"}, false, nil); err != nil {
		t.Fatalf("exec with the file still read-only: %v", err)
	}
}

// stubProbeRefused is a devcontainer whose exec fails for the held-file probe
// and succeeds for anything else.
func stubProbeRefused(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "devcontainer"), "#!/bin/sh\n"+
		"case \"$*\" in\n"+
		"  *': >>'*) exit 1 ;;\n"+
		"  read-configuration*) echo '{\"mergedConfiguration\":{}}' ;;\n"+
		"esac\nexit 0\n")
	if err := os.Chmod(filepath.Join(dir, "devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// An isolated container's workspace is a volume, never the folder: a generated
// document mounts the volume and names the folder only as where to seed from.
func TestIsolatedGeneratedConfigMountsAVolume(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	folder := t.TempDir()

	if err := runContainerCreate(t.Context(), a, "", "api", folder,
		createOpts{generate: true, tools: []string{"yq"}, noStart: true, mode: model.ModeIsolated}); err != nil {
		t.Fatalf("create: %v", err)
	}
	c := containerRow(t, a, "api")
	if !strings.Contains(c.GeneratedConfig, "source=dev-ws-api,target=/workspaces/api,type=volume") {
		t.Errorf("the generated document does not mount the workspace volume:\n%s", c.GeneratedConfig)
	}
}

// A project-owned isolated container gets the volume merged into its own
// document, replacing whatever workspace mount the project named.
func TestIsolatedProjectConfigGetsTheVolume(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, captured := stubCapturingDevcontainer(t)
	project := t.TempDir()
	writeProjectConfigIn(t, project, `{"image":"ubuntu","workspaceMount":"source=${localWorkspaceFolder},target=/src,type=bind","workspaceFolder":"/src"}`)
	stubEngineWithMounts(t, "")

	if err := runContainerCreate(t.Context(), a, "", "api", project, createOpts{mode: model.ModeIsolated}); err != nil {
		t.Fatalf("create: %v", err)
	}
	doc := documentUsedForUp(t, calls, captured)
	if !strings.Contains(doc, "source=dev-ws-api,target=/workspaces/api,type=volume") {
		t.Errorf("the merged document does not mount the volume:\n%s", doc)
	}
	if strings.Contains(doc, "localWorkspaceFolder") {
		t.Errorf("the project's bind of the folder survived the merge:\n%s", doc)
	}
}

// documentUsedForUp returns the merged document the last up received.
func documentUsedForUp(t *testing.T, calls, captured string) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(callsIn(t, calls)), "\n")
	upLine := 0
	for i, l := range lines {
		if strings.HasPrefix(l, "up ") {
			upLine = i + 1
		}
	}
	files, _ := filepath.Glob(filepath.Join(captured, itoa(upLine)+"-*"))
	var best string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) > len(best) {
			best = string(b)
		}
	}
	return best
}

// The list says each container's mode, and legacy for one from before modes.
func TestListShowsTheMode(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)
	st, _ := a.store()
	for _, c := range []model.Container{
		{Name: "g", WorkspaceName: "ws", SourceKind: model.SourceFolder, Source: "/src/g", Mode: model.ModeGuarded},
		{Name: "i", WorkspaceName: "ws", SourceKind: model.SourceFolder, Source: "/src/i", Mode: model.ModeIsolated},
		{Name: "l", WorkspaceName: "ws", SourceKind: model.SourceFolder, Source: "/src/l", Mode: model.ModeLegacy},
		{Name: "n", WorkspaceName: "ws", SourceKind: model.SourceNone, Mode: model.ModeNone},
	} {
		if err := st.CreateContainer(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := runContainerList(t.Context(), a, "", false, time.Second); err != nil {
		t.Fatalf("list: %v", err)
	}
	lines := strings.Split(out.String(), "\n")
	headerLine := ""
	for _, l := range lines {
		if strings.HasPrefix(l, "WORKSPACE") {
			headerLine = l
		}
	}
	if !strings.Contains(headerLine, "MODE") || strings.Contains(headerLine, " GIT") {
		t.Errorf("header = %q, want MODE in place of GIT", headerLine)
	}
	for name, want := range map[string]string{"g": "guarded", "i": "isolated", "l": "legacy", "n": "-"} {
		found := false
		for _, l := range lines[1:] {
			f := strings.Fields(l)
			if len(f) > 1 && f[1] == name {
				found = true
				if f[len(f)-1] != want {
					t.Errorf("%s: mode column = %q, want %q (%q)", name, f[len(f)-1], want, l)
				}
			}
		}
		if !found {
			t.Errorf("%s is not listed:\n%s", name, out)
		}
	}
}

// A legacy container that was guarded is told, once per command, that its
// guard can lapse and how to replace it.
func TestLegacyGuardedContainerIsWarned(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	stubEngineDocker(t)
	root := gitRepoWithConfig(t, `{"image":"ubuntu"}`)
	if err := runContainerCreate(t.Context(), a, "", "old", root, createOpts{legacy: true, noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	warnings := captureStderr(t, func() {
		_ = runContainerExec(t.Context(), a, "", "old", []string{"true"}, false, nil)
	})
	if !strings.Contains(warnings, "predates container modes") || !strings.Contains(warnings, "--mode") {
		t.Errorf("no legacy warning: %q", warnings)
	}
}
