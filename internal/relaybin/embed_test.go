package relaybin

import (
	"strings"
	"testing"
)

func TestBinaryArchNames(t *testing.T) {
	// Both spellings: uname -m and Go name the same architectures differently.
	for _, arch := range []string{"x86_64", "amd64", "aarch64", "arm64"} {
		if _, err := Binary(arch); err != nil && !isNotBuilt(err) {
			t.Errorf("Binary(%q): %v", arch, err)
		}
	}
}

func TestBinaryUnknownArch(t *testing.T) {
	_, err := Binary("riscv64")
	if err == nil {
		t.Fatal("expected an error for an unsupported architecture")
	}
	if !strings.Contains(err.Error(), "riscv64") {
		t.Errorf("error should name the architecture, got: %v", err)
	}
}

// TestBinaryIsBuilt checks the embedded relay is an ELF executable. It skips
// when the binaries are not built, as on a fresh checkout.
func TestBinaryIsBuilt(t *testing.T) {
	for _, arch := range []string{"x86_64", "aarch64"} {
		b, err := Binary(arch)
		if err != nil && isNotBuilt(err) {
			t.Skip("relay not built; run make relay")
		}
		if err != nil {
			t.Fatalf("Binary(%q): %v", arch, err)
		}
		// Anything else would only fail inside a container.
		if len(b) < 4 || string(b[:4]) != "\x7fELF" {
			t.Errorf("Binary(%q) is not an ELF executable", arch)
		}
	}
}

// isNotBuilt reports whether the error means the relay binary was not built.
func isNotBuilt(err error) bool {
	return strings.Contains(err.Error(), "no relay binary")
}
