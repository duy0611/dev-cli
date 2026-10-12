// Package relay carries the host's SSH agent into a container. The agent signs
// on request and never hands the key over, so the container holds no key
// material.
//
// The transport is the provider's exec channel, not a bind mount of the agent
// socket: the macOS engine does not expose the real socket to mounts, and
// podman and colima expose none.
//
// The in-container half is a compiled binary embedded per architecture (see
// embed.go). The base image has no socat or nc, and python3 is not guaranteed.
//
// Nothing here parses the agent protocol; both halves copy bytes.
package relay
