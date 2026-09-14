package secret

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	schemeLiteral     = "literal"
	schemeKeychain    = "keychain"
	schemeOnePassword = "op"
)

// --- literal ------------------------------------------------------------------

// literalBackend passes the reference through unchanged. For values that are
// not secrets — a base URL, a feature flag — where a keychain entry would be
// ceremony for nothing.
type literalBackend struct{}

func (literalBackend) Scheme() string                 { return schemeLiteral }
func (literalBackend) Available(context.Context) bool { return true }
func (literalBackend) Get(_ context.Context, ref string) (string, error) {
	return ref, nil
}

// --- macOS Keychain -------------------------------------------------------------

// keychainBackend reads a generic password from the macOS Keychain.
type keychainBackend struct{}

func (keychainBackend) Scheme() string { return schemeKeychain }

func (keychainBackend) Available(context.Context) bool {
	_, err := exec.LookPath("security")
	return err == nil
}

// Get reads the generic password stored for the current user under the service
// name ref.
//
// Account defaults to $USER, which is what `security add-generic-password -a
// "$USER" -s NAME -w` writes, so a key added the documented way is found
// without any further configuration.
func (keychainBackend) Get(ctx context.Context, ref string) (string, error) {
	account := os.Getenv("USER")
	if account == "" {
		account = os.Getenv("LOGNAME")
	}

	out, err := run(ctx, "security",
		"find-generic-password", "-a", account, "-s", ref, "-w")
	if err != nil {
		return "", fmt.Errorf("keychain lookup of %q failed: %w", ref, err)
	}
	// -w prints the password followed by a newline. Trim only the line ending:
	// a token with leading or trailing spaces is unusual but not ours to fix.
	return strings.Trim(out, "\r\n"), nil
}

// --- 1Password -------------------------------------------------------------------

// onePasswordBackend reads a field through the 1Password CLI.
type onePasswordBackend struct{}

func (onePasswordBackend) Scheme() string { return schemeOnePassword }

func (onePasswordBackend) Available(context.Context) bool {
	_, err := exec.LookPath("op")
	return err == nil
}

// Get resolves a full op:// URI. `op read` handles the vault, item and field
// lookup, and prompts for unlock on its own when the session has expired.
func (onePasswordBackend) Get(ctx context.Context, ref string) (string, error) {
	out, err := run(ctx, "op", "read", "--no-newline", ref)
	if err != nil {
		return "", fmt.Errorf("1Password lookup of %q failed: %w", ref, err)
	}
	return strings.Trim(out, "\r\n"), nil
}

// --- shared ----------------------------------------------------------------------

// run executes a lookup and returns its stdout.
//
// Stderr is captured and quoted on failure but never on success: it is where
// both tools explain a miss ("could not be found in the keychain"), and
// dropping it would leave the operator with a bare exit status. The value
// itself is only ever on stdout, so it cannot leak this way.
func run(ctx context.Context, name string, args ...string) (string, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%s", msg)
		}
		return "", err
	}
	return string(out), nil
}
