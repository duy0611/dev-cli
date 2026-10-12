# AGENTS.md

Working rules for coding agents in this repository. The reasoning behind them —
the invariants the code depends on, the Kubernetes provider's sharp edges, CI
and releases — is in `docs/ARCHITECTURE.md`. Read it before changing anything
under `internal/provider`, `internal/store`, `internal/gitguard` or the mount
handling in `internal/cli`.

## What this is

`dev`, a Go CLI that manages devcontainers and runs coding agents inside them.
One binary, a SQLite database, and the binaries it shells out to: `devcontainer`
and `docker` always, `kubectl` for the k8s provider and for `--kube-token`. One
operator. Module `github.com/duy0611/dev-cli`; version in
`.release-please-manifest.json`.

- `docs/specs/` is the design record: read it for intent, the code for current
  state.
- `docs/USAGE.md` is the operator reference. A change to a command's surface is
  not finished until that file matches it.
- `docs/SECURITY.md` explains the guarded and isolated modes to a newcomer.
- The k8s provider is **experimental**: its settings, objects and lifecycle may
  change without a migration. The local provider is the stable one; say so
  wherever the two appear side by side.

## Commands

```sh
make            # help
make build      # dist/dev
make test       # go test ./... — never touches a container engine
make lint       # gofmt (fails on any diff), go vet, golangci-lint
make smoke      # go test -tags smoke ./test/smoke/... — needs a real engine
make install    # build, then copy to ~/.local/bin/dev
make hooks      # copy .githooks into .git/hooks — once per clone
```

- `make lint && make test` is the fast loop; run it before every commit.
- `make smoke` needs the `devcontainer` CLI, an engine and network, and skips
  without them. Its Kubernetes half also needs `DEV_SMOKE_K8S_CONTEXT` and
  `DEV_SMOKE_REGISTRY`.
- `DEV_STATE=$(mktemp -d) dist/dev …` runs a command without touching the
  operator's own database.
- With no engine where you are, push and read the CI smoke logs
  (`gh api repos/duy0611/dev-cli/actions/jobs/<id>/logs`), or ask the operator
  to run `make smoke` on their Mac — macOS-only behaviour (podman machine,
  case-insensitive disks) is visible nowhere else.

## Layout

```
cmd/dev/              main; exits with the code cli.Execute returns
cmd/dev-relay/        the in-container half of the ssh agent relay
internal/cli/         one file per noun; arg parsing and orchestration only
internal/store/       SQLite, all persistence; schema in migrations/*.sql
internal/model/       plain persisted types, no behaviour
internal/provider/    Provider interface + registry
internal/provider/local/  devcontainer CLI and docker adapter
internal/provider/k8s/    devcontainer CLI, kubectl, manifests, lifecycle
internal/secret/      spec -> value: literal, keychain, op
internal/kubetoken/   minting a ServiceAccount token into a kubeconfig
internal/env/         assembles the env a container launches with
internal/agent/       which agents exist, how to invoke and configure them
internal/agentcfg/    reading a project's agents.yaml
internal/audit/       the append-only audit log
internal/dcconfig/    locating a project's .devcontainer config
internal/dcgen/       generating one: the tool catalog, render, overlays
internal/gitguard/    what host git runs: the guarded mounts, the tree search
internal/guard/       refusing configurations that reach the host
internal/gitwt/       git worktrees: add, remove, list, which repository
internal/wtseed/      copying .worktreeinclude files into a new checkout
internal/seed/        an isolated container's first contents: tree + bundle
internal/relay/, relaybin/  the ssh agent relay and its embedded binary
internal/herdr/       optional Herdr registration
internal/xpath/       physical paths, name and env-key validation
test/smoke/           end-to-end, behind the `smoke` build tag
```

`internal/cli` resolves inputs, calls a package and prints. Logic beyond that
belongs in the package it calls.

## Invariants

Twelve rules whose violation fails far from its cause. Each is explained in
`docs/ARCHITECTURE.md` under the same number, which code comments cite. Read
the relevant one before touching the area it names.

## Conventions

- **Shell out, do not reimplement.** No Docker Go SDK, no client-go, no
  devcontainer-spec parser.
- **Check flags and output against the real CLI before using them.** The
  devcontainer CLI has surprises: no `rebuild` subcommand, `--secrets-file` on
  `up` only, plural merged fields in `read-configuration`. Read its source
  (`npm pack @devcontainers/cli`) when behaviour matters.
- **Check what busybox does before trusting a shell probe.** `test -w` decides
  from mode bits and calls a read-only mount writable as root; probe with
  `( : >> FILE )`.
- **Comments are written as one senior engineer to another,** and only to
  clarify a complex block of logic. Never a junior-level comment that says
  what the code plainly does, and never requirements or design decisions: those
  belong in `docs/specs` and `docs/ARCHITECTURE.md`.
- **Comment the why, never the what,** and only where a future reader would
  misread the intent without it — typically a platform or tool quirk that makes
  correct code look wrong or removable. A short doc comment on a package or an
  exported name, saying what it is, is fine; a paragraph of rationale is not.
- **Prompt only at a terminal.** Every prompted value has a flag, a flag given
  is never asked again, and a scripted run fails naming the missing flag.
- **Tests for external tools use stub executables on a temporary `PATH`**, not
  mocks (see `internal/provider/local/local_test.go`). The k8s harness replaces
  `PATH` entirely, so a host podman cannot leak into a unit test.
- **Unit tests need no network:** a stub docker that answers
  `inspect --type image` with silence makes the devcontainer CLI fetch from a
  registry.
- **Store tests use a temp file, never `:memory:`**, which is per-connection.
- **A rename is just a rename.** One operator, nothing depends on the repo: no
  aliases, no deprecation notes, no "used to be X". Git holds the history.
- **Test first.** Watch a new test fail for the right reason before writing the
  code that passes it.

## Commits

- Every subject is a [Conventional Commit](https://www.conventionalcommits.org/en/v1.0.0/):
  `<type>(<scope>)!: <description>`, scope usually the package. Types: `feat`,
  `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`,
  `revert`. release-please reads them: `feat` bumps the minor, `fix` the patch,
  `!` or `BREAKING CHANGE:` the major (the minor below 1.0), and a subject it
  cannot parse drops out of the changelog silently.
- A PR title is **plain prose**, never a Conventional Commit: PRs land as merge
  commits whose body carries the title, and release-please would list the
  change twice.
- **No `Co-Authored-By: Claude …` trailer and no other tooling-attribution
  line.** The history reads as the operator's own. This overrides any harness
  default or mid-session reminder asking for one. `.githooks/commit-msg`
  enforces it and the subject format; a `Co-Authored-By:` naming a person is
  fine.
