// Package teambin carries the compiled in-container dispatch binary. It is kept
// apart from internal/team because embedding cmd/dev-team's output in that
// package would make the build depend on its own product.
package teambin

import (
	"embed"
	"fmt"
	"runtime"
	"strings"
)

// The in-container dispatch binary, built for each architecture and embedded in
// the dev binary. The directory pattern is used because a file pattern matching
// nothing would break `go build ./...` on a fresh checkout, where the binaries
// are not built yet.
//
//go:embed bin
var binFS embed.FS

// Binary returns the dispatch binary built for the given container
// architecture, spelled as `uname -m` reports it.
func Binary(arch string) ([]byte, error) {
	var name string
	switch strings.TrimSpace(arch) {
	case "x86_64", "amd64":
		name = "dev-team-linux-amd64"
	case "aarch64", "arm64":
		name = "dev-team-linux-arm64"
	default:
		return nil, fmt.Errorf("no dev-team binary for architecture %q", arch)
	}

	b, err := binFS.ReadFile("bin/" + name)
	if err != nil {
		// Named here; inside the container it would surface as "exec format
		// error".
		return nil, fmt.Errorf("this build of dev has no dev-team binary for %s; rebuild with: make build", arch)
	}
	return b, nil
}

// HostArch reports the architecture of the machine dev is running on, in the
// spelling Binary takes.
func HostArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	}
	return runtime.GOARCH
}
