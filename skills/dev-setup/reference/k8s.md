# k8s provider (EXPERIMENTAL)

> **Experimental.** Its settings, objects and lifecycle may change without a
> migration; a change may need you to remove the provider and configure it
> again. The local provider is the stable one.

Needs `devcontainer`, `docker` (the image is built on this machine and pushed)
and `kubectl`.

Ask the user for: kubeconfig context, namespace, registry prefix.

```sh
docker login <registry>        # before the first create
dev provider configure prod --kind k8s --context <ctx> --namespace <ns> \
  --registry <registry/prefix> [--platform linux/arm64] [--storage-size 1Gi] \
  [--storage-class SC] [--service-account SA] [--image-pull-secret NAME]
dev workspace init cloud --provider prod
dev workspace use cloud
```

Facts (from `docs/USAGE.md`):

- The namespace must already exist; `dev` creates none.
- `--registry` is required. Default platform is `linux/amd64`; set
  `--platform linux/arm64` for arm64 nodes (`exec format error` otherwise).
- A private registry needs a pull secret in the namespace and
  `--image-pull-secret` (`ImagePullBackOff` otherwise).
- One `--folder` only; more is exit 2.
- Isolated only: the folder is copied in once at create, not mounted. Guarded
  and unprotected are refused. Move later changes with git
  (`dev container exec <name> -- git pull`).
- No `--copy` (needs local; plain `--from` works) and no `remove --keep-state`.
- No worktree containers.
- After rotating a secret: `dev container sync <name>`.
- Pass `-` as the value of an optional setting to clear it.
