// Package relay carries the host's SSH agent into a container without putting
// anything in the container that is worth stealing.
//
// An SSH agent signs on request and never hands the key over, so reaching the
// operator's agent from inside a container is strictly better than copying a
// key or a token in: a compromised container can ask for signatures while the
// session lasts, and keeps nothing once it ends.
//
// The transport is the provider's own exec channel rather than a bind mount of
// the agent socket. Mounting looks simpler and is not: the macOS engine only
// exposes a synthesised /run/host-services/ssh-auth.sock and refuses to mount
// the real one, podman and colima expose neither, the mounted socket arrives
// owned by root against a container running as a non-root user, and a mount is
// fixed at creation so the setting could not be changed without a rebuild. A
// relay over exec has none of those problems and works on Kubernetes too, where
// there is no host socket to mount at all. It is also what the VS Code Dev
// Containers extension does — its logs forward a container socket to a *Windows
// named pipe*, which no bind mount could ever do.
//
// The in-container half has to be a compiled binary. The base image ships no
// socat, nc or ncat, so there is no shell one-liner to lean on, and depending
// on python3 would work here but fail on any project whose own devcontainer is
// alpine or distroless. So a small program is embedded for each supported
// architecture and streamed in per session; see embed.go.
//
// Nothing here parses the agent protocol. It is request-response over a stream,
// and both halves only copy bytes.
package relay
