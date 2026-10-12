// Command dev-relay is the in-container half of the SSH agent relay. dev embeds
// a build of it per architecture and streams it into the container; see
// internal/relay. It must stay dependency-free, since the base image has no
// socat or nc and python3 is not guaranteed.
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

	// stdout carries frames, so nothing else may be written to it.
	if err := relay.Serve(os.Args[1], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "dev-relay: %v\n", err)
		os.Exit(1)
	}
}
