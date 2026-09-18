# Generated devcontainer configuration

`dev container create` refuses a folder that ships no `.devcontainer`
configuration:

```
❯ dist/dev container create demo-test --folder ./
dev: no devcontainer config in /Users/dnguyen/Projects/…
```

That is right for a project that owns its container definition and wrong for
the folder that has none and wants a plain sandbox — a scratch checkout, a
throwaway clone, a directory to run an agent in. This adds a second way in:
`dev` generates a minimal Ubuntu-based configuration, with a set of tools the
operator picks from a catalog, and keeps that configuration in its own
database.

The project folder is still never written to. The rule the current code states
as "a folder without a config is an error" becomes "`dev` never writes into a
project folder" — the same constraint, minus the part that was really about
having nowhere to put a generated file.

## Data model

The generated configuration lives in the database, on the container row.

```sql
-- 0002_generated_config.sql
ALTER TABLE containers ADD COLUMN generated_config TEXT NOT NULL DEFAULT '';
```

Portable SQL: no default expression, no SQLite-only affinity, so it replays
against Postgres with the rest. `model.Container` gains:

```go
GeneratedConfig string // the devcontainer.json dev wrote; "" when the project owns one
```

Empty is the discriminator. A project-owned container has a `config_path` and
an empty `generated_config`; a generated one has an empty `config_path` and a
populated `generated_config`. Never both.

Storing the JSON rather than a path buys three things. The configuration
cascades away with the workspace, so nothing is orphaned by a delete that goes
through the existing foreign key. It travels to Postgres when the cloud
provider lands, which a file under `$DEV_STATE` would not. And it keeps the
filesystem the engine's business, matching how the rest of the tool treats the
database as the record.

The cost is that the devcontainer CLI only accepts a file path, so a stored
configuration is materialised to a temporary file on every invocation. That is
one helper in `internal/cli`; see [Execution](#execution).

The stored JSON is the truth about a container's tools. The set of catalog ids
is *derived* from it by reverse-mapping the keys of its `features` object, not
stored alongside it. So a configuration edited by hand stays intelligible to
`rebuild --tools`, and a feature reference the catalog does not recognise is
preserved rather than silently dropped on the next rewrite.

Two entries need more than a key lookup to reverse. The shared
`kubectl-helm-minikube` reference maps back to whichever of `kubectl` and
`helm` its options did not disable, and the `apt-get-packages` reference maps
back through its `packages` string, so a package no catalog entry claims is
carried through untouched.

## The catalog

A new package, `internal/dcgen`, holds the tool catalog and renders the JSON.
It knows nothing about the database or about cobra.

```go
// Tool is one installable entry in the catalog.
type Tool struct {
	ID      string
	Summary string
	// Official marks a feature published by the devcontainers project or by
	// the vendor of the tool itself. The picker shows this: everything else
	// is a community image the operator is choosing to trust.
	Official bool
	// Feature is the OCI reference, empty when the tool is installed as an
	// apt package instead.
	Feature string
	Options map[string]any
	// Apt is the package name, set only when Feature is empty. These are
	// collected into one apt-get-packages feature so twelve tools do not
	// become twelve image layers.
	Apt string
}
```

Every reference below returned HTTP 200 from `ghcr.io` on 2026-09-17.

| id | install |
|---|---|
| `node` | `ghcr.io/devcontainers/features/node:1` |
| `python` | `ghcr.io/devcontainers/features/python:1` |
| `gh` | `ghcr.io/devcontainers/features/github-cli:1` |
| `kubectl` | `ghcr.io/devcontainers/features/kubectl-helm-minikube:1` |
| `helm` | the same reference |
| `aws` | `ghcr.io/devcontainers/features/aws-cli:1` |
| `claude-code` | `ghcr.io/anthropics/devcontainer-features/claude-code:1` |
| `hermes` | `ghcr.io/devcontainer-community/devcontainer-features/hermes-agent.nousresearch.com:1` |
| `opencode` | `ghcr.io/devcontainers-extra/features/npm-package:1`, `{"package": "opencode-ai"}` (requires `node`) |
| `gcloud` | `ghcr.io/dhoeric/features/google-cloud-cli:1` |
| `yq` | apt |

`kubectl` and `helm` share one feature reference, which is the one place the
render is more than a loop: selecting both must emit it once. Its options carry
`minikube: "none"` always — nothing in the catalog offers minikube — and
`helm: "none"` when `kubectl` was picked without `helm`, so that asking for one
tool does not quietly install two.

Four references are not first-party: `devcontainers-extra/npm-package`,
`dhoeric/google-cloud-cli`, `devcontainers-extra/apt-get-packages`, and the
community hermes feature. They are pinned by major version and marked
`Official: false` so `dev container tools` can say so.

`yq` is an apt package because no single-tool feature for it is published at a
reference that resolves — every `devcontainers-extra` and
`devcontainers-contrib` spelling tried returned 403.

`jq` is deliberately absent. `base:ubuntu` includes the `common-utils` feature,
whose Debian package list installs it, so a catalog entry would offer a tool
that is already there — and a test asserting it was installed would pass with
the feature omitted entirely. The catalog carries only what the base image
lacks.

### Render

`dcgen.Render(name string, toolIDs []string) (string, error)` produces:

```json
{
  "name": "demo",
  "image": "mcr.microsoft.com/devcontainers/base:1-ubuntu-24.04",
  "features": {
    "ghcr.io/devcontainers-extra/features/apt-get-packages:1": {
      "packages": "yq"
    },
    "ghcr.io/devcontainers/features/node:1": {}
  },
  "remoteUser": "vscode"
}
```

`1-ubuntu-24.04` pins the Ubuntu release while still taking the base image's
own security rebuilds; a bare `:ubuntu` would move to the next LTS underneath a
container that had been working. Keys are emitted in sorted order and the
output is indented, so the stored string is stable across runs and a golden
test can assert on it exactly.

Nothing else goes in. The workspace already supplies environment variables, SSH
forwarding and GPG forwarding through `internal/env`, and duplicating any of it
here would give the same setting two homes.

An empty tool list is legal and renders the same document with no `features`
key: a bare Ubuntu box is a reasonable thing to ask for.

One dependency exists between catalog entries: `opencode` installs through the
npm-package feature, which needs a node runtime. Selecting it without `node`
adds `node` silently and says so, rather than building an image whose install
step fails.

## The create flow

`dcconfig.Find` returning `ErrNoConfig` stops being the end of the story.

**`--generate` given.** Render from `--tools`, a comma-separated list of
catalog ids. Unknown id is exit 2, naming the id and pointing at
`dev container tools`. No prompting on this path at all, so it scripts.

```sh
dev container create demo --folder ./ --generate --tools node,yq,claude-code
```

**No flag, stdin is a terminal.** Ask, then show the picker:

```
no devcontainer config in /proj
generate a base Ubuntu devcontainer? [y/N]: y
```

Answering no gives the error the command gives today.

**No flag, not a terminal.** The current error, extended to teach the flag:

```
dev: no devcontainer config in /proj
     (--generate builds a base Ubuntu one; dev container tools lists what it can add)
```

Exit code stays 2. This follows `provider configure`: prompt only at a
terminal, and make the scripted path fail naming the flag rather than block on
a question nobody will see.

Two guards, both exit 2:

- `--generate` against a folder that *does* ship a configuration. The project
  owns it; shadowing it silently would be the worst of both behaviours.
- `--tools` without `--generate`, which can only be a mistake.

Two new commands come with this. `dev container tools` lists the catalog with
its `official`/`community` column, and `dev container config show NAME` prints
the stored JSON — with the configuration in the database rather than on disk,
there has to be one way to look at it.

## The picker

`internal/cli/picker.go`, built on `golang.org/x/term`, which is already a
direct dependency. Three pieces, so that the logic is testable without a pty:

- `decodeKey(buf []byte) (key, int)` — pure, maps `\x1b[A`, `\x1b[B`, space,
  `\r`, `\x03` to a key and reports how many bytes it consumed. Table-tested,
  including a split escape sequence arriving across two reads.
- `multiSelect(items []item, in io.Reader, out io.Writer) ([]string, error)` —
  takes its streams the way `prompter` does, so a test drives the whole
  interaction over a pipe.
- A wrapper that calls `term.MakeRaw` and restores the terminal with `defer`,
  including on panic. The only untested part, and it holds no logic.

```
tools (↑↓ move, space toggle, enter accept, ^C cancel)

  > [x] node         Node.js LTS
    [ ] gh           GitHub CLI
    [x] claude-code  Claude Code
    ...
```

Redraw moves the cursor up over the list and rewrites it, so the screen does
not scroll on every keystroke. `Ctrl-C` cancels: exit 1, nothing written to the
database, no container row left behind.

## Mutation

```sh
dev container rebuild demo --tools +yq,-helm
```

Every element carrying a `+` or `-` makes the list a diff against the derived
tool set. A list with no prefixes at all is a replacement. Mixing prefixed and
bare elements is exit 2 — it reads as one intent and means another.

The new set is rendered, stored in `generated_config`, and then the existing
rebuild path runs unchanged. On a project-owned container, `--tools` is exit 2:
that configuration is not `dev`'s to rewrite.

## Execution

Both kinds of container run through one code path. Before calling a provider,
`internal/cli` materialises a generated configuration to
`<tmpdir>/.devcontainer/devcontainer.json` and removes the directory on return.
The `model.Container` handed to the provider therefore always carries a real
`ConfigPath`: the stored one for a project-owned container, the temporary one
for a generated container.

Providers then pass `--config <path>` on every `up`, `exec`, `build` and
`read-configuration`, and never rely on the CLI's own lookup. No provider grows
an `if generated` branch, and the lookup `dcconfig.Find` already performed
stops being repeated by the CLI underneath.

Invariant 1 is untouched: both `--id-label` values are still passed on every
`up` and `exec`, still built only in `internal/provider/local/label.go`.

### Settled: `--config` is enough

The CLI's help for `--override-config` says it is "required when there is no
devcontainer.json otherwise", which read as though plain `--config` might not
serve a folder with no `.devcontainer` of its own. It does. From the installed
CLI (v0.89.0), in the function that resolves which configuration to load:

```js
Q = t || (E ? await wr(g, E.configFolderPath) || (i ? kt(...) : void 0) : i)
```

`t` is `--config` and `i` is `--override-config`. When `--config` is given it is
used as-is and the workspace folder is never searched, so a folder with no
configuration of its own is not a problem. A live `read-configuration` agrees:
with an empty folder and an external `--config`, the CLI's own `docker ps`
filter named the external path as `devcontainer.config_file`, and got as far as
the engine before failing on the engine being absent.

Two constraints came out of the same reading, and both are already satisfied:

- The file must be named `devcontainer.json` or `.devcontainer.json`; any other
  name is rejected outright. The materialised path ends in `devcontainer.json`.
- With `--config`, the container is labelled with *that* path as
  `devcontainer.config_file`. A materialised path is in a fresh temporary
  directory each invocation, so a lookup inferred from it would miss the
  container every time. Nothing infers: both `--id-label` values are passed on
  every `up` and `exec`. That invariant is what makes this design safe, rather
  than an unrelated precaution.

Neither `up` nor the created container was exercised end to end: the machine
this was settled on has no docker, podman or kubectl. The smoke test in the
implementation plan is what closes that gap, on a host that has an engine.

## Testing

- `dcgen`: golden JSON for a representative selection, the shared
  `kubectl`/`helm` reference emitting once, apt tools collecting into one
  feature, the empty selection, an unknown id, and a
  render → reverse-map → render round-trip.
- `picker`: `decodeKey` as a table including a split escape sequence;
  `multiSelect` driven over a pipe for toggle, accept and cancel.
- `cli`: all three guards, the non-terminal error text and its exit code, and
  the `+`/`-` diff parser including the mixed-form rejection.
- `store`: a round-trip of the new column, and that deleting a workspace takes
  a generated container's configuration with it.
- `smoke`: create with `--generate --tools yq`, then exec `yq --version` — a
  tool the base image does not already ship, so the assertion proves the
  feature installed rather than that the image was always going to have it.

## Documentation

The work invalidates two pieces of current wording, both updated as part of it:

- `CLAUDE.md` invariant 9, which reads "A folder without one is an error". The
  rule becomes that `dev` never edits or writes into a project's folder, and
  that a folder with its own configuration always wins.
- The `container create` long help, which says the folder "must ship its own
  .devcontainer configuration".
