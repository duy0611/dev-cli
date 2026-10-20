package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/audit"
	"github.com/duy0611/dev-cli/internal/model"
)

func TestCheckModeChange(t *testing.T) {
	allowed := map[[2]model.Mode]bool{
		{model.ModeGuarded, model.ModeUnprotected}:     true,
		{model.ModeUnprotected, model.ModeGuarded}:     true,
		{model.ModeGuarded, model.ModeIsolated}:        true,
		{model.ModeUnprotected, model.ModeIsolated}:    true,
		{model.ModeGuarded, model.ModeGuarded}:         true,
		{model.ModeUnprotected, model.ModeUnprotected}: true,
		{model.ModeIsolated, model.ModeIsolated}:       true,
	}
	froms := []model.Mode{model.ModeGuarded, model.ModeUnprotected, model.ModeIsolated, model.ModeLegacy, model.ModeNone}
	tos := []model.Mode{model.ModeGuarded, model.ModeUnprotected, model.ModeIsolated}
	for _, from := range froms {
		for _, to := range tos {
			err := checkModeChange("api", from, to, model.KindLocal)
			if allowed[[2]model.Mode{from, to}] {
				if err != nil {
					t.Errorf("local %s -> %s: %v, want nil", from, to, err)
				}
				continue
			}
			if got := exitCodeOf(err); got != exitUsage {
				t.Errorf("local %s -> %s: exit code %d (%v), want %d", from, to, got, err, exitUsage)
			}
		}
	}
	if err := checkModeChange("api", model.ModeIsolated, model.ModeIsolated, model.KindK8s); err != nil {
		t.Errorf("k8s isolated -> isolated: %v", err)
	}
	for _, to := range []model.Mode{model.ModeGuarded, model.ModeUnprotected} {
		err := checkModeChange("api", model.ModeIsolated, to, model.KindK8s)
		if got := exitCodeOf(err); got != exitUsage {
			t.Errorf("k8s -> %s: exit code %d (%v), want %d", to, got, err, exitUsage)
		}
	}
	if err := checkModeChange("api", model.ModeGuarded, "bogus", model.KindLocal); exitCodeOf(err) != exitUsage {
		t.Errorf("unknown mode: %v, want a usage error", err)
	}
}

// stubDockerWithoutVolumes answers every call but `volume inspect`, so a
// seeded rebuild finds no volume in its way.
func stubDockerWithoutVolumes(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "docker"),
		"#!/bin/sh\ncase \"$*\" in\n  'volume inspect'*) exit 1 ;;\n  *State*) echo running ;;\n  ps*) echo abc123 ;;\nesac\nexit 0\n")
	if err := os.Chmod(filepath.Join(dir, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func lastAuditRebuild(t *testing.T) audit.Entry {
	t.Helper()
	entries := auditEntries(t, audit.Filter{Events: []string{"rebuild"}})
	if len(entries) == 0 {
		t.Fatal("no rebuild record")
	}
	return entries[len(entries)-1]
}

func upCalls(t *testing.T, calls string) []string {
	t.Helper()
	var out []string
	for _, l := range strings.Split(callsIn(t, calls), "\n") {
		if strings.HasPrefix(l, "up ") {
			out = append(out, l)
		}
	}
	return out
}

func TestRebuildGuardedToUnprotected(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, captured := stubCapturingDevcontainer(t)
	root := gitRepoWithConfig(t, `{"image":"ubuntu"}`)
	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := runIn(t, a, "container", "rebuild", "api", "--mode", "unprotected"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	gitdir := "/workspaces/" + filepath.Base(root) + "/.git"
	if mounts := mountsUsedForUp(t, calls, captured); hasMountAt(mounts, gitdir) {
		t.Errorf("a mount at %s after the switch:\n  %s", gitdir, strings.Join(mounts, "\n  "))
	}
	if got := containerRow(t, a, "api").Mode; got != model.ModeUnprotected {
		t.Errorf("mode = %q, want unprotected", got)
	}
	e := lastAuditRebuild(t)
	if e.Fields["mode_from"] != "guarded" || e.Fields["mode_to"] != "unprotected" {
		t.Errorf("record fields = %v", e.Fields)
	}
}

func TestRebuildUnprotectedToGuarded(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, captured := stubCapturingDevcontainer(t)
	root := gitRepoWithConfig(t, `{"image":"ubuntu"}`)
	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{mode: model.ModeUnprotected}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := runIn(t, a, "container", "rebuild", "api", "--mode", "guarded"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	gitdir := "/workspaces/" + filepath.Base(root) + "/.git"
	if mounts := mountsUsedForUp(t, calls, captured); !hasMount(mounts, gitdir, true) {
		t.Errorf("no read-only mount at %s after the switch:\n  %s", gitdir, strings.Join(mounts, "\n  "))
	}
	if got := containerRow(t, a, "api").Mode; got != model.ModeGuarded {
		t.Errorf("mode = %q, want guarded", got)
	}
}

func TestRebuildIntoGuardedRefusesAMovedWorkspace(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, _ := stubCapturingDevcontainer(t)
	root := gitRepoWithConfig(t, `{"image":"ubuntu","workspaceMount":"source=x,target=/src,type=bind"}`)
	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{mode: model.ModeUnprotected}); err != nil {
		t.Fatalf("create: %v", err)
	}
	before := len(upCalls(t, calls))

	err := runIn(t, a, "container", "rebuild", "api", "--mode", "guarded")
	if got := exitCodeOf(err); got != exitUsage {
		t.Fatalf("exit code = %d (%v), want %d", got, err, exitUsage)
	}
	if got := containerRow(t, a, "api").Mode; got != model.ModeUnprotected {
		t.Errorf("mode = %q, want unprotected", got)
	}
	if after := len(upCalls(t, calls)); after != before {
		t.Errorf("up ran %d more times after a refusal", after-before)
	}
}

func TestRebuildRefusesLeavingIsolated(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	isolatedOn(t, a, "iso", t.TempDir())
	before := containerRow(t, a, "iso")

	err := runIn(t, a, "container", "rebuild", "iso", "--mode", "guarded")
	if got := exitCodeOf(err); got != exitUsage {
		t.Fatalf("exit code = %d (%v), want %d", got, err, exitUsage)
	}
	if !strings.Contains(err.Error(), "dev container fetch") {
		t.Errorf("error %q does not point at fetch", err)
	}
	if after := containerRow(t, a, "iso"); after.Mode != before.Mode || after.WorkspacePath != before.WorkspacePath {
		t.Errorf("row changed: %+v -> %+v", before, after)
	}
}

func TestRebuildIsolatedToIsolatedNeverSeeds(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, _ := stubCapturingDevcontainer(t)
	stubEngineWithMounts(t, "")
	isolatedOn(t, a, "iso", t.TempDir())

	if err := runIn(t, a, "container", "rebuild", "iso", "--mode", "isolated"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	ups := upCalls(t, calls)
	if len(ups) != 1 {
		t.Fatalf("up calls = %v, want one", ups)
	}
	if strings.Contains(ups[0], "--skip-post-create") {
		t.Errorf("a rebuild of an isolated container seeded: %s", ups[0])
	}
}

func generatedGuardedOnRepo(t *testing.T, a *app, name string) string {
	t.Helper()
	root := initRepo(t)
	if err := runContainerCreate(t.Context(), a, "", name, root,
		createOpts{generate: true, tools: []string{"yq"}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	return root
}

func TestRebuildGuardedToIsolatedSeeds(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, _ := stubCapturingDevcontainer(t)
	stubEngineWithMounts(t, "")
	root := generatedGuardedOnRepo(t, a, "api")
	stubDockerWithoutVolumes(t)

	if err := runIn(t, a, "container", "rebuild", "api", "--mode", "isolated"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	ups := upCalls(t, calls)
	last := ups[len(ups)-1]
	if !strings.Contains(last, "--skip-post-create") || !strings.Contains(last, "--remove-existing-container") {
		t.Errorf("up = %s, want a seeding recreate", last)
	}
	c := containerRow(t, a, "api")
	base := filepath.Base(root)
	if c.Mode != model.ModeIsolated || c.WorkspacePath != "/workspaces/"+base {
		t.Errorf("row = mode %q path %q", c.Mode, c.WorkspacePath)
	}
	if want := "source=dev-ws-api,target=/workspaces/" + base + ",type=volume"; !strings.Contains(c.GeneratedConfig, want) {
		t.Errorf("stored document lacks the volume mount:\n%s", c.GeneratedConfig)
	}
}

func TestRebuildWorktreeToIsolatedDropsTheHostBinds(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	calls, captured := stubCapturingDevcontainer(t)
	stubEngineWithMounts(t, "")
	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "feat", wt)
	if err := runContainerCreate(t.Context(), a, "", "feat", wt,
		createOpts{generate: true, tools: []string{"yq"}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	common := containerRow(t, a, "feat").WorktreeRepo
	if common == "" {
		t.Fatal("fixture is not a worktree container")
	}
	stubDockerWithoutVolumes(t)

	if err := runIn(t, a, "container", "rebuild", "feat", "--mode", "isolated"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	mounts := mountsUsedForUp(t, calls, captured)
	// The workspace mount rides in workspaceMount, not mounts: both must show
	// the volume, so an empty list cannot pass.
	if len(mounts) == 0 {
		t.Fatal("the up document has no mounts")
	}
	docs, _ := filepath.Glob(filepath.Join(captured, "*"))
	volume := false
	for _, f := range docs {
		b, _ := os.ReadFile(f)
		volume = volume || strings.Contains(string(b), `"workspaceMount": "source=dev-ws-feat,`) ||
			strings.Contains(string(b), `"workspaceMount":"source=dev-ws-feat,`)
	}
	if !volume {
		t.Errorf("no volume workspace mount in any document up received (mounts: %v)", mounts)
	}
	for _, m := range mounts {
		for _, p := range []string{wt, common} {
			if strings.Contains(m, "source="+p+",") || strings.HasSuffix(m, "source="+p) {
				t.Errorf("host bind survived: %s", m)
			}
		}
	}
	if got := containerRow(t, a, "feat").WorktreeRepo; got != "" {
		t.Errorf("WorktreeRepo = %q, want empty", got)
	}
}

func TestFailedModeRebuildLeavesTheRow(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	stubEngineWithMounts(t, "")
	generatedGuardedOnRepo(t, a, "api")
	before := containerRow(t, a, "api")
	stubDevcontainer(t, "--remove-existing-container")

	if err := runIn(t, a, "container", "rebuild", "api", "--mode", "unprotected"); err == nil {
		t.Fatal("rebuild succeeded against a failing up")
	}
	after := containerRow(t, a, "api")
	if after.Mode != before.Mode || after.WorkspacePath != before.WorkspacePath ||
		after.WorktreeRepo != before.WorktreeRepo || after.GeneratedConfig != before.GeneratedConfig ||
		!slices.Equal(after.Folders, before.Folders) {
		t.Errorf("row changed:\n before %+v\n after  %+v", before, after)
	}
}

func TestLifecycleFailureIntoIsolatedStillRecordsTheMode(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	stubEngineWithMounts(t, "")
	generatedGuardedOnRepo(t, a, "api")
	stubDevcontainer(t, "run-user-commands")
	stubDockerWithoutVolumes(t)

	if err := runIn(t, a, "container", "rebuild", "api", "--mode", "isolated"); err == nil {
		t.Fatal("rebuild succeeded with a failing hook")
	}
	if got := containerRow(t, a, "api").Mode; got != model.ModeIsolated {
		t.Errorf("mode = %q, want isolated", got)
	}
	if e := lastAuditRebuild(t); e.Fields["mode_to"] != "isolated" {
		t.Errorf("record fields = %v", e.Fields)
	}
}

func TestLifecycleFailureIntoIsolatedRecordsTheDigest(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubEchoingDevcontainer(t)
	stubEngineWithMounts(t, "")
	root := gitRepoWithConfig(t, `{"image":"ubuntu"}`)
	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	before := containerRow(t, a, "api").ConfigDigest
	stubDockerWithoutVolumes(t)
	// Fails the hooks, and answers read-configuration like the echoing stub.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "devcontainer"), `#!/bin/sh
case "$1" in
  read-configuration)
    file=""; prev=""
    for a in "$@"; do case "$prev" in --override-config) file="$a" ;; esac; prev="$a"; done
    printf '{"mergedConfiguration":%s}\n' "$(tr -d "\n" < "$file")"; exit 0 ;;
  run-user-commands) echo boom >&2; exit 1 ;;
esac
exit 0
`)
	if err := os.Chmod(filepath.Join(dir, "devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := runIn(t, a, "container", "rebuild", "api", "--mode", "isolated"); err == nil {
		t.Fatal("rebuild succeeded with a failing hook")
	}
	c := containerRow(t, a, "api")
	if c.Mode != model.ModeIsolated {
		t.Fatalf("mode = %q, want isolated", c.Mode)
	}
	if c.ConfigDigest == before {
		t.Errorf("the digest is still the guarded configuration's")
	}
	if err := runIn(t, a, "container", "rebuild", "api"); err != nil {
		t.Errorf("a plain rebuild after the switch: %v", err)
	}
}

func TestRebuildPrintsTheModeChange(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	root := gitRepoWithConfig(t, `{"image":"ubuntu"}`)
	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{mode: model.ModeUnprotected}); err != nil {
		t.Fatalf("create: %v", err)
	}
	out.Reset()

	if err := runIn(t, a, "container", "rebuild", "api", "--mode", "guarded"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if want := "container api rebuilt (unprotected -> guarded)\n"; !strings.Contains(out.String(), want) {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
	out.Reset()
	if err := runIn(t, a, "container", "rebuild", "api"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if got := out.String(); strings.Contains(got, "->") {
		t.Errorf("a rebuild without a change named one: %q", got)
	}
}

func TestRefusedModeChangeLeavesTheToolsAlone(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	stubEngineWithMounts(t, "")
	generatedGuardedOnRepo(t, a, "api")
	stubDockerWithoutVolumes(t)
	if err := runIn(t, a, "container", "rebuild", "api", "--mode", "isolated"); err != nil {
		t.Fatalf("switch: %v", err)
	}
	before := containerRow(t, a, "api")

	err := runIn(t, a, "container", "rebuild", "api", "--mode", "guarded", "--tools", "+helm")
	if got := exitCodeOf(err); got != exitUsage {
		t.Fatalf("exit code = %d (%v), want %d", got, err, exitUsage)
	}
	if after := containerRow(t, a, "api"); after.GeneratedConfig != before.GeneratedConfig {
		t.Errorf("the refused rebuild stored the tool change:\n%s", after.GeneratedConfig)
	}
}

func TestRebuildIntoUnprotectedWarns(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubCapturingDevcontainer(t)
	root := gitRepoWithConfig(t, `{"image":"ubuntu"}`)
	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}

	warnings := captureStderr(t, func() {
		if err := runIn(t, a, "container", "rebuild", "api", "--mode", "unprotected"); err != nil {
			t.Fatalf("rebuild: %v", err)
		}
	})
	if !strings.Contains(warnings, "is unprotected") {
		t.Errorf("warnings = %q", warnings)
	}
}

func TestRebuildAddsAFolderToAnUnprotectedContainer(t *testing.T) {
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
		createOpts{generate: true, tools: []string{"yq"}, mode: model.ModeUnprotected, extraFolders: []string{repo2}}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := runIn(t, a, "container", "rebuild", "multi", "--add-folder", third, "--remove-folder", repo2); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	mounts := mountsUsedForUp(t, calls, captured)
	if !hasMountAt(mounts, "/workspaces/repo-3") {
		t.Errorf("the added folder is not mounted:\n  %s", strings.Join(mounts, "\n  "))
	}
	if hasMount(mounts, "/workspaces/repo-3/.git", true) {
		t.Errorf("the added folder's .git is read-only:\n  %s", strings.Join(mounts, "\n  "))
	}
	if c := containerRow(t, a, "multi"); len(c.Folders) != 1 || c.Folders[0].HostPath != third {
		t.Errorf("folders = %v", c.Folders)
	}
}

// stubEchoingDevcontainer is a devcontainer whose read-configuration reports
// the override document it was given, as the real CLI does, or else the
// project's file.
func stubEchoingDevcontainer(t *testing.T) (calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> '` + calls + `'
if [ "$1" = read-configuration ]; then
  file=""; prev=""
  for a in "$@"; do
    case "$prev" in --override-config) file="$a" ;; --config) [ -n "$file" ] || cfg="$a" ;; esac
    prev="$a"
  done
  [ -n "$file" ] || file="$cfg"
  printf '{"mergedConfiguration":%s}\n' "$(tr -d "\n" < "$file")"
fi
exit 0
`
	writeFile(t, filepath.Join(dir, "devcontainer"), script)
	if err := os.Chmod(filepath.Join(dir, "devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func TestModeSwitchOfAProjectConfigIsNotDrift(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubEchoingDevcontainer(t)
	stubEngineWithMounts(t, "")
	root := gitRepoWithConfig(t, `{"image":"ubuntu"}`)
	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	guardedDigest := containerRow(t, a, "api").ConfigDigest
	if guardedDigest == "" {
		t.Fatal("fixture has no digest")
	}

	if err := runIn(t, a, "container", "rebuild", "api", "--mode", "unprotected"); err != nil {
		t.Fatalf("guarded -> unprotected: %v", err)
	}
	unprotected := containerRow(t, a, "api").ConfigDigest
	if unprotected == guardedDigest {
		t.Errorf("the digest was not updated for the new mode")
	}
	if err := runIn(t, a, "container", "rebuild", "api", "--mode", "guarded"); err != nil {
		t.Fatalf("unprotected -> guarded: %v", err)
	}
	if got := containerRow(t, a, "api").ConfigDigest; got != guardedDigest {
		t.Errorf("digest after the way back = %s, want %s", got, guardedDigest)
	}
}

func TestModeSwitchOfAProjectConfigIntoIsolatedIsNotDrift(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubEchoingDevcontainer(t)
	stubEngineWithMounts(t, "")
	root := gitRepoWithConfig(t, `{"image":"ubuntu"}`)
	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	before := containerRow(t, a, "api").ConfigDigest
	stubDockerWithoutVolumes(t)

	if err := runIn(t, a, "container", "rebuild", "api", "--mode", "isolated"); err != nil {
		t.Fatalf("guarded -> isolated: %v", err)
	}
	c := containerRow(t, a, "api")
	if c.Mode != model.ModeIsolated || c.ConfigDigest == "" || c.ConfigDigest == before {
		t.Errorf("row = mode %q digest %q (was %q)", c.Mode, c.ConfigDigest, before)
	}
}

func TestProjectEditDuringAModeSwitchIsStillDrift(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	stubEchoingDevcontainer(t)
	stubEngineWithMounts(t, "")
	root := gitRepoWithConfig(t, `{"image":"ubuntu"}`)
	if err := runContainerCreate(t.Context(), a, "", "api", root, createOpts{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	before := containerRow(t, a, "api")
	writeFile(t, filepath.Join(root, ".devcontainer", "devcontainer.json"), `{"image":"debian"}`)

	err := runIn(t, a, "container", "rebuild", "api", "--mode", "unprotected")
	if got := exitCodeOf(err); got != exitUsage {
		t.Fatalf("exit code = %d (%v), want %d", got, err, exitUsage)
	}
	after := containerRow(t, a, "api")
	if after.Mode != before.Mode || after.ConfigDigest != before.ConfigDigest {
		t.Errorf("row changed: %+v -> %+v", before, after)
	}
}
