package dcconfig

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestFindDotDevcontainerDirectory(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, ".devcontainer", "devcontainer.json")
	writeFile(t, want)

	got, err := Find(dir)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got != want {
		t.Errorf("Find = %q, want %q", got, want)
	}
}

func TestFindRootLevelFile(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, ".devcontainer.json")
	writeFile(t, want)

	got, err := Find(dir)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got != want {
		t.Errorf("Find = %q, want %q", got, want)
	}
}

func TestFindPrefersTheDirectoryForm(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, ".devcontainer", "devcontainer.json")
	writeFile(t, nested)
	writeFile(t, filepath.Join(dir, ".devcontainer.json"))

	got, err := Find(dir)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got != nested {
		t.Errorf("Find = %q, want the .devcontainer/ form %q", got, nested)
	}
}

func TestFindMissingIsAnError(t *testing.T) {
	_, err := Find(t.TempDir())
	if !errors.Is(err, ErrNoConfig) {
		t.Errorf("Find on a bare directory: err = %v, want ErrNoConfig", err)
	}
}

// A project one level up is somebody else's project. The folder argument is
// explicit here, so searching above it would guess at what the operator meant.
func TestFindDoesNotWalkUp(t *testing.T) {
	parent := t.TempDir()
	writeFile(t, filepath.Join(parent, ".devcontainer", "devcontainer.json"))

	child := filepath.Join(parent, "sub")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	if _, err := Find(child); !errors.Is(err, ErrNoConfig) {
		t.Errorf("Find in a subdirectory: err = %v, want ErrNoConfig", err)
	}
}

// A directory named devcontainer.json is not a config file.
func TestFindIgnoresDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".devcontainer", "devcontainer.json"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if _, err := Find(dir); !errors.Is(err, ErrNoConfig) {
		t.Errorf("Find with a directory in the config's place: err = %v, want ErrNoConfig", err)
	}
}
