package env

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	want := map[string]string{
		"GIT_AUTHOR_NAME":     "Ada",
		"GIT_AUTHOR_EMAIL":    "ada@example.invalid",
		"GIT_COMMITTER_NAME":  "Ada",
		"GIT_COMMITTER_EMAIL": "ada@example.invalid",
	}
	for key, w := range want {
		if v, ok := find(got, key); !ok || v != w {
			t.Errorf("%s = %q (present=%v), want %q", key, v, ok, w)
		}
	}
	for _, e := range got {
		if strings.HasPrefix(e.Key, "DEVCONTAINER_GIT_") {
			t.Errorf("%s is set; git does not read it", e.Key)
		}
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

// A workspace setting wins over the host identity.
func TestWorkspaceSettingOverridesGitIdentity(t *testing.T) {
	fakeGit(t, "Ada", "ada@example.invalid")

	got, err := Assemble(context.Background(), secret.NewResolver(), []model.Setting{
		{Key: "GIT_AUTHOR_EMAIL", Spec: "literal:bot@example.invalid"},
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	seen := 0
	for _, e := range got {
		if e.Key == "GIT_AUTHOR_EMAIL" {
			seen++
			if e.Value != "bot@example.invalid" {
				t.Errorf("GIT_AUTHOR_EMAIL = %q, want the workspace's value", e.Value)
			}
		}
	}
	if seen != 1 {
		t.Errorf("GIT_AUTHOR_EMAIL appears %d times, want exactly 1", seen)
	}
}

// Missing git is not an error.
func TestAssembleWithoutGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	got, err := Assemble(context.Background(), secret.NewResolver(), []model.Setting{
		{Key: "BASE_URL", Spec: "literal:x"},
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if _, ok := find(got, "GIT_AUTHOR_NAME"); ok {
		t.Error("GIT_AUTHOR_NAME was set with no git on PATH")
	}
	if v, _ := find(got, "BASE_URL"); v != "x" {
		t.Errorf("BASE_URL = %q, want %q", v, "x")
	}
}

// An unresolvable setting stops the launch.
func TestAssembleFailsOnUnresolvableSetting(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := Assemble(context.Background(), secret.NewResolver(), []model.Setting{
		{Key: "TOKEN", Spec: "keychain:nope"},
	})
	if err == nil {
		t.Fatal("Assemble succeeded with an unresolvable setting")
	}
}
