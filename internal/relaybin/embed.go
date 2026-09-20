// Package relaybin carries the compiled in-container relay.
//
// Separate from internal/relay, which holds the logic, because cmd/dev-relay
// imports that logic and this package embeds the binary built from
// cmd/dev-relay. In one package that is a cycle through the build: `make relay`
// could not produce the file that the package being compiled to produce it
// requires. Split, nothing on the path to building the relay depends on the
// embedded copy.
package relaybin

import (
	_ "embed"
	"fmt"
	"runtime"
	"strings"
)

// The in-container relay, built for each architecture a container might run on
// and carried inside the `dev` binary.
//
// Embedded rather than downloaded or installed: the relay has to be in the
// container before anything works, and a `dev` that fetched it at runtime would
// fail on an air-gapped host and would need somewhere trusted to fetch from.
//
// The real files come from `make relay`. The placeholders committed beside this
// file keep `go build ./...` working before it has been run, and produce a
// clear error rather than a corrupt binary if one reaches a container.
//
//go:embed bin/relay-linux-amd64
var relayAMD64 []byte

//go:embed bin/relay-linux-arm64
var relayARM64 []byte

// placeholder marks an unbuilt embed. Kept short and unlikely to collide with
// the first bytes of a real ELF binary, which begin \x7fELF.
const placeholder = "placeholder: run make relay\n"

// Binary returns the relay built for the given container architecture.
//
// arch is as `uname -m` reports it, because that is what is available over an
// exec channel into a container that might not have Go's naming anywhere.
func Binary(arch string) ([]byte, error) {
	var b []byte
	switch strings.TrimSpace(arch) {
	case "x86_64", "amd64":
		b = relayAMD64
	case "aarch64", "arm64":
		b = relayARM64
	default:
		return nil, fmt.Errorf("no relay for architecture %q", arch)
	}

	// A placeholder means this `dev` was built without `make relay`. Caught
	// here, naming the fix, rather than in the container as "exec format
	// error" or "cannot execute binary file".
	if strings.HasPrefix(string(b), placeholder) {
		return nil, fmt.Errorf("this build of dev has no relay binary for %s; rebuild with: make build", arch)
	}
	return b, nil
}

// HostArch reports the architecture of the machine dev is running on, in the
// same spelling Binary takes. Used by tests and as the fallback when a
// container cannot be asked.
func HostArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	}
	return runtime.GOARCH
}
