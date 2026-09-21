# Persistent agent state

Install a Claude Code plugin inside a container `dev` made, then rebuild it:

```
❯ dev container exec api -- claude plugin marketplace list
No marketplaces configured.
```

The plugins are gone, and so is anything the operator configured with
`git config --global`. A rebuild replaces the container filesystem, and on the
local provider `$HOME` is part of it.

This gives every container one volume that its agents keep their configuration
on, destroyed only with the container. It is the default rather than something
to opt into: an agent losing its plugins on a rebuild is a bug from where the
operator sits, not a setting they should have known to ask about.
`--no-persist-state` opts out.

The column defaults to 0 while `create` defaults it to 1, and the difference
matters. The migration's default applies to rows that already exist, and a
container created before this has no volume — reading it as persisting state
would mount one that was never populated and delete it on the next `container
remove`. A default about intent belongs in the CLI; the column only records
what is true of the row.

## What is already true

Two things are easy to misread as broken and are not.

**Git identity survives.** `internal/env/env.go` reads the host's `user.name`
and `user.email` and injects them as `DEVCONTAINER_GIT_*` on every invocation.
Nothing can lose them, because nothing stores them. What does not survive is the
rest of a `.gitconfig` — aliases, `pull.rebase`, anything the operator set inside
the container.

**Kubernetes already persists all of it.** The PVC carries a `home` subPath, so
`~/.claude` outlives a stop and a rebuild there. The providers disagree:

| | local | k8s |
|---|---|---|
| `$HOME` across stop/start | lost | persists |
| `$HOME` across rebuild | lost | persists |

`docs/specs/2026-09-18-folderless-containers.md` recorded the asymmetry and left
it: *"`$HOME` inside a local container remains container-filesystem-only… k8s
persists home because scaling to zero forced the question."* The question is now
being asked of local too, so the answer stops being incidental. After this
change both providers persist the same directory for the same reason, and the
k8s behaviour is a tested guarantee rather than a side effect of the volume
layout.

## Credentials stay out

Agents authenticate through environment variables — `dev workspace set
ANTHROPIC_API_KEY keychain:…`, resolved per invocation. That is invariant 3 and
it does not change here. The volume holds plugins, marketplaces, settings and
MCP server definitions.

This is what keeps `docs/specs/2026-09-19-ssh-agent-relay.md` standing. Its
argument is that these containers run programs that execute code on their own,
so *"treat anything placed in one as readable"*, and the relay exists so no
credential ever rests in one. A volume of plugin manifests does not contradict
it. A volume with an OAuth token on it would.

One thing to say out loud rather than let someone discover: nothing *prevents* an
operator running an interactive login inside a container, and that would write a
token onto the volume, where it would then persist. The design puts no secret
there. A login still can.

## One volume, four variables

The volume mounts at `/var/dev-state`, and four documented environment variables
point tools into it:

| variable | target |
|---|---|
| `CLAUDE_CONFIG_DIR` | `/var/dev-state/claude` |
| `CODEX_HOME` | `/var/dev-state/codex` |
| `HERMES_HOME` | `/var/dev-state/hermes` |
| `GIT_CONFIG_GLOBAL` | `/var/dev-state/gitconfig` |

All four are set whenever the flag is on, whatever the container installs. An
agent that is not present never reads its variable, so nothing has to know which
agent a container has, and agents stay ordinary catalog tools. Adding one later
is a row in that table.

`CLAUDE_CONFIG_DIR` is the reason this works for Claude Code at all: it moves
`.claude.json` as well as the directory, so there is no second path to mount.

**Outside `$HOME`, deliberately.** A project-owned configuration can set any
`remoteUser` it likes, and the local provider does not know what it is without
running `read-configuration` to ask. `/var/dev-state` needs no lookup and is
spelled the same in the generated JSON, the `--mount` argument and the
Kubernetes volumeMount.

### Why variables and not symlinks

Symlinking `~/.claude` and friends into one directory would work for every agent
uniformly, including agents with no relocation variable. It was rejected because
the uniformity is worth less than the failure it introduces. `ln -sfn` against a
directory that already exists does not replace it — it creates the link *inside*
it, silently, so `~/.claude` becomes `~/.claude/state` and the agent goes on
using the unlinked parent. It needs `-T`, and it needs to run after every feature
that might create those directories first, which the `claude-code` feature does.
Three of the four agents have a documented variable that cannot fail this way.

### Why opencode is not covered

opencode splits its state across four XDG directories — `~/.config/opencode`,
`~/.local/share/opencode`, `~/.local/state/opencode` and `~/.cache/opencode`.
Its consolidating variable, `OPENCODE_CONFIG_DIR`, appears in a GitHub issue and
not in the documentation, and this repository does not build on a flag it has not
checked against the real tool. Setting `XDG_CONFIG_HOME` and friends globally
would relocate every XDG-aware program in the container, not just opencode.

So `--persist-state` does nothing for opencode, and the documentation says so.
Closing the gap later means verifying the variable against a real opencode and
adding one row to the table above — not revisiting this design.

## A column, not just a document

The flag is a column on `containers`, and the generated configuration is
rendered from it.

Storing it only in the rendered JSON would work until the first
`rebuild --tools`. `rewriteGeneratedTools` re-renders the whole document from
`SourceKind` and the tool list, so anything not derivable from those two inputs
is destroyed — the trap the folderless spec called "the sharpest edge in the
change", which is now a second edge in the same place. A test covers it.

It is fixed at create for the same reason that file already gives: *"a rebuild
must not be able to change what a container is mounted on."* Changing it is
`remove` then `create`, which is explicit about destroying the contents. There
is no `rebuild --persist-state`.

## Two mechanisms, one outcome

A container `dev` generated gets the mount in its configuration:

```json
"containerEnv": { "CLAUDE_CONFIG_DIR": "/var/dev-state/claude" },
"mounts": ["source=dev-<workspace>-<container>-state,target=/var/dev-state,type=volume"]
```

A project that ships its own `.devcontainer` cannot: `dev` never writes into a
project folder, which is invariant 9. Those containers get the same volume from
`devcontainer up --mount`, which exists on `up` and **not** on `exec` — so it
must never enter `execArgs`, where it would be a flag error on every command.
The variables ride `--remote-env`, which both paths already carry, so only the
mount needs two routes.

The volume is named `dev-<workspace>-<container>-state`, built beside
`VolumeName` in `internal/provider/local/label.go` and for the reason recorded
there: the name is written at create, read at up and passed to `docker volume
rm`, and a second spelling leaks the volume or mounts an empty one. The
workspace is in the name because container names are unique only within one.

**The chown.** Docker creates a named volume owned by root and, unlike a bind
mount, it gets no UID remapping from the devcontainer CLI — so the remote user's
first write fails with permission denied. This is the same trap the folderless
workspace volume already documents. A generated configuration chowns it in
`postCreateCommand`; a project-owned one has no such hook, so `up` follows with a
chown over the exec channel and says so if it fails — for a project-owned
container only, since doing it for both would be two answers to one question.

That call runs on every up rather than once, because there is no marker to
consult, so it is guarded by a writability test: a recursive chown over a
directory holding an agent's accumulated history is not worth repeating once the
volume already belongs to the remote user. The ownership comes from `id -u`
inside the container rather than a hardcoded `vscode`, because a project-owned
image can run as anyone. Both paths are no-ops on Kubernetes, where `fsGroup`
has already made the volume group-writable.

Two smaller edges in the generated document. `mounts` is a JSON array, so its
order has to be deterministic or the byte-stability test that makes the stored
copy comparable stops holding. And `postCreateCommand` is a single string that
the folderless branch already owns, so a container that is both folderless and
state-persisting needs the two commands joined rather than one overwriting the
other.

## Kubernetes

The Kubernetes provider ignores `mounts` in a devcontainer.json entirely — it
builds its own pod spec — so parity comes from the manifest. The PVC gains a
third subPath, `state`, mounted at the same `/var/dev-state`. No new object, and
it is deleted by the `Remove` that already deletes the PVC.

The overlap guard in `buildManifest` grows to three mounts. It exists because a
home directory nested inside the workspace would mount one subPath over another
and which wins is not something to leave to chance; a third mount point is a
third chance at exactly that.

Only the *presence* of the flag is read there. The volume name means nothing to
Kubernetes, the same way `workspaceMount` already does not.

## Codex joins the catalog

`codex` is in the agent registry and has no catalog entry, so a generated
configuration cannot install it at all. It gets one:

```
ghcr.io/jsburckhardt/devcontainer-features/codex:1
```

Checked against ghcr.io as the catalog requires: the reference resolves, tag `1`
is published, and the feature declares no options — so the entry carries none,
since an option name that does not exist is accepted silently and then does
nothing. There is no official Codex feature, so this community one stands, and
`Official` stays false as it is for `gcloud`, `hermes` and `opencode`. It pins
nothing and installs whatever Codex is current.

A catalog entry is only half-wired until `ToolsOf` maps it back, or
`rebuild --tools +yq` would quietly drop it from a container that had it.

## The collision that would have been silent

`hasGitConfigEnv` in `internal/cli/sshagent.go` matches any key beginning
`GIT_CONFIG_`, and skips configuring commit signing when one is present.
`GIT_CONFIG_GLOBAL` begins `GIT_CONFIG_`. Introducing it would therefore turn off
commit signing for every state-persisting container, with a warning that names a
variable the operator never set.

The check narrows to the three keys it actually means — `GIT_CONFIG_COUNT`,
`GIT_CONFIG_KEY_` and `GIT_CONFIG_VALUE_` — and gets a test. The two mechanisms
compose correctly in git: `GIT_CONFIG_GLOBAL` names the global file and the
numbered triplet layers overrides on top of it.

## Testing

Everything below runs under `make test` against the existing stub-executable
harnesses. Nothing new needs a container engine.

- `internal/dcgen`: a state render carries `mounts` and every `containerEnv`
  key; the output stays byte-stable; a container that is both folderless and
  state-persisting chowns both paths; `ToolsOf` round-trips a document with the
  new fields, and round-trips `codex`.
- `internal/cli`: `rebuild --tools` preserves the state mount — the edge that
  would otherwise be found months later — and does not invent one for a
  container that never asked; `--persist-state` reaches the stored row from
  `create`; the variables reach a project-owned container through
  `containerEnv`, and a workspace setting of the same name still wins; signing
  is still configured when `GIT_CONFIG_GLOBAL` is set.
- `internal/provider/local`: `up` passes `--mount` when the flag is on and not
  when it is off; `execArgs` never carries one; `Remove` removes the volume.
- `internal/provider/k8s`: three volumeMounts when on and two when off; the
  overlap guard rejects a state path nested inside the workspace or home.
- `internal/store`: a migration test in the shape of `migrate_gpg_test.go` —
  seed the older schema by hand, migrate, confirm existing containers default to
  off. Seeding it exposed that the existing gpg test's fixture had no
  `containers` table at all, which no migration had touched until this one.
- `test/smoke`: create with `--persist-state`, write under
  `$CLAUDE_CONFIG_DIR`, rebuild, confirm it is still there; remove, confirm the
  volume is gone.

The smoke test is the only one that proves the opening complaint is fixed. The
unit tests prove the arguments are built correctly, which is a different claim.
