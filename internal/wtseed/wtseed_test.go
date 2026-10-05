package wtseed

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Real git, as in internal/gitwt: the behaviour under test is git's matching.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fixture is a repository whose main checkout holds a typical spread of
// ignored files, plus a fresh worktree of it on branch "feat".
func fixture(t *testing.T) (source, dest string) {
	t.Helper()
	requireGit(t)
	source = t.TempDir()
	git(t, source, "init", "-q", ".")
	git(t, source, "config", "user.email", "test@example.com")
	git(t, source, "config", "user.name", "test")
	write(t, filepath.Join(source, ".gitignore"),
		".env*\nnode_modules/\nbuild/\nconfig/local.json\n")
	write(t, filepath.Join(source, "config", "app.json"), "{}")
	git(t, source, "add", ".")
	git(t, source, "commit", "-qm", "init")

	write(t, filepath.Join(source, ".env"), "A=1")
	write(t, filepath.Join(source, ".env.production"), "P=1")
	write(t, filepath.Join(source, "node_modules", "a", "index.js"), "a")
	write(t, filepath.Join(source, "node_modules", "b", "index.js"), "b")
	write(t, filepath.Join(source, "build", "cache", "c"), "c")
	write(t, filepath.Join(source, "build", "out", "o"), "o")
	write(t, filepath.Join(source, "config", "local.json"), "{}")
	write(t, filepath.Join(source, "scratch.txt"), "not ignored")

	dest = filepath.Join(t.TempDir(), "wt")
	git(t, source, "worktree", "add", "-q", dest, "-b", "feat")
	return source, dest
}

func manifest(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "include")
	write(t, p, content)
	return p
}

func selectWith(t *testing.T, source, dest string, files ...string) string {
	t.Helper()
	got, err := Select(t.Context(), source, dest, files)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	return strings.Join(got, ",")
}

func TestSelectCopiesIgnoredMatchesOnly(t *testing.T) {
	source, dest := fixture(t)
	// scratch.txt matches `*` but is not ignored; config/app.json is tracked.
	got := selectWith(t, source, dest, manifest(t, "*\n"))
	want := ".env,.env.production,build/,config/local.json,node_modules/"
	if got != want {
		t.Errorf("Select = %s, want %s", got, want)
	}
}

// A directory is one entry only when everything under it was chosen; a
// pattern naming part of it yields the part.
func TestSelectCollapsesOnlyWholeDirectories(t *testing.T) {
	source, dest := fixture(t)
	got := selectWith(t, source, dest, manifest(t, "build/cache/\nnode_modules/\n"))
	if want := "build/cache/,node_modules/"; got != want {
		t.Errorf("Select = %s, want %s", got, want)
	}
}

// The new branch commits a file the source branch ignores: copying it would
// silently change the branch.
func TestSelectSkipsWhatTheNewCheckoutTracks(t *testing.T) {
	source, dest := fixture(t)
	write(t, filepath.Join(dest, ".env"), "COMMITTED=1")
	git(t, dest, "add", "-f", ".env")
	git(t, dest, "commit", "-qm", "commit env")

	got := selectWith(t, source, dest, manifest(t, ".env*\n"))
	if got != ".env.production" {
		t.Errorf("Select = %s, want .env.production", got)
	}
}

// A directory the new checkout tracks something in must not be copied whole:
// cp would nest the copy inside the existing directory.
func TestSelectSplitsADirectoryTheNewCheckoutHas(t *testing.T) {
	source, dest := fixture(t)
	write(t, filepath.Join(dest, "node_modules", "vendored.js"), "v")
	git(t, dest, "add", "-f", "node_modules/vendored.js")
	git(t, dest, "commit", "-qm", "vendor")

	got := selectWith(t, source, dest, manifest(t, "node_modules/\n"))
	if want := "node_modules/a/,node_modules/b/"; got != want {
		t.Errorf("Select = %s, want %s", got, want)
	}
}

func TestSelectAppliesLaterFilesOnTop(t *testing.T) {
	source, dest := fixture(t)
	repo := manifest(t, ".env*\n")
	cases := []struct {
		name  string
		files []string
		want  string
	}{
		{"repository only", []string{repo}, ".env,.env.production"},
		{"flag only", []string{manifest(t, "node_modules/\n")}, "node_modules/"},
		{"both add up", []string{repo, manifest(t, "node_modules/\n")},
			".env,.env.production,node_modules/"},
		{"flag negation narrows", []string{repo, manifest(t, "!.env.production\n")}, ".env"},
		{"none", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := selectWith(t, source, dest, c.files...); got != c.want {
				t.Errorf("Select = %s, want %s", got, c.want)
			}
		})
	}
}

func TestPatternFiles(t *testing.T) {
	source := t.TempDir()
	flag := manifest(t, "x\n")

	got, err := PatternFiles(source, "")
	if err != nil || len(got) != 0 {
		t.Errorf("no manifest, no flag = %v, %v; want nothing", got, err)
	}
	if got, err = PatternFiles(source, flag); err != nil || len(got) != 1 || got[0] != flag {
		t.Errorf("flag only = %v, %v; want [%s]", got, err, flag)
	}

	own := filepath.Join(source, FileName)
	write(t, own, "y\n")
	got, err = PatternFiles(source, flag)
	if err != nil || len(got) != 2 || got[0] != own || got[1] != flag {
		t.Errorf("both = %v, %v; want the repository's first, then the flag's", got, err)
	}
}

func TestPatternFilesRefusesAMissingFlagFile(t *testing.T) {
	if _, err := PatternFiles(t.TempDir(), filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("a missing --include-file was accepted")
	}
	if _, err := PatternFiles(t.TempDir(), t.TempDir()); err == nil {
		t.Error("a directory as --include-file was accepted")
	}
}

// A relative --include-file means relative to where the operator stood, not to
// the source checkout git is later run from.
func TestPatternFilesMakesTheFlagAbsolute(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "inc"), "x\n")
	t.Chdir(dir)
	got, err := PatternFiles(t.TempDir(), "inc")
	if err != nil || len(got) != 1 || !filepath.IsAbs(got[0]) {
		t.Errorf("PatternFiles = %v, %v; want one absolute path", got, err)
	}
}

func TestPatternFilesWithNoSource(t *testing.T) {
	if got, err := PatternFiles("", ""); err != nil || len(got) != 0 {
		t.Errorf("no source, no flag = %v, %v; want nothing", got, err)
	}
	if _, err := PatternFiles("", manifest(t, "x\n")); !errors.Is(err, ErrNoSource) {
		t.Errorf("no source with a flag = %v, want ErrNoSource", err)
	}
}

func TestCopy(t *testing.T) {
	source, dest := t.TempDir(), t.TempDir()
	write(t, filepath.Join(source, "apps", "web", ".env"), "W=1")
	write(t, filepath.Join(source, "node_modules", "a", "index.js"), "a")
	if err := os.Chmod(filepath.Join(source, "node_modules", "a", "index.js"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a/index.js", filepath.Join(source, "node_modules", "link")); err != nil {
		t.Fatal(err)
	}

	if err := Copy(t.Context(), source, dest, []string{"apps/web/.env", "node_modules/"}); err != nil {
		t.Fatalf("Copy: %v", err)
	}

	if b, err := os.ReadFile(filepath.Join(dest, "apps", "web", ".env")); err != nil || string(b) != "W=1" {
		t.Errorf("nested file = %q, %v", b, err)
	}
	fi, err := os.Stat(filepath.Join(dest, "node_modules", "a", "index.js"))
	if err != nil {
		t.Fatalf("file inside the copied directory: %v", err)
	}
	if fi.Mode().Perm() != 0o751 {
		t.Errorf("mode = %v, want 0751", fi.Mode().Perm())
	}
	// Copied as a link, not followed: a relative link resolves in dest.
	if target, err := os.Readlink(filepath.Join(dest, "node_modules", "link")); err != nil || target != "a/index.js" {
		t.Errorf("symlink = %q, %v; want a link to a/index.js", target, err)
	}
	// A directory entry lands as the directory, not nested inside one.
	if _, err := os.Stat(filepath.Join(dest, "node_modules", "node_modules")); err == nil {
		t.Error("node_modules was copied into itself")
	}
}

func TestCopyNamesTheFailingPath(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads unreadable files")
	}
	source, dest := t.TempDir(), t.TempDir()
	write(t, filepath.Join(source, "secret"), "s")
	if err := os.Chmod(filepath.Join(source, "secret"), 0o000); err != nil {
		t.Fatal(err)
	}

	err := Copy(t.Context(), source, dest, []string{"secret"})
	if err == nil || !strings.Contains(err.Error(), "secret") {
		t.Errorf("Copy = %v, want an error naming secret", err)
	}
}

func TestCopyRefusesAnExistingDestination(t *testing.T) {
	source, dest := t.TempDir(), t.TempDir()
	write(t, filepath.Join(source, "d", "f"), "new")
	write(t, filepath.Join(dest, "d", "f"), "old")

	if err := Copy(t.Context(), source, dest, []string{"d/"}); err == nil {
		t.Error("Copy over an existing directory succeeded")
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "d", "f")); string(b) != "old" {
		t.Errorf("existing file = %q, want it untouched", b)
	}
}
