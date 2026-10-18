# Local containers built for the engine's architecture

## Problem

The local provider runs `devcontainer up` with no platform. Docker and podman
then build `FROM` whatever image the local tag currently names, whatever its
architecture.

The k8s provider builds on the same host with `--platform linux/amd64` by
default. That pull moves the shared local tag — for this repository,
`mcr.microsoft.com/devcontainers/base:1-ubuntu-24.04` — onto the amd64 image,
because an engine keeps one image per tag. From then on, every local container
on an arm64 engine is built and run as amd64 under emulation. Nothing fails.
The only signs are podman's warning on the `FROM` step:

```
WARNING: image platform (linux/amd64) does not match the expected platform (linux/arm64)
```

and builds several times slower. On an Apple Silicon Mac, a gopls compile in
the image went from about 4 minutes to 38 seconds once the tag pointed at arm64
again.

Pinning `FROM` to the index digest does not help: a single-architecture pull
records the index digest on the image it pulled, so the amd64 image matches it.
`podman build --pull=newer` does not help either. Only an explicit platform
on the build makes the engine fetch the right variant.

## Goal

A local container is always built and run for the architecture of the engine
it runs on, whatever earlier builds did to the local tags.

Success: on an arm64 engine, with the base tag pointing at an amd64 image,
`dev container create` prints no platform warning and `uname -m` in the
container prints `aarch64`.

## Decisions

- **The platform is the engine's, not dev's.** The engine runs the container.
  With a remote `DOCKER_HOST`, or an Intel Mac driving an arm64 machine,
  `runtime.GOARCH` names the wrong architecture. dev asks the engine:
  `docker version --format '{{.Server.Os}}/{{.Server.Arch}}'`, which prints
  `linux/arm64` on podman machine and on Docker Desktop for Apple Silicon.
- **`DOCKER_DEFAULT_PLATFORM` in the environment of `devcontainer up`, not
  `--platform`.** The devcontainer CLI refuses `--platform` without BuildKit:
  `--platform or --push require BuildKit enabled.` A docker CLI without the
  buildx plugin is common (Homebrew's `docker` formula ships none) and works
  today. The docker CLI honours `DOCKER_DEFAULT_PLATFORM` on `build`, `run` and
  `compose`, with or without buildx. Checked on podman 5.3.1 through the docker
  27.4.1 CLI, with no buildx: the build pulled arm64 and `RUN uname -m` printed
  `aarch64`.
- **One `up`, as today.** A separate `devcontainer build --platform` before
  `up` would need BuildKit, and a second call carrying the same `--config`,
  `--override-config` and no-cache handling. The env var leaves the command line
  alone.
- **An operator's own `DOCKER_DEFAULT_PLATFORM` wins.** If dev's environment
  already has it, dev passes it through untouched and does not probe. That is
  the escape hatch for anyone who wants emulation deliberately.
- **A failed probe changes nothing.** If `docker version` fails or prints
  something that is not `os/arch`, dev runs `up` without the variable, exactly
  as before. An unreachable engine fails `up` a moment later with the CLI's own
  error, which says more than dev could.
- **No setting, no flag.** The right answer is the engine's architecture in
  every case that is not deliberate emulation, and that case has the env var.

## Behaviour

`Provider.up` in `internal/provider/local` serves both `Up` and `Rebuild`.
Before it runs `devcontainer up`:

1. If `DOCKER_DEFAULT_PLATFORM` is set in dev's environment, use the
   environment unchanged.
2. Otherwise run `docker version --format '{{.Server.Os}}/{{.Server.Arch}}'`.
   Trim the output. If the command succeeded and the output has the shape
   `<os>/<arch>` with both parts non-empty, add
   `DOCKER_DEFAULT_PLATFORM=<output>` to the environment of the `devcontainer
   up` child process.
3. Otherwise run `up` with dev's environment unchanged.

The variable goes only on that child process. dev's own environment, and every
other command it runs, stay as they are. `read-configuration`, `exec` and
`run-user-commands` build nothing, so they do not get it.

The k8s provider is unchanged. It passes `--platform` explicitly to
`devcontainer build`, which overrides the variable even if an operator set it.

## Implementation shape

- A function in `internal/provider/local` that takes the current environment
  and returns the environment for `devcontainer up`: either unchanged, or with
  the variable added. It runs the probe through the same `docker` binary the
  provider already drives (`dockerBin`).
- `up` sets `cmd.Env` from it. No other call site changes.
- A short comment at the probe saying why the variable and not `--platform`,
  citing the ARCHITECTURE.md bullet below.

## Testing

Unit tests, with stub `docker` and `devcontainer` executables on a temporary
`PATH`, as in `internal/provider/local/local_test.go`:

- The stub docker answers `version` with `linux/arm64`. The stub devcontainer
  records its environment. Assert `up` saw `DOCKER_DEFAULT_PLATFORM=linux/arm64`.
- With `DOCKER_DEFAULT_PLATFORM=linux/amd64` set by the test, assert
  devcontainer saw `linux/amd64` and the stub docker was never asked `version`.
- The stub docker exits non-zero, then prints garbage: assert devcontainer saw
  no `DOCKER_DEFAULT_PLATFORM` and `up` still ran.
- `Rebuild` gets the variable too.

Smoke test, in `test/smoke`, behind the `smoke` tag:

1. Read the engine's platform with the same `docker version` command and pick
   the other one of `linux/amd64` and `linux/arm64`.
2. `docker pull --platform <other> alpine:3.20`, so the local tag names the
   wrong architecture.
3. Create a container from a project whose Dockerfile is
   `FROM alpine:3.20` and `RUN uname -m > /arch`.
4. Assert `/arch` in the container names the engine's architecture
   (`x86_64` for amd64, `aarch64` for arm64).
5. Clean up: remove the container, then pull `alpine:3.20` again for the
   engine's platform.

CI runs smoke on `ubuntu-latest` and `ubuntu-24.04-arm`. Neither registers
qemu, so without the fix step 3 fails with `exec format error` rather than
passing slowly — a clear regression signal on both. The podman behaviour above
is verified; Docker Engine with buildx honouring the variable on a
mismatched local tag is not yet, and the CI run is that check.

## Documentation

- `docs/ARCHITECTURE.md`, under *Tooling and testing, in detail*: one bullet
  saying the local provider sets `DOCKER_DEFAULT_PLATFORM` from the engine's
  own platform on `up`, why not `--platform` (BuildKit), and why the engine's
  architecture rather than dev's.
- `docs/USAGE.md`, troubleshooting: an entry for the podman `FROM` warning
  and an `x86_64` container on Apple Silicon. dev now prevents it; an operator
  who set `DOCKER_DEFAULT_PLATFORM` themselves gets what they asked for.

## Out of scope

- Platform variants such as `linux/arm/v7`. `docker version` reports no
  variant, and no engine dev targets needs one.
- Restoring the local tag after a k8s cross-build. It would mean reading base
  images out of Dockerfiles, which is the devcontainer-spec parsing the
  conventions rule out, and the local fix makes it unnecessary.
- A provider setting for the local platform.
