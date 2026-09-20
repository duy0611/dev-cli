# Relay binaries

`make relay` writes `relay-linux-amd64` and `relay-linux-arm64` here, and
`embed.go` carries them inside `dev`. They are build output and are not
committed — `make build` depends on `relay`, so a real build always has them.

This file is committed and must stay. `embed.go` embeds the *directory*, and an
embed pattern that matches nothing is a compile error: without something here,
`go build ./...` would fail on a fresh checkout before `make relay` had a chance
to run.
