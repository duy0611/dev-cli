package review

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runTestGit(t, dir, "init", "-q", "-b", "main")
	runTestGit(t, dir, "config", "user.email", "test@example.com")
	runTestGit(t, dir, "config", "user.name", "Test")
	return dir
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitTestRepo(t *testing.T, dir, message string) {
	t.Helper()
	runTestGit(t, dir, "add", "-A")
	runTestGit(t, dir, "commit", "-q", "-m", message)
}

func collectFile(t *testing.T, files []File, path string) *File {
	t.Helper()
	for i := range files {
		if files[i].Path == path {
			return &files[i]
		}
	}
	t.Fatalf("no file %q in %v", path, filePaths(files))
	return nil
}

func filePaths(files []File) []string {
	out := make([]string, len(files))
	for i := range files {
		out[i] = files[i].Path
	}
	return out
}

func TestCollectTrackedAndStaged(t *testing.T) {
	requireGit(t)
	dir := initTestRepo(t)
	writeTestFile(t, filepath.Join(dir, "a.go"), "package a\n")
	writeTestFile(t, filepath.Join(dir, "b.go"), "package b\n")
	commitTestRepo(t, dir, "initial")

	writeTestFile(t, filepath.Join(dir, "a.go"), "package a\n// unstaged\n")
	writeTestFile(t, filepath.Join(dir, "b.go"), "package b\n// staged\n")
	runTestGit(t, dir, "add", "b.go")

	got, err := Collect(t.Context(), []Folder{{Path: dir}})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	collectFile(t, got.Files, "a.go")
	collectFile(t, got.Files, "b.go")
}

func TestCollectUntrackedWithoutTouchingTheIndex(t *testing.T) {
	requireGit(t)
	dir := initTestRepo(t)
	writeTestFile(t, filepath.Join(dir, "old.go"), "package old\n")
	writeTestFile(t, filepath.Join(dir, ".gitignore"), ".env\n")
	commitTestRepo(t, dir, "initial")

	writeTestFile(t, filepath.Join(dir, "new.go"), "package new\n")
	writeTestFile(t, filepath.Join(dir, ".env"), "SECRET=1\n")

	indexPath := filepath.Join(dir, ".git", "index")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}

	got, err := Collect(t.Context(), []Folder{{Path: dir}})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("Collect changed .git/index; it must not stage or touch untracked files")
	}

	f := collectFile(t, got.Files, "new.go")
	if len(f.Hunks) != 1 {
		t.Fatalf("new.go has %d hunks, want 1", len(f.Hunks))
	}
	if len(f.Hunks[0].Lines) != 1 || f.Hunks[0].Lines[0].Kind != Added {
		t.Errorf("new.go hunk = %+v, want one added line", f.Hunks[0].Lines)
	}
	for _, file := range got.Files {
		if file.Path == ".env" {
			t.Error(".env is ignored but appeared in the diff")
		}
	}
}

func TestCollectLargeUntrackedIsNoted(t *testing.T) {
	requireGit(t)
	dir := initTestRepo(t)
	writeTestFile(t, filepath.Join(dir, "small.go"), "package small\n")
	commitTestRepo(t, dir, "initial")

	large := strings.Repeat("x", 300<<10)
	writeTestFile(t, filepath.Join(dir, "artifact.bin"), large)
	writeTestFile(t, filepath.Join(dir, "small.go"), "package small\n// "+large+"\n")

	got, err := Collect(t.Context(), []Folder{{Path: dir}})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	f := collectFile(t, got.Files, "artifact.bin")
	if f.Note == "" {
		t.Error("oversized untracked file has no Note")
	}
	if len(f.Hunks) != 0 {
		t.Errorf("oversized untracked file has %d hunks, want none", len(f.Hunks))
	}

	tracked := collectFile(t, got.Files, "small.go")
	if len(tracked.Hunks) == 0 {
		t.Error("a large tracked change was not shown in full")
	}
}

func TestCollectRepoWithoutCommits(t *testing.T) {
	requireGit(t)
	dir := initTestRepo(t)

	writeTestFile(t, filepath.Join(dir, "staged.go"), "package staged\n")
	runTestGit(t, dir, "add", "staged.go")
	writeTestFile(t, filepath.Join(dir, "untracked.go"), "package untracked\n")

	got, err := Collect(t.Context(), []Folder{{Path: dir}})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, path := range []string{"staged.go", "untracked.go"} {
		f := collectFile(t, got.Files, path)
		if len(f.Hunks) == 0 {
			t.Errorf("%s has no hunks; a repository with no commits shows every file as new", path)
		}
	}
}

func TestCollectIgnoresDiffDrivers(t *testing.T) {
	requireGit(t)
	dir := initTestRepo(t)
	writeTestFile(t, filepath.Join(dir, "a.txt"), "one\n")
	writeTestFile(t, filepath.Join(dir, ".gitattributes"), "*.txt diff=evil\n")
	commitTestRepo(t, dir, "initial")

	sentinel := filepath.Join(t.TempDir(), "sentinel")
	script := filepath.Join(t.TempDir(), "evil.sh")
	writeTestFile(t, script, "#!/bin/sh\ntouch "+sentinel+"\ncat \"$1\" 2>/dev/null || true\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, dir, "config", "diff.evil.textconv", script)
	runTestGit(t, dir, "config", "diff.evil.command", script)

	writeTestFile(t, filepath.Join(dir, "a.txt"), "two\n")

	got, err := Collect(t.Context(), []Folder{{Path: dir}})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("the repository's diff driver ran; host git must use --no-ext-diff and --no-textconv")
	}
	f := collectFile(t, got.Files, "a.txt")
	var added []string
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if l.Kind == Added {
				added = append(added, l.Text)
			}
		}
	}
	if !reflect.DeepEqual(added, []string{"two"}) {
		t.Errorf("added lines = %v, want the real text change", added)
	}
}

func TestCollectFolderInsideRepo(t *testing.T) {
	requireGit(t)
	dir := initTestRepo(t)
	writeTestFile(t, filepath.Join(dir, "sub", "a.go"), "package sub\n")
	writeTestFile(t, filepath.Join(dir, "other.go"), "package other\n")
	commitTestRepo(t, dir, "initial")

	writeTestFile(t, filepath.Join(dir, "sub", "a.go"), "package sub\n// changed\n")
	writeTestFile(t, filepath.Join(dir, "other.go"), "package other\n// changed\n")

	got, err := Collect(t.Context(), []Folder{{Path: filepath.Join(dir, "sub")}})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got.Files) != 1 {
		t.Fatalf("got files %v, want only sub/a.go", filePaths(got.Files))
	}
	if got.Files[0].Path != "a.go" {
		t.Errorf("Path = %q, want a.go relative to the folder", got.Files[0].Path)
	}
}

func TestCollectSkipsNonRepository(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.go"), "package a\n")

	got, err := Collect(t.Context(), []Folder{{Path: dir}})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got.Files) != 0 {
		t.Errorf("got files %v, want none", filePaths(got.Files))
	}
	if !reflect.DeepEqual(got.Skipped, []string{dir}) {
		t.Errorf("Skipped = %v, want [%s]", got.Skipped, dir)
	}
}

func TestCollectLabelsFolders(t *testing.T) {
	requireGit(t)
	app := initTestRepo(t)
	lib := initTestRepo(t)
	writeTestFile(t, filepath.Join(app, "a.go"), "package a\n")
	commitTestRepo(t, app, "initial")
	writeTestFile(t, filepath.Join(lib, "l.go"), "package l\n")
	commitTestRepo(t, lib, "initial")
	writeTestFile(t, filepath.Join(app, "a.go"), "package a\n// app\n")
	writeTestFile(t, filepath.Join(lib, "l.go"), "package l\n// lib\n")

	got, err := Collect(t.Context(), []Folder{
		{Label: "app", Path: app},
		{Label: "lib", Path: lib},
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got.Files) != 2 {
		t.Fatalf("got files %v, want two", filePaths(got.Files))
	}
	if got.Files[0].Folder != "app" || got.Files[1].Folder != "lib" {
		t.Errorf("folders = %q/%q, want app/lib", got.Files[0].Folder, got.Files[1].Folder)
	}
}

func TestLabels(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  []Folder
	}{
		{"one folder has no label", []string{"/src/app"}, []Folder{{Path: "/src/app"}}},
		{
			"distinct basenames are the labels",
			[]string{"/src/app", "/src/lib"},
			[]Folder{{Label: "app", Path: "/src/app"}, {Label: "lib", Path: "/src/lib"}},
		},
		{
			"a repeated basename uses the full path",
			[]string{"/one/app", "/two/app", "/src/lib"},
			[]Folder{
				{Label: "/one/app", Path: "/one/app"},
				{Label: "/two/app", Path: "/two/app"},
				{Label: "lib", Path: "/src/lib"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Labels(tc.paths); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Labels(%v) = %+v, want %+v", tc.paths, got, tc.want)
			}
		})
	}
}

// A filter driver from the operator's config is one an agent could select by
// editing .gitattributes, exactly as with a diff driver.
func TestCollectDoesNotRunCleanFilters(t *testing.T) {
	requireGit(t)
	dir := initTestRepo(t)
	writeTestFile(t, filepath.Join(dir, "a.txt"), "one\n")
	writeTestFile(t, filepath.Join(dir, ".gitattributes"), "*.txt filter=evil\n")
	commitTestRepo(t, dir, "initial")

	sentinel := filepath.Join(t.TempDir(), "sentinel")
	script := filepath.Join(t.TempDir(), "evil.sh")
	writeTestFile(t, script, "#!/bin/sh\ntouch "+sentinel+"\ncat \"$1\" 2>/dev/null || true\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"filter.evil.clean", "filter.evil.smudge", "filter.evil.process"} {
		runTestGit(t, dir, "config", key, script)
	}

	writeTestFile(t, filepath.Join(dir, "a.txt"), "two\n")

	got, err := Collect(t.Context(), []Folder{{Path: dir}})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("the repository's clean filter ran on the host")
	}
	f := collectFile(t, got.Files, "a.txt")
	var added []string
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if l.Kind == Added {
				added = append(added, l.Text)
			}
		}
	}
	if !reflect.DeepEqual(added, []string{"two"}) {
		t.Errorf("added lines = %v, want the real text change", added)
	}
}
