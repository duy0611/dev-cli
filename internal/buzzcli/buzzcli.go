// Package buzzcli puts a host-built buzz CLI into a container as one read-only
// file mount.
package buzzcli

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// Target is where the binary appears in the container.
const Target = "/usr/local/bin/buzz"

// Arches are the container architectures a binary can be named for, spelled
// as Go and the engine spell them.
var Arches = []string{"arm64", "amd64"}

const (
	emX8664   = 62
	emAArch64 = 183
)

// CheckELF reports whether path is a 64-bit ELF image for arch ("arm64" or
// "amd64"). The error names the file.
func CheckELF(path, arch string) error {
	var machine uint16
	switch arch {
	case "arm64":
		machine = emAArch64
	case "amd64":
		machine = emX8664
	default:
		return fmt.Errorf("%s: unsupported architecture %q", path, arch)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("the buzz binary for linux/%s: %w", arch, err)
	}
	defer f.Close()

	hdr := make([]byte, 20)
	if _, err := io.ReadFull(f, hdr); err != nil || string(hdr[:4]) != "\x7fELF" {
		return fmt.Errorf("the buzz binary for linux/%s, %s, is not an ELF executable", arch, path)
	}
	// hdr[5] is the byte order; e_machine is read in it.
	var order binary.ByteOrder = binary.LittleEndian
	if hdr[5] == 2 {
		order = binary.BigEndian
	}
	if hdr[4] != 2 || order.Uint16(hdr[18:]) != machine {
		return fmt.Errorf("the buzz binary for linux/%s, %s, is not a 64-bit Linux executable for %s", arch, path, arch)
	}
	return nil
}

// MountEntry is the mounts entry that binds path read-only at Target.
func MountEntry(path string) string {
	return "source=" + path + ",target=" + Target + ",type=bind,readonly"
}
