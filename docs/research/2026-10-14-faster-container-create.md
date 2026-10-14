# Faster `dev container create`

Research, not a design. It records where the time goes when
`dev container create NAME --mode isolated` runs, how other tools remove the
same costs, and which of their techniques fit `dev`. A design that picks from
it belongs in `docs/specs/`.

Nothing here was timed against a real engine; the environment the research ran
in had none. The costs are read from dev's code and from the devcontainer CLI's
source (`@devcontainers/cli` 0.89.0, `npm pack`). The first step of any design
should be to measure (see *Measure first*).

## Where the time goes

`runContainerCreate` (`internal/cli/container.go`) ends in `a.start`, which runs
three phases in sequence.

### 1. Image build: `devcontainer up`

A folder without its own config gets a generated one (`internal/dcgen`): the
pinned `mcr.microsoft.com/devcontainers/base:1-ubuntu-24.04` plus one
devcontainer Feature per tool. The CLI turns that into a
`vsc-<folder>-<sha256(path)>-features` image by writing a
`Dockerfile.extended` and building it, so each Feature is a shell script run at
build time: `apt-get`, `npm install -g`, a downloaded tarball.

On a cold cache every Feature runs, which takes minutes. The cache misses more
often than the operator expects:

- Feature references float on a major tag (`:1`, `:2`); a new release changes
  the layer, and the lockfile that would pin it is not written for a generated
  config living in a temporary directory.
- Every distinct `--tools` set is a distinct image; nothing is shared between
  `go,claude-code` and `go,claude-code,gh` beyond the base.
- Feature tarballs are fetched from ghcr.io by the CLI itself over HTTP
  (`[httpOci]`) on each build that is not fully cached.
- The k8s provider tags each container's image `<registry>/<ws>-<name>:latest`
  (`internal/provider/k8s/name.go`), so two containers with identical tools
  push and pull two images.

### 2. Seeding the isolated volume

The workspace volume starts empty, so `up` runs with `--skip-post-create`, then
dev streams a tar of the folder plus a `git bundle --branches` into the
container and runs `seed.Script` (`internal/provider/local/local.go`,
`internal/seed`). Ignored files travel too, by design (`node_modules`, `.env`),
so a large build tree costs its full size. Then `run-user-commands` runs the
lifecycle hooks the seeding `up` held back.

### 3. Applying agents.yaml

`applyAgentConfig` (`internal/cli/agentconfig.go`) turns the file into steps
(`internal/agent`) and runs every step, every check and every file read and
write as its own `devcontainer exec`. Each one:

- starts Node and loads the CLI: about 0.8–1.0 s for `devcontainer --version`
  alone on the research machine;
- re-reads the configuration and finds the container with `docker ps`/`inspect`;
- runs the **userEnvProbe**, a login interactive shell in the container that
  prints its environment (10 s timeout). `up` and `run-user-commands` can cache
  the result with `--container-session-data-folder`; `exec` has no such flag,
  so every step pays it.

`docs/agents.sample.yaml` produces roughly 20–25 execs: a `command -v` per agent,
check/add/check per Claude marketplace, two execs per MCP server per agent, a
read and a write per file edit, and one clone per git skill repository **per
agent** — the same two repositories are cloned six times for three agents.
At 1–3 s of overhead per exec that is 30–60 s before any step does real work,
and `claude plugin install` adds network time on top.

## How the industry solves it

Every tool below converges on the same split: **do the slow, stable,
secret-free work once, key the result by a hash of its inputs, and start new
environments from that result**. They differ in what the cached artifact is.

### Prebuilt images keyed by config hash

| Tool | Artifact | Key | Where |
|---|---|---|---|
| devcontainers `build` / `devcontainers/ci` | Image with `devcontainer.metadata` label | Operator-chosen tag | Any registry |
| DevPod prebuilds | Image `devpod-<hash>` | Hash of arch, build-relevant config fields, Dockerfile text, build context | Registry given by `--prebuild-repository` |
| Coder envbuilder | Every layer as its own image, plus optionally the whole image | Layer content hash | `ENVBUILDER_CACHE_REPO` |
| Ona (Gitpod) | Built devcontainer image | The devcontainer configuration | ECR on AWS runners |
| Daytona | Snapshot built from an image | Deterministic hash of Dockerfile and context; runners chosen for snapshot locality | Runner-local cache |

- **devcontainer CLI.** `devcontainer build --image-name X [--push]` runs the
  same build `up` does and writes the merged config and every Feature's
  metadata into the `devcontainer.metadata` image label. A config that says only
  `"image": "X"` gets the Features' `containerEnv`, lifecycle hooks, `capAdd`
  and so on back from the label, merged with devcontainer.json last. This is
  the spec's own answer to "prebuilding".
- **DevPod** makes the key automatic: the tag is a hash of exactly what affects
  the build, and it looks for that tag in the registry before building. That
  also solves the problem the devcontainers community keeps raising — keeping a
  prebuilt image in step with its config — because a changed config is a
  different tag.
- **envbuilder** caches at layer granularity: coder/coder's build went from
  36 min uncached to 40 s cached. Its users learned to key the cache by
  repository, not by workspace; a per-workspace cache means every new workspace
  builds from scratch and only restarts hit it — the same mistake as dev's
  per-container k8s tag.

### Snapshots after setup, not just images

- **GitHub Codespaces prebuilds** create a temporary codespace, run setup
  through `onCreateCommand` and `updateContentCommand`, snapshot the container,
  and deploy new codespaces from the snapshot. `postCreateCommand` is not run
  in the prebuild. User secrets are not available during it, so
  secret-dependent steps belong in `postCreateCommand`. GitHub's own rule of
  thumb: prebuilds pay off once creation takes more than two minutes.
- **Ona** prebuilds run tasks with the prebuild trigger after the image build
  and snapshot the filesystem; its docs warn that lifecycle hooks are *not*
  covered by its image cache and recommend moving stable setup into the image
  build.
- **Codex cloud** clones the repository, runs the setup script, and caches the
  container state for up to 12 hours; a resumed container runs only a
  maintenance script. Any change to the scripts, environment variables or
  secrets invalidates the cache.

The common lesson: the lifecycle hooks are part of what gets cached, and the
config is split by *when* a step may run — once per image, once per content
change, once per user with secrets.

### Pre-warmed pools and VM snapshots

E2B restores pre-booted Firecracker microVM snapshots (memory and disk, paged
in lazily) in about 150 ms; Daytona claims instant starts from a warm pool of
already-running sandboxes, matched exactly on snapshot, resources, user,
environment and volumes. Both need a VM layer and a scheduler. Neither fits a
single-operator CLI on a laptop engine, but the warm-pool idea has a local
equivalent: create the next container ahead of time (see *Further out*).

### Making pulls cheaper

- **Pull-through cache.** A `registry:2` with `REGISTRY_PROXY_REMOTEURL`
  proxies exactly one upstream, so ghcr.io and mcr.microsoft.com need one each.
  dockerd's `registry-mirrors` applies to Docker Hub only; BuildKit
  (`buildkitd.toml`), containerd and podman (`registries.conf`) support
  per-registry mirrors. The devcontainer CLI downloads Features with its own
  HTTP client, so an engine mirror does not cover them.
- **Lazy pulling** (eStargz, SOCI, Nydus) starts a container before its image
  is fully downloaded. Grab found that it moves download time from startup into
  runtime instead of removing it. It matters for multi-GB images pulled onto
  fresh k8s nodes, not for a local engine that already holds the image.

### Feature pinning

The devcontainer lockfile (`devcontainer-lock.json`, stable and on by default
in current CLIs; `--frozen-lockfile` enforces it) records the digest each
Feature tag resolved to. Its stated goals include cacheability: pinned Features
keep the image cache key stable.

### Baking agent configuration into the image

Claude Code documents a supported way to pre-install plugins in an image:
install at build time with `CLAUDE_CODE_PLUGIN_CACHE_DIR=/opt/claude-seed`,
then set `CLAUDE_CODE_PLUGIN_SEED_DIR=/opt/claude-seed` at runtime. Claude
registers the seed's marketplaces at startup and loads plugins from it in place,
without cloning. The seed is read-only and auto-update is forced off. Seed
plugins still have to be enabled through `enabledPlugins`. Baking into
`CLAUDE_CONFIG_DIR` instead does not work for dev: that directory is the state
volume, which hides whatever the image put there and is only filled from the
image the first time the volume is created.

## What fits dev

Ranked by gain per unit of risk. "Registry" means a registry is required.

### A. Batch agents.yaml into a few execs

No caching, no new state, no invalidation to get wrong. Run every command step
in one shell script with per-step markers on stderr, and do file edits as one
read pass (all files at once) and one write pass. Check commands can run in the
same batch. Clone each git skill repository once and copy into each agent's
directory. Roughly 25 execs become 2–4, which saves 30–60 s on the sample file.

Constraint: errors must still name the failing step (`Step.Desc`), and the
pending flag must still survive an interrupted apply.

### B. Shared, content-addressed images for generated configs

The DevPod pattern, without a registry. dev owns the generated config, so it
can:

1. render the Features block as today, hash it with the base image reference
   and the architecture;
2. if `dev-tools:<hash>` is missing, run
   `devcontainer build --image-name dev-tools:<hash>` once;
3. give the container a config that names `"image": "dev-tools:<hash>"` and
   omits `features`.

The `devcontainer.metadata` label carries the Features' env and hooks, so
behaviour is unchanged. Every container with the same tool set then starts with
no build at all, and `dev container rebuild` can rebuild the shared image.
Questions for a design:

- what invalidates an image: a new Feature release under the same major tag
  (pin with the lockfile, or rebuild on `rebuild --no-cache`), a new base
  image security release;
- garbage collection of unreferenced `dev-tools:*` images;
- the digest dev records for drift (`ConfigDigest`) is computed over the
  merged config, which would change shape.

Projects that ship their own Dockerfile are outside B: dev never edits their
config. For them, `devcontainer up --cache-from`/`--cache-to` (BuildKit) is the
lever, opt-in per workspace.

### C. Content-addressed tags on k8s (registry)

The k8s provider already builds and pushes. Tagging by the same hash as B
instead of `<ws>-<name>:latest` lets identical tool sets share one image,
lets `buildAndPush` skip both steps when the tag exists in the registry
(`docker manifest inspect` or `buildx imagetools inspect`), and removes the
need for `imagePullPolicy: Always`. This is envbuilder's "key by repository,
not by workspace" lesson applied to dev.

### D. Seed Claude plugins into the image

When agents.yaml declares Claude marketplaces and plugins, build them into the
shared image from B (`CLAUDE_CODE_PLUGIN_CACHE_DIR`) and set
`CLAUDE_CODE_PLUGIN_SEED_DIR`, leaving the apply step only `enabledPlugins`.
This removes the network-bound `claude plugin install` from every create. The
cost: the image key now includes the plugin list, and the plugin versions are
frozen until the image is rebuilt. MCP servers and skills are cheap file
writes once A is in place and need not move.

### E. Speed up the remaining execs

Either pass `--default-user-env-probe none` where the environment dev passes
with `--remote-env` is enough, or run internal steps through `docker exec -u
<remoteUser>` with the environment read once (about 0.1 s per call). Both touch
the rule that exec must see the same document as `up` (the state volume and its
env), so check `docs/ARCHITECTURE.md` before choosing. After A, this saves
seconds, not tens of seconds.

### Further out

- **A local registry** is worth running only if images must survive
  `docker system prune` or a podman machine reset, or be shared with CI, other
  machines or the k8s provider. On one machine the engine's own image store is
  the cache; pulling 1–2 GB from `localhost:5000` is slower than reusing a
  local image. Feature tarballs could be served from it by rewriting Feature
  references, which is untested.
- **Snapshot after setup** (the Codespaces and Codex model) for isolated mode:
  commit a container after `onCreateCommand` and agents.yaml and start later
  containers from it. Hard to do safely: the snapshot would hold anything the
  hooks wrote, the workspace and state live on volumes a commit does not
  capture, and secrets must never reach the image.
- **A warm container** created in the background for the next `create` with the
  same tool set. Only pays off with a predictable workload.
- **Lazy pulling** on k8s clusters that support SOCI or stargz, for large images
  on fresh nodes.

## Measure first

Before designing, time each phase on the operator's machine:

```sh
docker builder prune -af             # cold
time dev container create a --mode isolated --generate --tools go,claude-code
time dev container create b --mode isolated --generate --tools go,claude-code   # warm
time dev container create c --mode isolated --generate --tools go,claude-code --no-agent-config
```

`a − b` is the build, `b − c` is agents.yaml, and the rest of `c` is seeding and
start. Running the CLI with `--log-level trace` on one `devcontainer exec`
shows how long its userEnvProbe takes.

## Sources

- [About GitHub Codespaces prebuilds](https://docs.github.com/en/codespaces/prebuilding-your-codespaces/about-github-codespaces-prebuilds)
- [Configuring prebuilds](https://docs.github.com/en/codespaces/prebuilding-your-codespaces/configuring-prebuilds)
- [Dev Container CLI](https://code.visualstudio.com/docs/devcontainers/devcontainer-cli)
- [Dev container reference: image metadata](https://github.com/devcontainers/spec/blob/main/docs/specs/devcontainer-reference.md)
- [devcontainers/ci GitHub Action](https://github.com/devcontainers/ci/blob/main/docs/github-action.md)
- [devcontainers/cli#153: cacheFrom and Features](https://github.com/devcontainers/cli/issues/153)
- [Ken Muse: Fast start dev containers](https://www.kenmuse.com/blog/fast-start-dev-containers/)
- [Dev container lockfile spec](https://github.com/devcontainers/spec/blob/main/docs/specs/devcontainer-lockfile.md)
- [DevPod: Prebuild a workspace](https://devpod.sh/docs/developing-in-workspaces/prebuild-a-workspace)
- [DevPod: Reduce build times with remote caching](https://devpod.sh/docs/tutorials/reduce-build-times-with-cache)
- [devcontainers discussion #204: keeping prebuilt images in sync](https://github.com/orgs/devcontainers/discussions/204)
- [envbuilder caching](https://github.com/coder/envbuilder/blob/main/docs/caching.md)
- [Coder: Envbuilder security and caching](https://coder.com/docs/admin/integrations/devcontainers/envbuilder/envbuilder-security-caching)
- [Envbuilder cache per repo, not per workspace](https://github.com/anthony-spruyt/spruyt-labs/issues/3200)
- [Ona: Prebuilds](https://ona.com/docs/ona/projects/prebuilds)
- [Ona: Dev container image cache](https://www.gitpod.io/blog/prebuilds)
- [Codex cloud environments](https://developers.openai.com/codex/cloud/environments)
- [E2B runtime](https://github.com/e2b-dev/runtime)
- [E2B sandbox snapshots](https://e2b.dev/docs/sandbox/snapshots)
- [Daytona snapshots](https://www.daytona.io/docs/en/snapshots/)
- [Daytona sandboxes](https://www.daytona.io/docs/en/sandboxes/)
- [Talos: pull-through image cache](https://docs.siderolabs.com/talos/v1.13/configure-your-talos-cluster/images-container-runtime/pull-through-cache)
- [Alex Ellis: multiple registry mirrors with BuildKit](https://blog.alexellis.io/how-to-configure-multiple-docker-registry-mirrors/)
- [stargz-snapshotter](https://github.com/containerd/stargz-snapshotter)
- [Grab: Docker lazy loading](https://engineering.grab.com/docker-lazy-loading)
- [Seekable OCI paper](https://arxiv.org/html/2607.06868)
- [Claude Code: manage plugins for your organization (seed containers)](https://code.claude.com/docs/en/plugins/org)
- [Claude Code: development containers](https://code.claude.com/docs/en/devcontainer)
