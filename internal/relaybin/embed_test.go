package relaybin

import (
	"strings"
	"testing"
)

func TestBinaryArchNames(t *testing.T) {
	// Both spellings, because `uname -m` says x86_64 and aarch64 while Go says
	// amd64 and arm64, and the arch reaches this code from whichever of the two
	// the caller had to hand.
	for _, arch := range []string{"x86_64", "amd64", "aarch64", "arm64"} {
		if _, err := Binary(arch); err != nil && !isPlaceholder(err) {
			t.Errorf("Binary(%q): %v", arch, err)
		}
	}
}

func TestBinaryUnknownArch(t *testing.T) {
	_, err := Binary("riscv64")
	if err == nil {
		t.Fatal("expected an error for an unsupported architecture")
	}
	// Named, because the alternative is an exec format error inside a container
	// that says nothing about which architecture was wanted.
	if !strings.Contains(err.Error(), "riscv64") {
		t.Errorf("error should name the architecture, got: %v", err)
	}
}

// TestBinaryIsBuilt fails when the embedded relay is still the committed
// placeholder, which means `make relay` has not run. Skipped rather than
// failed, so `go test ./...` works on a fresh checkout; `make build` depends on
// `relay`, so a real build always has them.
func TestBinaryIsBuilt(t *testing.T) {
	for _, arch := range []string{"x86_64", "aarch64"} {
		b, err := Binary(arch)
		if err != nil && isPlaceholder(err) {
			t.Skip("relay not built; run make relay")
		}
		if err != nil {
			t.Fatalf("Binary(%q): %v", arch, err)
		}
		// ELF magic. A build that silently produced something else — a shell
		// script, an error page — would otherwise only fail in a container.
		if len(b) < 4 || string(b[:4]) != "\x7fELF" {
			t.Errorf("Binary(%q) is not an ELF executable", arch)
		}
	}
}

func isPlaceholder(err error) bool {
	return strings.Contains(err.Error(), "no relay binary")
}
