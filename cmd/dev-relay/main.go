// Command dev-relay is the in-container half of the SSH agent relay.
//
// It is not installed or run by hand: `dev` embeds a build of it per
// architecture and streams the right one into the container for the length of a
// session. See internal/relay.
//
// Its own binary because the base image has no socat, nc or ncat to lean on,
// and depending on python3 would fail on a project whose devcontainer is alpine
// or distroless. Keep it dependency-free so it stays small and runs anywhere:
// the standard library only, and no import of the rest of this module beyond
// internal/relay.
package main

import (
	"fmt"
	"os"

	"github.com/duy0611/dev-cli/internal/relay"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: dev-relay SOCKET_PATH")
		os.Exit(2)
	}

	// stdin and stdout are the exec channel back to the host. Everything human
	// goes to stderr, or it would be parsed as frames.
	if err := relay.Serve(os.Args[1], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "dev-relay: %v\n", err)
		os.Exit(1)
	}
}
