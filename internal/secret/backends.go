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
	schemeKeyring     = "keyring"
	schemeOnePassword = "op"
)

// --- literal ------------------------------------------------------------------

// literalBackend passes the reference through unchanged, for values that are
// not secrets.
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

// Get reads the generic password stored under service name ref, for the account
// `security add-generic-password -a "$USER"` writes.
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
	// -w appends a newline; trim only that, never the value's own spaces.
	return strings.Trim(out, "\r\n"), nil
}

// --- freedesktop Secret Service -----------------------------------------------

// keyringBackend reads a secret from the freedesktop Secret Service, which
// Ubuntu's GNOME Keyring and KWallet both expose through secret-tool.
type keyringBackend struct{}

func (keyringBackend) Scheme() string { return schemeKeyring }

func (keyringBackend) Available(context.Context) bool {
	_, err := exec.LookPath("secret-tool")
	return err == nil
}

// Get reads the secret stored under the `service` attribute ref, matching what
// `secret-tool store --label=SERVICE service SERVICE` writes.
func (keyringBackend) Get(ctx context.Context, ref string) (string, error) {
	out, err := run(ctx, "secret-tool", "lookup", "service", ref)
	if err != nil {
		return "", fmt.Errorf("keyring lookup of %q failed: %w", ref, err)
	}
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

// Get resolves a full op:// URI; `op read` also handles any unlock prompt.
func (onePasswordBackend) Get(ctx context.Context, ref string) (string, error) {
	out, err := run(ctx, "op", "read", "--no-newline", ref)
	if err != nil {
		return "", fmt.Errorf("1Password lookup of %q failed: %w", ref, err)
	}
	return strings.Trim(out, "\r\n"), nil
}

// --- shared ----------------------------------------------------------------------

// run returns a lookup's stdout. Stderr is surfaced only on failure, where both
// tools explain a miss; the value is only ever on stdout.
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
