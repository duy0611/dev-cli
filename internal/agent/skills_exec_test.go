package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/duy0611/dev-cli/internal/agentcfg"
)

// runLocally runs a step's command with the host's own sh, which is what a
// container's sh does with the same argv.
func runLocally(t *testing.T, s Step) error {
	t.Helper()
	cmd := exec.Command(s.Cmd[0], s.Cmd[1:]...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	t.Logf("%s", out)
	return err
}

func gitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "."}, {"add", "-A"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-qm", "x"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// A skill repository is third-party content. A symlink in it must not let the
// skill land outside the skills directory, and must never become a path the
// step deletes through: `rm -rf "$d/.git"` on a skill that is a link to a
// checkout deletes that checkout's history.
func TestAGitSkillThatIsASymlinkIsRefused(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	victim := t.TempDir()
	if err := os.WriteFile(filepath.Join(victim, "SKILL.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(victim, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	repo := t.TempDir()
	if err := os.Symlink(victim, filepath.Join(repo, "evil")); err != nil {
		t.Fatal(err)
	}
	gitRepo(t, repo)

	skills := t.TempDir()
	steps := skillSteps("claude", skills,
		[]agentcfg.Skill{{Name: "evil", Git: "file://" + repo, Path: "evil"}})
	if err := runLocally(t, steps[0]); err == nil {
		t.Error("a skill that is a symlink out of its repository was installed")
	}
	if _, err := os.Stat(filepath.Join(victim, ".git")); err != nil {
		t.Errorf("the step deleted through the symlink: %v", err)
	}
	if fi, err := os.Lstat(filepath.Join(skills, "evil")); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		t.Error("the installed skill is a symlink")
	}
}

// The ordinary case still works end to end.
func TestAGitSkillIsCopiedWithoutItsHistory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "skills", "good"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "skills", "good", "SKILL.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRepo(t, repo)

	skills := t.TempDir()
	steps := skillSteps("claude", skills,
		[]agentcfg.Skill{{Name: "good", Git: "file://" + repo, Path: "skills/good"}})
	if err := runLocally(t, steps[0]); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skills, "good", "SKILL.md")); err != nil {
		t.Errorf("skill not installed: %v", err)
	}
}

// Several skills from one clone all land, and one bad path installs none of
// them: a half-applied repository is harder to notice than a failed step.
func TestGitSkillsFromOneRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(repo, "skills", name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "skills", name, "SKILL.md"), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitRepo(t, repo)
	git := "file://" + repo

	skills := t.TempDir()
	steps := skillSteps("claude", skills, []agentcfg.Skill{
		{Name: "a", Git: git, Path: "skills/a"},
		{Name: "b", Git: git, Path: "skills/b"},
	})
	if len(steps) != 1 {
		t.Fatalf("steps = %d, want one clone", len(steps))
	}
	if err := runLocally(t, steps[0]); err != nil {
		t.Fatalf("install: %v", err)
	}
	for _, name := range []string{"a", "b"} {
		if got, err := os.ReadFile(filepath.Join(skills, name, "SKILL.md")); err != nil || string(got) != name {
			t.Errorf("skill %s: %q, %v", name, got, err)
		}
	}

	fresh := t.TempDir()
	steps = skillSteps("claude", fresh, []agentcfg.Skill{
		{Name: "a", Git: git, Path: "skills/a"},
		{Name: "gone", Git: git, Path: "skills/gone"},
	})
	if err := runLocally(t, steps[0]); err == nil {
		t.Error("a repository with a missing skill was installed")
	}
	if entries, _ := os.ReadDir(fresh); len(entries) != 0 {
		t.Errorf("a failed repository left %v behind", entries)
	}
}

// The same escape through a directory above the skill: path a/b with a -> /.
func TestAGitSkillBehindASymlinkedDirectoryIsRefused(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "b", "SKILL.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "a")); err != nil {
		t.Fatal(err)
	}
	gitRepo(t, repo)

	skills := t.TempDir()
	steps := skillSteps("claude", skills,
		[]agentcfg.Skill{{Name: "b", Git: "file://" + repo, Path: "a/b"}})
	if err := runLocally(t, steps[0]); err == nil {
		t.Error("a skill reached through a symlinked directory was installed")
	}
}

// A write that is cut off must leave the old file whole. The next apply reads
// that file back and merges into it, so a truncated one would silently drop
// every setting the operator had there.
func TestAnInterruptedWriteLeavesTheFileWhole(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(file, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := writeFileCmd(dir + "/opencode.json")
	w := exec.Command(cmd[0], cmd[1:]...)
	w.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := w.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write([]byte(`{"th`)); err != nil {
		t.Fatal(err)
	}
	// Wait until the write is under way — cat has started consuming — so the
	// kill lands mid-stream rather than before the shell opened anything.
	deadline := time.Now().Add(5 * time.Second)
	for !partialWritten(dir) {
		if time.Now().After(deadline) {
			t.Fatal("the write never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The exec dying mid-stream, as a dropped kubectl exec or a Ctrl-C would.
	// The whole process group, so cat goes with the shell.
	_ = syscall.Kill(-w.Process.Pid, syscall.SIGKILL)
	_ = w.Wait()

	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"theme":"dark"}` {
		t.Errorf("file = %q after an interrupted write", got)
	}

	// And a completed write still lands.
	done := exec.Command(cmd[0], cmd[1:]...)
	done.Stdin = strings.NewReader(`{"theme":"light"}`)
	if out, err := done.CombinedOutput(); err != nil {
		t.Fatalf("write: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(file); string(got) != `{"theme":"light"}` {
		t.Errorf("file = %q after a completed write", got)
	}
}

// partialWritten reports whether some file in dir already holds the four bytes
// the interrupted write sent: the target itself when the write truncates in
// place, a temporary beside it when the write goes through a rename.
func partialWritten(dir string) bool {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil && string(b) == `{"th` {
			return true
		}
	}
	return false
}
