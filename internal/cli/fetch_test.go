package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
)

// isolatedOn records an isolated container on folder without starting it.
func isolatedOn(t *testing.T, a *app, name, folder string) {
	t.Helper()
	if err := runContainerCreate(t.Context(), a, "", name, folder,
		createOpts{generate: true, tools: []string{"yq"}, noStart: true, mode: model.ModeIsolated}); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
}

// stubContainerRepo runs the container's commands on the host, against a real
// repository at dir.
func stubContainerRepo(t *testing.T, dir string) {
	t.Helper()
	old := inContainer
	inContainer = func(model.Container, string) string { return dir }
	t.Cleanup(func() { inContainer = old })

	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "devcontainer"), `#!/bin/sh
case "$1" in
  exec) ;;
  read-configuration) echo '{"mergedConfiguration":{}}'; exit 0 ;;
  *) exit 0 ;;
esac
shift
while [ $# -gt 0 ]; do
  case "$1" in
    --workspace-folder|--id-label|--config|--override-config|--remote-env) shift 2 ;;
    *) break ;;
  esac
done
exec "$@"
`)
	if err := os.Chmod(filepath.Join(bin, "devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(bin, "docker"), "#!/bin/sh\ncase \"$*\" in\n  *State*) echo running ;;\n  ps*) echo abc123 ;;\nesac\nexit 0\n")
	if err := os.Chmod(filepath.Join(bin, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestFetchBringsTheContainersBranchesHome(t *testing.T) {
	requireGit(t)
	a, out := newTestApp(t)
	seedWorkspace(t, a)
	folder := initRepo(t)
	isolatedOn(t, a, "iso", folder)

	inside := t.TempDir()
	gitIn(t, inside, "clone", "-q", folder, ".")
	gitIn(t, inside, "switch", "-q", "-c", "agent-work")
	gitIn(t, inside, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "from the agent")
	stubContainerRepo(t, inside)

	if err := runContainerFetch(t.Context(), a, "", "iso"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	log := gitOutput(t, folder, "log", "--format=%s", "-1", "refs/remotes/iso/agent-work")
	if log != "from the agent" {
		t.Errorf("refs/remotes/iso/agent-work = %q, want the agent's commit", log)
	}
	if strings.Contains(gitOutput(t, folder, "branch", "--list", "agent-work"), "agent-work") {
		t.Error("fetch created a local branch; it should only add remote-tracking refs")
	}
	if !strings.Contains(out.String(), "iso/agent-work") {
		t.Errorf("output does not name what it fetched:\n%s", out)
	}
}

func TestFetchRefusesAGuardedContainer(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	folder := initRepo(t)
	containerOn(t, a, "api", folder, createOpts{})

	err := runContainerFetch(t.Context(), a, "", "api")
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d (%v), want %d", got, err, exitUsage)
	}
}

func TestRemoveRefusesUnfetchedIsolatedWork(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	folder := initRepo(t)
	isolatedOn(t, a, "iso", folder)

	inside := t.TempDir()
	gitIn(t, inside, "clone", "-q", folder, ".")
	gitIn(t, inside, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "unfetched")
	stubContainerRepo(t, inside)

	err := runContainerRemove(t.Context(), a, "", "iso", false)
	if got := exitCodeOf(err); got != exitUsage {
		t.Fatalf("exit code = %d (%v), want %d", got, err, exitUsage)
	}
	if !strings.Contains(err.Error(), "dev container fetch") {
		t.Errorf("error %q does not name fetch", err)
	}

	if err := runContainerFetch(t.Context(), a, "", "iso"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	err = runContainerRemove(t.Context(), a, "", "iso", false)
	if got := exitCodeOf(err); got != exitUsage {
		t.Fatalf("remove after fetch, before merge: exit code = %d (%v), want %d", got, err, exitUsage)
	}
	branch := gitOutput(t, inside, "branch", "--show-current")
	if !strings.Contains(err.Error(), "branch "+branch) {
		t.Errorf("error %q does not name the unmerged branch", err)
	}

	gitIn(t, folder, "merge", "-q", "--ff-only", "iso/"+branch)
	if err := runContainerRemove(t.Context(), a, "", "iso", false); err != nil {
		t.Fatalf("remove after merge: %v", err)
	}
	if refs := gitOutput(t, folder, "for-each-ref", "refs/remotes/iso/"); refs != "" {
		t.Errorf("remove left the fetched refs behind:\n%s", refs)
	}
}

func TestRemoveAcceptsWorkPushedToARemote(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	folder := initRepo(t)
	isolatedOn(t, a, "iso", folder)

	upstream := t.TempDir()
	gitIn(t, upstream, "init", "-q", "--bare")
	gitIn(t, folder, "remote", "add", "upstream", upstream)

	inside := t.TempDir()
	gitIn(t, inside, "clone", "-q", folder, ".")
	gitIn(t, inside, "switch", "-q", "-c", "agent-work")
	gitIn(t, inside, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "pushed")
	gitIn(t, inside, "push", "-q", upstream, "agent-work")
	stubContainerRepo(t, inside)

	gitIn(t, folder, "fetch", "-q", "upstream")
	if err := runContainerRemove(t.Context(), a, "", "iso", false); err != nil {
		t.Errorf("remove with the branch on a remote: %v", err)
	}
}

func TestRemoveForceKeepsTheFetchedRefs(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	folder := initRepo(t)
	isolatedOn(t, a, "iso", folder)

	inside := t.TempDir()
	gitIn(t, inside, "clone", "-q", folder, ".")
	gitIn(t, inside, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "unmerged")
	stubContainerRepo(t, inside)

	if err := runContainerFetch(t.Context(), a, "", "iso"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if err := runContainerRemove(t.Context(), a, "", "iso", true); err != nil {
		t.Fatalf("remove --force: %v", err)
	}
	if refs := gitOutput(t, folder, "for-each-ref", "refs/remotes/iso/"); refs == "" {
		t.Error("remove --force deleted the fetched refs, the only copy of unmerged work")
	}
}

func TestRemoveRefusesUncommittedIsolatedWork(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	folder := initRepo(t)
	isolatedOn(t, a, "iso", folder)

	inside := t.TempDir()
	gitIn(t, inside, "clone", "-q", folder, ".")
	writeFile(t, filepath.Join(inside, "half-done"), "work")
	stubContainerRepo(t, inside)

	err := runContainerRemove(t.Context(), a, "", "iso", false)
	if got := exitCodeOf(err); got != exitUsage {
		t.Fatalf("exit code = %d (%v), want %d", got, err, exitUsage)
	}
	if !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("error %q does not say there are uncommitted changes", err)
	}
	if err := runContainerRemove(t.Context(), a, "", "iso", true); err != nil {
		t.Errorf("remove --force: %v", err)
	}
}

// gitOutput runs git in dir and returns its trimmed output.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func TestRemoveLeavesARealRemotesRefs(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	folder := initRepo(t)
	isolatedOn(t, a, "upstream", folder)

	bare := t.TempDir()
	gitIn(t, bare, "init", "-q", "--bare")
	gitIn(t, folder, "remote", "add", "upstream", bare)
	gitIn(t, folder, "push", "-q", "upstream", "HEAD:refs/heads/kept")
	gitIn(t, folder, "fetch", "-q", "upstream")

	inside := t.TempDir()
	gitIn(t, inside, "clone", "-q", folder, ".")
	stubContainerRepo(t, inside)

	if err := runContainerRemove(t.Context(), a, "", "upstream", false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if refs := gitOutput(t, folder, "for-each-ref", "refs/remotes/upstream/"); refs == "" {
		t.Error("remove deleted the tracking refs of a git remote that shares the container's name")
	}
}

func TestFetchRefusesANameAGitRemoteHas(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	folder := initRepo(t)
	isolatedOn(t, a, "upstream", folder)
	gitIn(t, folder, "remote", "add", "upstream", t.TempDir())

	inside := t.TempDir()
	gitIn(t, inside, "clone", "-q", folder, ".")
	stubContainerRepo(t, inside)

	err := runContainerFetch(t.Context(), a, "", "upstream")
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d (%v), want %d", got, err, exitUsage)
	}
}
