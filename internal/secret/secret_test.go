package secret

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stub installs an executable named name on a temp PATH.
func stub(t *testing.T, name, script string) {
	t.Helper()

	dir := os.Getenv("DEV_TEST_STUBDIR")
	if dir == "" {
		dir = t.TempDir()
		t.Setenv("DEV_TEST_STUBDIR", dir)
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("writing stub %s: %v", name, err)
	}
}

// emptyPath makes every external backend unavailable.
func emptyPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

func TestResolveLiteral(t *testing.T) {
	r := NewResolver()
	got, err := r.Resolve(context.Background(), "literal:https://example.invalid")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "https://example.invalid" {
		t.Errorf("Resolve = %q, want %q", got, "https://example.invalid")
	}
}

// A literal with a colon in it must survive: URLs are the common case.
func TestResolveLiteralKeepsColons(t *testing.T) {
	r := NewResolver()
	got, err := r.Resolve(context.Background(), "literal:host:8080/path?a=b:c")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "host:8080/path?a=b:c" {
		t.Errorf("Resolve = %q, want the reference verbatim", got)
	}
}

func TestResolveKeychain(t *testing.T) {
	// argv is: find-generic-password -a ACCOUNT -s SERVICE -w
	stub(t, "security", `printf '%s\n' "tok-for-$5-as-$3"`)
	t.Setenv("USER", "tester")

	got, err := NewResolver().Resolve(context.Background(), "keychain:MY_TOKEN")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Checks three things at once: the service name reached -s, the account
	// defaulted to $USER, and the trailing newline was trimmed.
	if got != "tok-for-MY_TOKEN-as-tester" {
		t.Errorf("Resolve = %q, want %q", got, "tok-for-MY_TOKEN-as-tester")
	}
}

func TestResolveOnePasswordKeepsTheWholeURI(t *testing.T) {
	stub(t, "op", `printf '%s' "read:$3"`)

	got, err := NewResolver().Resolve(context.Background(), "op://Private/gh/token")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// $3 is the URI argument: op:// is a URI, not a scheme:ref pair, so the
	// whole spec has to reach the CLI unchanged.
	if got != "read:op://Private/gh/token" {
		t.Errorf("Resolve = %q, want the full URI passed through", got)
	}
}

func TestResolveReportsTheToolsOwnError(t *testing.T) {
	stub(t, "security", `echo "security: SecKeychainSearchCopyNext: not found" >&2; exit 44`)
	t.Setenv("USER", "tester")

	_, err := NewResolver().Resolve(context.Background(), "keychain:MISSING")
	if err == nil {
		t.Fatal("Resolve of a missing key succeeded")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q dropped the tool's own explanation", err)
	}
}

func TestResolveRejectsEmptyValues(t *testing.T) {
	stub(t, "security", `printf '\n'`)
	t.Setenv("USER", "tester")

	_, err := NewResolver().Resolve(context.Background(), "keychain:BLANK")
	if err == nil {
		t.Fatal("an empty value resolved successfully; it fails far away from here instead")
	}
}

func TestResolveRejectsBadSpecs(t *testing.T) {
	r := NewResolver()
	for _, spec := range []string{"", "   ", "bare", "literal:", "nosuch:thing"} {
		if _, err := r.Resolve(context.Background(), spec); err == nil {
			t.Errorf("Resolve(%q) succeeded, want an error", spec)
		}
	}
}

func TestUnavailableBackendIsNamed(t *testing.T) {
	emptyPath(t)

	_, err := NewResolver().Resolve(context.Background(), "keychain:X")
	if err == nil {
		t.Fatal("Resolve succeeded with no security binary")
	}
	if !strings.Contains(err.Error(), "keychain") {
		t.Errorf("error %q does not say which backend is unavailable", err)
	}
}
