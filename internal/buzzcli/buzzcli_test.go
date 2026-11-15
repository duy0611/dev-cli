package buzzcli

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeELF(t *testing.T, class byte, machine uint16) string {
	t.Helper()
	h := make([]byte, 64)
	copy(h, "\x7fELF")
	h[4] = class
	h[5] = 1
	binary.LittleEndian.PutUint16(h[18:], machine)
	path := filepath.Join(t.TempDir(), "buzz")
	if err := os.WriteFile(path, h, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckELFAcceptsTheMatchingArchitecture(t *testing.T) {
	if err := CheckELF(writeELF(t, 2, 183), "arm64"); err != nil {
		t.Errorf("arm64 ELF refused for arm64: %v", err)
	}
	if err := CheckELF(writeELF(t, 2, 62), "amd64"); err != nil {
		t.Errorf("x86-64 ELF refused for amd64: %v", err)
	}
}

func TestCheckELFRefusesAnythingElse(t *testing.T) {
	notELF := filepath.Join(t.TempDir(), "buzz")
	if err := os.WriteFile(notELF, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"wrong arch": writeELF(t, 2, 62),
		"32-bit":     writeELF(t, 1, 183),
		"not an ELF": notELF,
		"missing":    filepath.Join(t.TempDir(), "nope"),
	}
	for name, path := range cases {
		err := CheckELF(path, "arm64")
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("%s: error %q does not name the file", name, err)
		}
	}
	if err := CheckELF(writeELF(t, 2, 183), "riscv"); err == nil {
		t.Error("an unknown architecture was accepted")
	}
}

func TestMountEntryIsAReadOnlyBindAtTarget(t *testing.T) {
	got := MountEntry("/h/buzz")
	want := "source=/h/buzz,target=/usr/local/bin/buzz,type=bind,readonly"
	if got != want {
		t.Errorf("MountEntry = %q, want %q", got, want)
	}
}
