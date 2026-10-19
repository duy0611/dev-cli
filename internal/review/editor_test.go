package review

import (
	"os"
	"path/filepath"
	"testing"
)

// stubEditor writes a shell script that records the name it was invoked under
// into the file it is given as its last argument.
func stubEditor(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\nfor last; do :; done\nprintf '%s' " + name + " > \"$last\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenEditorPrefersVisual(t *testing.T) {
	visual := stubEditor(t, "visual")
	editor := stubEditor(t, "editor")
	path := filepath.Join(t.TempDir(), "review.diff")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("VISUAL", visual)
	t.Setenv("EDITOR", editor)
	if err := OpenEditor(t.Context(), path); err != nil {
		t.Fatalf("OpenEditor: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "visual" {
		t.Errorf("file = %q, want VISUAL to have run", b)
	}

	t.Setenv("VISUAL", "")
	if err := OpenEditor(t.Context(), path); err != nil {
		t.Fatalf("OpenEditor: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "editor" {
		t.Errorf("file = %q, want EDITOR to have run", b)
	}
}

func TestOpenEditorEditorWithArgument(t *testing.T) {
	editor := stubEditor(t, "editor")
	path := filepath.Join(t.TempDir(), "review.diff")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", editor+" --wait")
	if err := OpenEditor(t.Context(), path); err != nil {
		t.Fatalf("OpenEditor: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "editor" {
		t.Errorf("file = %q, want the editor to have run", b)
	}
}

func TestOpenEditorReportsAFailedEditor(t *testing.T) {
	dir := t.TempDir()
	editor := filepath.Join(dir, "fail")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", editor)

	if err := OpenEditor(t.Context(), filepath.Join(dir, "review.diff")); err == nil {
		t.Error("a failing editor was not reported")
	}
}
