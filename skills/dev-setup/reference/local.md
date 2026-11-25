# Local provider (stable)

Runs containers on this machine's docker-compatible engine (Docker Desktop,
Podman with a `docker` context, Colima, Linux docker).

```sh
dev provider configure local --kind local
dev workspace init personal --provider local
dev provider list && dev workspace list
```

- Requires `devcontainer` and `docker` on PATH and a running engine.
- Modes: `guarded` (default), `unprotected`, `isolated`. `rebuild --mode`
  changes it: guarded <-> unprotected, either -> isolated (one way).
- `--copy image,state` with `--from`, `--keep-state` on `remove`, and several
  `--folder` flags are supported here.
- `--config <project>-dev/devcontainer.json` keeps a hand-written
  `devcontainer.json` outside the project; the project's `.git` is still guarded.
- Worktree containers (`dev worktree create`) need this provider.
- Secrets rotate without a rebuild: settings resolve on every command.
- The kind of a provider cannot change; remove and reconfigure instead.
