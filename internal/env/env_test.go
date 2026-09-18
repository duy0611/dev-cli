package env

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/secret"
)

// fakeGit puts a git stub on PATH that answers user.name and user.email.
func fakeGit(t *testing.T, name, email string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"case \"$3\" in\n" +
		"  user.name)  printf '%s\\n' '" + name + "' ;;\n" +
		"  user.email) printf '%s\\n' '" + email + "' ;;\n" +
		"  *) exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing git stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func find(env []provider.EnvVar, key string) (string, bool) {
	for _, e := range env {
		if e.Key == key {
			return e.Value, true
		}
	}
	return "", false
}

func TestAssembleAddsGitIdentity(t *testing.T) {
	fakeGit(t, "Ada", "ada@example.invalid")

	got, err := Assemble(context.Background(), secret.NewResolver(), nil)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if v, ok := find(got, gitNameKey); !ok || v != "Ada" {
		t.Errorf("%s = %q (present=%v), want %q", gitNameKey, v, ok, "Ada")
	}
	if v, ok := find(got, gitEmailKey); !ok || v != "ada@example.invalid" {
		t.Errorf("%s = %q (present=%v), want %q", gitEmailKey, v, ok, "ada@example.invalid")
	}
}

func TestAssembleResolvesSettings(t *testing.T) {
	fakeGit(t, "Ada", "ada@example.invalid")

	got, err := Assemble(context.Background(), secret.NewResolver(), []model.Setting{
		{Key: "BASE_URL", Spec: "literal:https://example.invalid"},
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if v, _ := find(got, "BASE_URL"); v != "https://example.invalid" {
		t.Errorf("BASE_URL = %q, want the resolved literal", v)
	}
}

// An explicit setting has to win over the identity read off the host, or there
// is no way to commit as someone else from inside a container.
func TestWorkspaceSettingOverridesGitIdentity(t *testing.T) {
	fakeGit(t, "Ada", "ada@example.invalid")

	got, err := Assemble(context.Background(), secret.NewResolver(), []model.Setting{
		{Key: gitEmailKey, Spec: "literal:bot@example.invalid"},
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	seen := 0
	for _, e := range got {
		if e.Key == gitEmailKey {
			seen++
			if e.Value != "bot@example.invalid" {
				t.Errorf("%s = %q, want the workspace's value", gitEmailKey, e.Value)
			}
		}
	}
	if seen != 1 {
		t.Errorf("%s appears %d times, want exactly 1", gitEmailKey, seen)
	}
}

// No git on PATH is a normal state for a container start, not a reason to fail.
func TestAssembleWithoutGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	got, err := Assemble(context.Background(), secret.NewResolver(), []model.Setting{
		{Key: "BASE_URL", Spec: "literal:x"},
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if _, ok := find(got, gitNameKey); ok {
		t.Errorf("%s was set with no git on PATH", gitNameKey)
	}
	if v, _ := find(got, "BASE_URL"); v != "x" {
		t.Errorf("BASE_URL = %q, want %q", v, "x")
	}
}

// A setting that cannot be resolved has to stop the launch: starting without it
// produces a failure inside the container that is much harder to read.
func TestAssembleFailsOnUnresolvableSetting(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := Assemble(context.Background(), secret.NewResolver(), []model.Setting{
		{Key: "TOKEN", Spec: "keychain:nope"},
	})
	if err == nil {
		t.Fatal("Assemble succeeded with an unresolvable setting")
	}
}
