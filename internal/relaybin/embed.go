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
	"embed"
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
// A directory rather than one //go:embed per file, because the binaries are
// build output and are not committed. A file pattern that matches nothing is a
// compile error, so naming them directly would break `go build ./...` on a
// fresh checkout; a directory pattern is satisfied by the README beside them,
// and a missing binary becomes a runtime error this package can explain.
//
//go:embed bin
var binFS embed.FS

// Binary returns the relay built for the given container architecture.
//
// arch is as `uname -m` reports it, because that is what is available over an
// exec channel into a container that might not have Go's naming anywhere.
func Binary(arch string) ([]byte, error) {
	var name string
	switch strings.TrimSpace(arch) {
	case "x86_64", "amd64":
		name = "relay-linux-amd64"
	case "aarch64", "arm64":
		name = "relay-linux-arm64"
	default:
		return nil, fmt.Errorf("no relay for architecture %q", arch)
	}

	b, err := binFS.ReadFile("bin/" + name)
	if err != nil {
		// This `dev` was built without `make relay`. Caught here, naming the
		// fix, rather than in the container as "exec format error" or "cannot
		// execute binary file".
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
