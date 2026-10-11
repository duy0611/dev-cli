//go:build smoke

package smoke

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// SPIKE — throwaway. TestSmokeReadOnlyGitDir showed a read-only .git closing
// both of the guard's gaps for an ordinary checkout. A worktree and a
// submodule each add a git directory that host git reaches through a .git
// *file* in a writable tree: the worktree checkout's .git, and the
// submodule's. Their git directories live under the repository's .git
// (worktrees/<name>, modules/<name>), so the read-only mount covers those; the
// .git files themselves are another matter.
//
// What the read-only mount covers is asserted. What the .git files leave open
// is logged, not asserted: a file in a writable tree is what docs/SECURITY.md
// gaps 1 to 3 are about, so the point is to see where the boundary falls.
//
// Layout as dev's worktree containers use it (invariant 11): the checkout and
// the repository each mounted at their own host path, since the worktree's
// .git file and the repository's backlink name absolute host paths.
func TestSmokeReadOnlyGitDirWorktreeAndSubmodule(t *testing.T) {
	requireBinaries(t, "docker", "git")

	// Physical paths: the engine resolves the source inside its VM
	// (invariant 2), and the worktree's .git file names them.
	physical := func(p string) string {
		t.Helper()
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	hostGit := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=smoke", "GIT_AUTHOR_EMAIL=smoke@example.com",
			"GIT_COMMITTER_NAME=smoke", "GIT_COMMITTER_EMAIL=smoke@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("host git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	project := physical(gitFixture(t))
	write(filepath.Join(project, "f.txt"), "one\n")
	hostGit(project, "add", "f.txt")
	hostGit(project, "commit", "-q", "-m", "init")

	// A submodule from a second local repository. file:// transport is off by
	// default since git 2.38.1, hence the -c.
	subsrc := physical(t.TempDir())
	hostGit(subsrc, "init", "-q", ".")
	write(filepath.Join(subsrc, "s.txt"), "sub\n")
	hostGit(subsrc, "add", "s.txt")
	hostGit(subsrc, "commit", "-q", "-m", "sub init")
	hostGit(project, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subsrc, "sub")
	hostGit(project, "commit", "-q", "-m", "add submodule")

	wt := filepath.Join(physical(t.TempDir()), "wt")
	hostGit(project, "worktree", "add", "-q", "-b", "rogit-wt", wt)

	common := filepath.Join(project, ".git")
	wtGitFile := filepath.Join(wt, ".git")
	subGitFile := filepath.Join(project, "sub", ".git")
	caseInsensitive := false
	if _, err := os.Stat(filepath.Join(project, ".GIT")); err == nil {
		caseInsensitive = true
	}

	// Kept to restore at the end: the case-variant probes may append to them,
	// and a gitlink with a line after its gitdir no longer parses.
	origWt, err := os.ReadFile(wtGitFile)
	if err != nil {
		t.Fatal(err)
	}
	origSub, err := os.ReadFile(subGitFile)
	if err != nil {
		t.Fatal(err)
	}

	const name = "dev-smoke-rogit-nested"
	_ = exec.Command("docker", "rm", "-f", name).Run()
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	// Both trees writable at their own paths, the repository's .git read-only
	// over them, and the worktree's .git file read-only on its own — which is
	// what the guard does for it today.
	engine(t, "run", "-d", "--name", name,
		"--mount", "type=bind,source="+project+",target="+project,
		"--mount", "type=bind,source="+common+",target="+common+",readonly",
		"--mount", "type=bind,source="+wt+",target="+wt,
		"--mount", "type=bind,source="+wtGitFile+",target="+wtGitFile+",readonly",
		roMountImage, "sleep", "600")
	t.Log("installing git in the container")
	engine(t, "exec", name, "apk", "add", "--no-cache", "-q", "git")
	// Both trees belong to the host user, so git refuses them as dubious
	// ownership without this. Root's global config, inside the container.
	engine(t, "exec", name, "git", "config", "--global", "--add", "safe.directory", "*")

	inContainer := func(args ...string) ([]byte, error) {
		return exec.Command("docker", append([]string{"exec", name}, args...)...).CombinedOutput()
	}

	// Reading works through both kinds of .git file; writing to either
	// repository does not.
	for _, c := range []struct {
		label string
		args  []string
		want  bool
	}{
		{"status in the worktree", []string{"git", "-C", wt, "status", "--short"}, true},
		{"log in the worktree", []string{"git", "-C", wt, "log", "--oneline", "-1"}, true},
		{"status in the superproject, which looks into the submodule", []string{"git", "-C", project, "status", "--short"}, true},
		{"log in the submodule", []string{"git", "-C", filepath.Join(project, "sub"), "log", "--oneline", "-1"}, true},
		{"commit in the worktree", []string{"git", "-C", wt, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "x"}, false},
		{"commit in the submodule", []string{"git", "-C", filepath.Join(project, "sub"), "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "x"}, false},
	} {
		out, err := inContainer(c.args...)
		t.Logf("in the container, %s: ok=%v %s", c.label, err == nil, firstLine(out))
		if (err == nil) != c.want {
			t.Errorf("in the container, %s: ok=%v, want %v", c.label, err == nil, c.want)
		}
	}

	// Under the read-only .git, by any name: asserted.
	type probe struct{ label, path, marker string }
	covered := []probe{
		{"the repository's config, which the worktree shares", filepath.Join(common, "config"), "# rogit-nested-1"},
		{"the worktree's config.worktree", filepath.Join(common, "worktrees", "wt", "config.worktree"), "# rogit-nested-2"},
		{"the worktree's commondir", filepath.Join(common, "worktrees", "wt", "commondir"), "# rogit-nested-3"},
		{"the submodule's config", filepath.Join(common, "modules", "sub", "config"), "# rogit-nested-4"},
		{"a new hook in the submodule's git directory", filepath.Join(common, "modules", "sub", "hooks", "rogit-probe"), "# rogit-nested-5"},
	}
	if caseInsensitive {
		covered = append(covered,
			probe{"the submodule's config, as modules/sub/CONFIG", filepath.Join(common, "modules", "sub", "CONFIG"), "# rogit-nested-6"},
			probe{"the worktree's config.worktree, as worktrees/wt/CONFIG.WORKTREE", filepath.Join(common, "worktrees", "wt", "CONFIG.WORKTREE"), "# rogit-nested-7"},
		)
	}
	// The host file a probe would land in: the case variants resolve to the
	// lowercase name on a case-insensitive host.
	hostFileOf := func(p string) string {
		dir, base := filepath.Split(p)
		return filepath.Join(dir, strings.ToLower(base))
	}
	tryCovered := func(when string) {
		t.Helper()
		for _, p := range covered {
			out, err := inContainer("sh", "-c", fmt.Sprintf("echo '%s' >> '%s'", p.marker, p.path))
			b, _ := os.ReadFile(hostFileOf(p.path))
			switch {
			case strings.Contains(string(b), p.marker):
				t.Errorf("%s: %s: the container's write reached the host (succeeded in the container: %v)", when, p.label, err == nil)
			case err == nil:
				t.Logf("%s: %s: succeeded in the container but did not reach the host", when, p.label)
			default:
				t.Logf("%s: %s: refused: %s", when, p.label, firstLine(out))
			}
		}
	}

	// The .git files, probed by opening for append and writing nothing, so a
	// gitlink that turns out writable is not damaged by finding out.
	openable := func(path string) bool {
		_, err := inContainer("sh", "-c", fmt.Sprintf("( : >> '%s' ) 2>/dev/null", path))
		return err == nil
	}
	gitlinks := []struct{ label, path string }{
		{"the worktree checkout's .git file, read-only on its own", wtGitFile},
		{"the submodule's .git file, in the writable workspace", subGitFile},
	}
	reportGitlinks := func(when string) {
		t.Helper()
		for _, g := range gitlinks {
			if openable(g.path) {
				t.Logf("%s: EXPOSED: %s opens for write in the container, and host git follows it", when, g.label)
			} else {
				t.Logf("%s: held: %s refuses writes", when, g.label)
			}
		}
	}

	tryCovered("before any host write")
	reportGitlinks("before any host write")

	// Host git beside the container: each must succeed, and the covered paths
	// must still refuse after it.
	write(filepath.Join(wt, "host.txt"), "host\n")
	for _, step := range []struct {
		dir  string
		args []string
	}{
		{project, []string{"config", "rogit.host", "1"}},
		{wt, []string{"add", "host.txt"}},
		{wt, []string{"commit", "-q", "-m", "host commit in the worktree"}},
		{filepath.Join(project, "sub"), []string{"config", "rogit.host", "1"}},
	} {
		hostGit(step.dir, step.args...)
		label := "host git " + strings.Join(step.args, " ") + " in " + filepath.Base(step.dir)
		t.Logf("%s: ok", label)
		tryCovered("after " + label)
	}

	// What `git worktree repair` does when it rewrites the checkout's .git
	// file: a new file renamed over the path its single-file mount sits on.
	tmp := wtGitFile + ".lock"
	write(tmp, string(origWt))
	if err := os.Rename(tmp, wtGitFile); err != nil {
		t.Fatal(err)
	}
	// Watched: on a podman machine a single-file mount held for about 1.5s
	// after a host rename before it opened.
	start := time.Now()
	opened := false
	for time.Since(start) < roGitWatch {
		if openable(wtGitFile) {
			t.Logf("%.1fs after the host rewrote the worktree's .git file: EXPOSED, it opens for write in the container",
				time.Since(start).Seconds())
			opened = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !opened {
		t.Logf("after the host rewrote the worktree's .git file: held for %v", roGitWatch)
	}
	tryCovered("after the host rewrote the worktree's .git file")

	// Last, and restored after: a marker appended to a gitlink breaks it.
	if caseInsensitive {
		for _, v := range []struct{ label, variant, host, marker string }{
			{"the worktree's .git file, as .GIT", filepath.Join(wt, ".GIT"), wtGitFile, "# rogit-nested-8"},
			{"the submodule's .git file, as .GIT", filepath.Join(project, "sub", ".GIT"), subGitFile, "# rogit-nested-9"},
		} {
			_, err := inContainer("sh", "-c", fmt.Sprintf("echo '%s' >> '%s'", v.marker, v.variant))
			b, _ := os.ReadFile(v.host)
			if strings.Contains(string(b), v.marker) {
				t.Logf("EXPOSED: %s: the container's write reached the host's file", v.label)
			} else {
				t.Logf("%s: did not reach the host (succeeded in the container: %v)", v.label, err == nil)
			}
		}
	} else {
		t.Log("the host filesystem is case-sensitive; the case-variant probes are skipped")
	}
	write(wtGitFile, string(origWt))
	write(subGitFile, string(origSub))

	// The container reads the host's worktree commit through the read-only
	// mount.
	out, err := inContainer("git", "-C", wt, "log", "--oneline", "-1")
	if err != nil || !strings.Contains(string(out), "host commit in the worktree") {
		t.Errorf("the container does not see the host's worktree commit: %v %s", err, out)
	}
}
