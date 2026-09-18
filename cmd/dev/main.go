// Command dev manages devcontainers and the coding agents that run inside them.
package main

import (
	"os"

	"github.com/duy0611/dev-cli/internal/cli"
)

// Set by the linker: `go build -ldflags "-X main.version=..."`. See the Makefile.
var version = "dev"

func main() {
	os.Exit(cli.Execute(version))
}
