package xpath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveFollowsSymlinks(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	// The whole point: a bind mount built from the symlink path resolves to a
	// different directory inside the engine's VM.
	got, err := Resolve(link)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if got != want {
		t.Errorf("Resolve(%q) = %q, want %q", link, got, want)
	}
}

func TestResolveRejectsNonDirectories(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := Resolve(file); err == nil {
		t.Error("Resolve on a file succeeded, want an error")
	}
	if _, err := Resolve(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("Resolve on a missing path succeeded, want an error")
	}
	if _, err := Resolve(""); err == nil {
		t.Error("Resolve on an empty path succeeded, want an error")
	}
}

func TestValidateName(t *testing.T) {
	ok := []string{"demo", "demo-1", "demo_1", "demo.1", "A", "0"}
	for _, n := range ok {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}

	bad := []string{"", "has space", "slash/es", "colon:s", "quote'", "emoji🙂", "star*"}
	for _, n := range bad {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) = nil, want an error", n)
		}
	}
}

func TestValidateEnvKey(t *testing.T) {
	ok := []string{"TOKEN", "GH_TOKEN", "_private", "a1"}
	for _, k := range ok {
		if err := ValidateEnvKey(k); err != nil {
			t.Errorf("ValidateEnvKey(%q) = %v, want nil", k, err)
		}
	}

	bad := []string{"", "1TOKEN", "GH-TOKEN", "GH TOKEN", "GH=TOKEN", "GH.TOKEN"}
	for _, k := range bad {
		if err := ValidateEnvKey(k); err == nil {
			t.Errorf("ValidateEnvKey(%q) = nil, want an error", k)
		}
	}
}

func TestShorten(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory in this environment")
	}

	cases := map[string]string{
		home:                              "~",
		filepath.Join(home, "code", "x"):  filepath.Join("~", "code", "x"),
		"/opt/elsewhere":                  "/opt/elsewhere",
		home + "-not-really-in-home/here": home + "-not-really-in-home/here",
	}
	for in, want := range cases {
		if got := Shorten(in); got != want {
			t.Errorf("Shorten(%q) = %q, want %q", in, got, want)
		}
	}
}
