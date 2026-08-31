# USAGE — the dc* toolchain

**What it does:** gives you a throwaway Linux box (a container) that has Claude
Code, your project files, and — if you want — real cloud credentials that
expire on their own. Your laptop stays clean. Claude can't touch anything
outside the project.

**Four commands. That's the whole thing.**

| Command | Gives you |
|---|---|
| `dcx` | a shell inside the box |
| `dcclaude` | Claude Code inside the box |
| `dcws` | a two-pane window: shell on the left, Claude on the right |
| `dccred` | credential admin: are my tokens alive? refresh them |

---

## Install these first

Three things, then two more that matter more than they look.

```sh
brew install jq fzf
npm install -g @devcontainers/cli
```

**A container engine.** The toolchain talks to it through the standard `docker`
CLI, so anything Docker-compatible works. If you have nothing yet, use Podman —
it's what this is developed and tested against:

```sh
brew install podman && podman machine init && podman machine start
```

| Engine | Status |
|---|---|
| Podman | preferred, tested |
| Docker Desktop | tested — just have the daemon running |
| Rancher Desktop | should work, not tried yet. **Set Container Engine to `dockerd (moby)`**, not `containerd` — in containerd mode there's no `docker` CLI and nothing here can start |

**Herdr** — <https://github.com/herdrdev/herdr>. Keep reading, this one is not
optional for `dcws`.

### The two people skip — don't

**`herdr`. `dcws` does not run without it. At all.**

`dcws` is the good part of this toolchain: one command, and you get a named
window with a shell pane and a Claude pane, both already inside the container.
It's also what makes worktrees (`--worktree`) work, and what pops a desktop
notification when a credential expires instead of letting you find out from a
confusing `kubectl` error.

The server has to be *running*, not just installed. Run `herdr` once in a
terminal and leave it. Skip it and every `dcws` command dies with:

```
dcws: no Herdr server reachable - run 'herdr' first
```

**`fzf`. Optional, but it's the difference between picking and squinting.**

Every choice this toolchain asks you to make — profile, cluster, namespace,
service account, GCP project, AWS role — goes through a picker. With `fzf` you
type a few letters and it filters live. Without it you get a numbered list and
have to read all 60 namespaces to find the one you want, then type `37`.

Nothing breaks without `fzf`. It's just worse every single time.

Quick check that everything is there:

```sh
docker info    >/dev/null 2>&1 || echo "no container engine reachable"
command -v fzf >/dev/null 2>&1 || echo "no fzf — pickers will be numbered menus"
herdr workspace list >/dev/null 2>&1 || echo "no Herdr server — run: herdr"
```

`dcx`, `dcclaude` and `dccred` all work fine without Herdr. Only `dcws` hard-requires it.

---

## Start here (60 seconds)

You're in a project folder. You want Claude working on it, safely.

```sh
cd ~/code/my-project
dcws -p base --as my-project
```

That's it. A window opens with a shell and Claude, both inside a container that
sees only this project.

Done reading? The [cheat sheet](#cheat-sheet) is at the bottom. Everything in
between is for when you need more.

---

## The mental model (read once, then forget it)

Four words. You only ever need these.

- **Profile** — which box. `base` (nothing but Claude + git),
  `k8s`, `cloud`, `full`. Pick with `-p`.
- **Instance** — one named, running box. Name it with `--as`. Its own Claude
  config, own shell history, own credentials. Two instances can point at the
  same project and never see each other.
- **Workspace** — the two-pane window `dcws` opens. It's a UI thing, not a
  container thing.
- **Worktree** — a second copy of your repo on a *different branch*, checked
  out in a different folder. Lets you have branch A and branch B open at once
  with zero stashing.

That's the whole vocabulary. Skip to whatever you need:

- [I just want Claude in a sandbox](#recipe-1--sandbox-with-no-cloud-access)
- [I need kubectl / gcloud in there](#recipe-2--sandbox-with-real-cloud-credentials)
- [Two branches at the same time](#recipe-4--two-branches-at-once-worktrees)
- [Something broke](#when-something-breaks)

---

## Recipe 1 — sandbox with no cloud access

The safe default. Use this unless you know you need cloud tools.

```sh
cd ~/code/my-project
dcws -p base --as my-project
```

**What you get:** a window, two panes, both inside the box.

**Just a shell, no window manager, no Claude:**

```sh
dcx -p base --as my-project
```

**Just Claude:**

```sh
dcclaude -p base --as my-project
```

**Run one command and exit:**

```sh
dcx -p base --as my-project -- npm test
```

Note the `--`. Everything after it runs inside the box.

---

## Recipe 2 — sandbox with real cloud credentials

Same as above, different profile.

| You need | Use |
|---|---|
| `kubectl`, `helm`, `k9s` | `-p k8s` |
| `gcloud`, `gsutil`, `bq`, `aws` | `-p cloud` |
| all of it | `-p full` |

**`aws` lives in `cloud`, not in `k8s`.** Kubernetes access is a minted
ServiceAccount token — it never goes through AWS — so `-p k8s` gives you cluster
access and nothing else. If you want a cluster *and* an AWS role, that's
`-p full`.

```sh
dcws -p k8s --as debug-prod
```

**First launch only,** you get asked a few questions — cluster, namespace,
service account for `k8s`; GCP project, service account to impersonate, and AWS
role for `cloud`. Answer once. Every later launch reuses your answers, so you
will never be asked again for this instance. (This is where `fzf` earns its
keep — type to filter instead of scrolling.)

Wrong answer? Redo the questions:

```sh
dccred pick debug-prod
```

**Important, and it will bite you eventually:** the credentials expire after
about an hour, and **nothing auto-refreshes**. That's deliberate — the box
holds nothing capable of minting a new token, so a leak from inside the box
costs you an hour, not your account.

When a token dies you'll see this inside the container:

```
$ kubectl get pods
dcx: k8s token expired 3m ago for instance debug-prod.
     Run on the HOST:  dccred refresh debug-prod
```

Open a normal terminal on your Mac (not the container pane) and run exactly
that. It lands live — you do **not** restart the container, and your running
Claude session is not interrupted.

Check what's still alive:

```sh
dccred status
```

---

## Recipe 3 — a project that already has a `.devcontainer/`

Some repos ship their own container definition. Then you don't pick a profile
at all — drop the `-p` and it uses theirs:

```sh
cd ~/code/that-project
dcws
```

Rule of thumb: **`-p` means "I don't have a devcontainer, make me one."** No
`-p` means "use the one in this repo."

If you run `dcx` in a folder with no devcontainer and no `-p`, it shows you a
profile picker instead of failing. That's fine — just pick.

---

## Recipe 4 — two branches at once (worktrees)

The problem this solves: you're mid-feature, something urgent lands, and you
don't want to stash, switch, rebuild, switch back.

A worktree is a second folder holding the same repo on a different branch. Both
exist at the same time. No stashing, ever.

**Run this from the main repo folder, not from inside another checkout:**

```sh
cd ~/code/my-project        # the real repo. This matters — see below.
dcws --worktree feat/thing
```

You get a new window, on a new branch `feat/thing`, in its own checkout, in its
own container. Your original folder is untouched and still on your old branch.

Branch off something other than your current HEAD:

```sh
dcws --worktree hotfix/urgent --base main
```

**Tearing it down** — removes the container, then the checkout. The branch and
your commits survive:

```sh
dcws --rm-worktree feat/thing
```

`--rm-worktree` works from anywhere in the repo. Only *creating* has the
"from the main repo" rule.

Refuses to delete because the checkout has uncommitted changes? That's the
safety gate working. If you truly want to throw the changes away:

```sh
dcws --rm-worktree feat/thing --force
```

### The one gotcha

`--worktree` names the instance `<folder-name>-<branch>`. If you run it from
inside another checkout, it takes *that* folder's name and you get a confusing
instance name. Two ways out:

```sh
dcws --worktree feat/thing ~/code/my-project    # say which repo
dcws --worktree feat/thing --as my-thing        # or name it yourself
```

---

## Recipe 5 — the window closed / the machine rebooted

**You closed the terminal but the machine stayed up:** nothing to do. Processes
kept running. Reattach and they're still there.

**You rebooted, or restarted the Herdr server:** the window layout came back but
the panes are empty. Refill them:

```sh
dcws -r -p k8s --as debug-prod
```

`-r` relaunches only the empty panes. Anything still running is left alone.

---

## Recipe 6 — signed commits from inside the box

One-time setup. Do it once ever, not once per project.

```sh
dccred signing-key
```

It prints a public key and the exact `gh` command to register it. Run that
command. Then add `--sign` to any launch:

```sh
dcws -p base --as my-project --sign
```

Two things worth knowing:

- It's an **SSH signing key**, not your GPG key. Your GPG key cannot reach the
  container — that's a hard platform limit, not a missing feature.
- Register it as a **signing** key, never an authentication key. The box has
  open internet and runs Claude with permissions skipped, so assume the key
  could leak. As a signing key, the worst case is forged signatures, fixed by
  deleting one key from GitHub. As an auth key, the worst case is repo write
  access.

Without `--sign`, commit signing is forced **off** inside the box. Otherwise
every commit fails with `No secret key`.

---

## Housekeeping

**What do I have running?**

```sh
dcx --list          # name, profile, which folder it's bound to
dccred status       # + running/stopped, + credential time left
```

**Delete one** — container, volumes, state, all of it. Your project files are
never touched:

```sh
dcx --rm my-project
```

**Two sandboxes on the same project?** Totally fine. Just give them different
`--as` names. They don't share history, config, or credentials.

```sh
dcx -p base --as scratch-a
dcx -p base --as scratch-b
```

**A repo that ships its own `.devcontainer/`** gets used as-is, and shows up in
`dcx --list` under the profile `project` the first time you run `dcx` in it. No
flag, nothing added to your repo. It's a record so the toolchain can name the
thing: `dcx --rm NAME` tears the container down, `dccred status` says whether
it's running. That's the whole feature — `dcx` doesn't manage volumes or
credentials for these, so `dccred mint` and `dccred pick` refuse them.

Here the container is keyed to the repo, not to a state directory, so the
"two sandboxes, different `--as`" trick doesn't apply: one repo, one container.
`--as` renames the record, and `dcx` refuses a name already bound to another
folder.

---

## When something breaks

| You see | It means | Do this |
|---|---|---|
| `no Herdr server reachable` | `dcws` needs Herdr running | Run `herdr` in a terminal, then retry |
| `dcws: command not found` | Herdr not installed | Install it, or use `dcx` / `dcclaude`, which don't need it |
| Pickers are numbered lists you have to scroll | no `fzf` | `brew install fzf` |
| `unknown profile 'x'` | typo | `base`, `k8s`, `cloud`, `full` — nothing else |
| `aws: command not found` in a `-p k8s` box | the aws CLI lives in `cloud` | `-p full` if you need a cluster and AWS |
| `gcloud: command not found` | same — it's in `cloud` | `-p cloud`, or `-p full` |
| `dcx: k8s token expired` | credentials aged out (~1h) | On the **host**: `dccred refresh NAME` |
| `no devcontainer config in ...` | plain `dcws` in a repo without one | Add `-p base` (or another profile) |
| `instance 'x' already exists with profile 'y'` | name taken by a different profile | Use another `--as`, or `dcx --rm x` first |
| `no such instance: x` | typo, or already deleted | `dcx --list` to see real names |
| `instance 'x' is bound to ...` | two repos share a basename | Pass `--as` to name this one differently |
| `x is a project instance` | `dccred mint`/`env`/`pick` on a repo that ships its own `.devcontainer/` | Nothing to do — `dcx` mints no credentials for those |
| `no sandbox signing key yet` | `--sign` before setup | Run `dccred signing-key` first |
| `Failed to load marketplace: cache-miss` | image is stale | `make build` |
| Claude can't see a file you swear exists | it's outside the mounted project | Only the project folder is mounted. Move the file in |
| Instance name ends up weird after `--worktree` | ran it from inside a checkout | Rerun from the main repo, or pass `--as` |

**Still stuck?** Every command has real help:

```sh
dcx --help
dcws --help
dcclaude --help
dccred --help
```

---

## What the box actually protects

Worth 20 seconds, because the right-hand column surprises people.

| Safe from the container | **Not** safe |
|---|---|
| Everything on your disk outside the project | Project files — mounted read-write, Claude can change them |
| Your `~/.kube`, `~/.config/gcloud`, `~/.aws` | The Claude API token |
| Your shell, your processes | Internet access — wide open, by design |
| Other instances | |

Read that as: it's a blast shield for your laptop, not a prison for the code.
Don't point it at a repo you'd hate to see rewritten, and don't put a secret in
there you'd hate to see leave.

---

## Cheat sheet

```sh
# --- daily (dcws needs a running `herdr`) ---
dcws -p base  --as NAME             # window: shell + Claude, no cloud access
dcws -p k8s   --as NAME             # ...with kubectl (asks scope on first run)
dcws -p cloud --as NAME             # ...with gcloud + aws
dcws                                # ...repo that ships its own .devcontainer
dcws -r -p base --as NAME           # refill empty panes after a reboot

dcx  -p base --as NAME              # shell only
dcclaude -p base --as NAME          # Claude only
dcx  -p base --as NAME -- CMD ...   # run one command in the box

# --- branches ---
dcws --worktree BRANCH              # new branch, new checkout, new box (from main repo)
dcws --worktree BRANCH --base main  # ...branched from main
dcws --rm-worktree BRANCH           # tear it down; branch and commits survive

# --- credentials ---
dccred status                       # what's alive, how long left
dccred refresh NAME                 # renew (run on the HOST, lands live)
dccred pick NAME                    # redo the cluster/namespace/SA questions
dccred signing-key                  # one-time commit-signing setup

# --- housekeeping ---
dcx --list                          # what exists
dcx --rm NAME                       # delete an instance (project files untouched)

# --- flags worth remembering ---
--as NAME                           # name the instance (default: folder name)
--sign                              # sign commits made inside the box
--gitconfig                         # copy your ~/.gitconfig in
--no-claude                         # dcws: shell pane only
-- CMD                              # everything after runs inside the box
```

Profiles: `base` (nothing) · `k8s` (kubectl, helm, k9s) · `cloud` (gcloud,
gsutil, bq, aws) · `full` (both).

Prerequisites: a Docker-compatible container engine, `devcontainer` CLI, `jq` —
all required. **Herdr** —
required for `dcws`, and it must be running. **`fzf`** — optional, but every
picker is better with it.

State lives in `~/.local/state/dcx/instances/<name>/`. Deleting an instance
deletes that. It never touches your code.
