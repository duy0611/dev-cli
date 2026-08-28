# devcontainer-claude-setup

Tooling for running Claude Code inside containers on macOS + Podman.

Two ways in, one command family:

- A project that ships a `.devcontainer/` is used as-is.
- A project that doesn't gets a **prebuilt sandbox image** chosen by profile
  (`base`, `k8s`, `gcp`, `full`), with short-lived, narrowly scoped cloud
  credentials minted on the host at launch.

Commands: `dcx` (shell/command), `dcclaude` (Claude Code), `dcws` (Herdr
workspace), `dccred` (credential lifecycle).

## Status

Design only. No code yet.

See [docs/design.md](docs/design.md) for the full design: architecture,
credential model, container invariants, and the verification plan.
