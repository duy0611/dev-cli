# Security

`dev` runs coding agents inside containers on your machine. This page is about
the one way such an agent can reach outside its container that is easy to
miss, how `dev` stops it, and what is left for you to decide.

## The short version

- Choose a mode when you create a container: `dev container create NAME
  --mode guarded` (the default), `--mode isolated` or `--mode unprotected`.
  `dev container rebuild NAME --mode MODE` changes it later, except out of
  isolated.
- **Guarded:** you see the agent's edits on your machine as it makes them. It
  can read your git history but cannot commit. You review and commit.
- **Isolated:** the agent works on its own copy of your repository and can
  commit and push. You see its work when you fetch its branches.
- **Unprotected:** the same live folder as guarded, with `.git` writable. The
  agent can commit and push, and it can also change what your git runs. It
  gives up the protection; use it for a task you are watching, then switch back.
- Pick **isolated** when the repository has submodules, when you use
  `dev worktree`, when the agent needs private dependencies over ssh, or when
  it will run unattended or read untrusted text.
- No mode makes it safe to open the agent's folder as a *trusted*
  workspace in your editor. See "What no mode covers".

## The risk

A container's workspace is normally your project folder, shared with the
container so the agent can edit it. That folder holds `.git`, the directory
where git keeps its configuration and hooks. Several git settings name a
program to run: `core.fsmonitor` runs on every `git status`, `core.hooksPath`
and hooks run on commit and checkout, `filter.*` runs when files are compared,
and there are more. If something in the container writes one of those, your
own git runs that program the next time you use git in the folder — on your
machine, as you, outside the container. Your editor runs `git status` every few
seconds without being asked, so you would not need to do anything at all.

"Something in the container" is not only the agent misbehaving. It can be an
agent following instructions hidden in a web page, an issue or a README it was
asked to read, or a compromised package whose install script runs inside.

Turning off hooks and `core.fsmonitor` on your machine does not close this.
Dozens of settings run programs, and a repository's own configuration wins over
your global one.

## The three modes

| | Guarded | Unprotected | Isolated |
|---|---|---|---|
| Your folder in the container | shared live; `.git` read-only | shared live; `.git` writable | copied once into a volume; never shared |
| See the agent's edits on your machine | yes, as they happen | yes, as they happen | no; fetch its branches |
| Agent can commit, branch, rebase | no | yes | yes |
| Agent can push and sign commits | no | yes, through `dev`'s ssh relay | yes, through `dev`'s ssh relay |
| Agent can change what your git runs | no, except through a submodule's or worktree's `.git` file | yes | no |
| Private dependencies over ssh | no | yes | yes |
| Works on Kubernetes | no | no | yes |

### Guarded

The agent edits files in your folder and you watch them change in your own
editor. Its `.git` is mounted read-only, so `git status`, `git diff` and
`git log` work inside, while `git commit` fails with
`Read-only file system`. You commit from your machine.

The container has no access to your ssh agent, because it has nothing to use
it for: it cannot commit, so it has nothing to push, and leaving the relay on
would only let it push what you committed.

Your own git is unaffected. You can commit, push, set upstreams and change
configuration while the container runs.

Tell the agent that committing is your job — in your project's agent
instructions — or it will keep trying. [USAGE.md](USAGE.md#what-a-container-can-do-to-your-git)
has a passage that works in every mode.

A container can be given several folders (`--folder` repeated). The promise is
per folder: each repository's `.git` is read-only, and each gap below applies
to the folder that has it. A folder you did not name is not in the container at
all, so pointing `--folder` at a parent of several repositories is not the way
to give an agent more than one — `dev` finds a repository only by walking up
from the folder, so the repositories below the parent would get no guard.

### Unprotected

The folder is shared live exactly as in guarded, but nothing is mounted over
`.git`. The agent commits, branches, stashes and pushes in your own checkout,
and you see it all as it happens. It gives up the protection described under
"The risk": anything running in the container can write a setting or hook that
your git then runs on your machine, as you. Use it for a task you are watching,
in a repository whose agent you trust for that task, not for an agent that is
unattended or reading untrusted text.

`create --mode unprotected` and a rebuild into it print a warning saying so.
`dev container rebuild NAME --mode guarded` puts the guard back from that
rebuild on. It undoes nothing: a hook or setting the container wrote while it
was unprotected is still in `.git`. After an unprotected stretch you did not
watch, read `.git/config` and `.git/hooks` before using git in the folder.
Kubernetes has no unprotected mode, since a pod cannot mount your folder.

### Isolated

`dev` copies your folder into a volume the container owns: the repository, the
branch you have checked out, your uncommitted changes and your untracked and
ignored files. Your folder is never shared with the container again, so nothing
the agent does can reach the git on your machine.

The agent commits, signs commits and pushes as it would anywhere else. To bring
its work home:

```sh
dev container fetch NAME
git log --oneline main..NAME/branch   # review before you merge
```

`fetch` copies the agent's branches into your folder as `NAME/<branch>`. It
reads only commit data, so nothing from the container's configuration reaches
your git. With several folders, each is copied into a volume of its own, and
each repository's branches come home into that repository.

Your files exist only in the container's volume until you fetch them, and the
fetched `NAME/<branch>` refs are the only copy on your machine until you merge
or push them. `dev container remove` refuses while there is anything
uncommitted, unfetched or unmerged, and deletes the `NAME/*` refs once it
passes; `--force` removes it anyway and keeps them. `podman machine reset` or deleting the volume by
hand loses it without asking.

## What no mode covers

**Reviewing the agent's commits.** Whatever the agent commits is code you have
not read. If your repository runs hook scripts from its own tree — husky's
`.husky/`, for instance — checking out or merging the agent's branch runs the
agent's version of them. Read a branch with `git log -p` or `git diff` first;
neither runs anything.

**Your editor.** Opening a folder as a trusted workspace lets your editor run
code from it: a linter from `node_modules`, a language server's build scripts,
a virtual environment's interpreter, tasks in `.vscode/`. Open folders the agent
has written to in your editor's restricted mode until you trust what is there.

**Your ssh agent, in unprotected and isolated modes.** While a `dev` command runs, the container
can ask your ssh agent to sign with any key it holds. Give it an agent holding
only the key it needs:

```sh
eval "$(ssh-agent -s)"          # a new agent, in this shell only
ssh-add ~/.ssh/github_ed25519   # one key
dev container start-agent NAME --ssh-agent
```

**Secrets you give it.** Workspace settings are ordinary environment variables
inside the container. Use keys made for the purpose, with spend limits and
short expiry where the issuer offers them.

## Guarded's known gaps

Guarded protects everything inside `.git`. It does not protect the files
outside `.git` that tell git where the repository is:

- **Submodules.** Each submodule has a `.git` *file* in your working tree that
  points at its repository. The container can change where it points, and your
  git follows it the next time you use git in that submodule. `dev` warns when a
  folder has submodules, naming the folder.
- **Worktrees.** A `git worktree` checkout has the same kind of `.git` file.
  `dev` warns when a folder is one, naming it.
- **New repositories.** The agent can create a repository anywhere in any of
  its folders, and an editor that finds it may run git there.
- **Projects that move the workspace.** A `devcontainer.json` that sets its own
  `workspaceMount` puts `.git` where `dev` cannot find it, so `dev` refuses to
  create a guarded container for it.

If any of these matter for your repository, use isolated mode.

## On macOS: a case-sensitive volume for your code

macOS treats `CONFIG` and `config` as the same file name; Linux, inside the
container, does not. Some protections fail because of this: in testing, a
container wrote `.git/CONFIG` and changed the real `.git/config` on the Mac.
Guarded mode's read-only `.git` is not affected, but the `.git` files above are.

Keeping your code on a case-sensitive volume removes the whole class. In Disk
Utility, add an "APFS (Case-sensitive)" volume to your existing container and
keep repositories there. Some macOS apps misbehave on case-sensitive volumes,
which is why a separate volume for code is the usual approach. This is
optional; it has not been tested with every engine's file sharing.

## Containers created before modes

Containers from earlier versions of `dev` show `legacy` in `dev container list`
and keep working as before. Their protection has two known weaknesses:

- a git command on your machine that writes repository configuration
  (`git push -u`, `git remote add`, your editor publishing a branch) silently
  turns it off until the container restarts;
- on macOS, writing `.git/CONFIG` from inside the container gets past it.

Recreate them in a mode.

## Why the modes are built this way

The earlier protection kept `.git` writable and mounted individual files inside
it read-only. Two tests showed that cannot work:

- **Replacing a file removes its mount.** git saves configuration by writing a
  new file and renaming it over the old one. Since Linux 3.18, renaming over a
  file that is mounted in another container detaches that mount
  ([`8ed936b`](https://github.com/torvalds/linux/commit/8ed936b5671bfb33d89bc60bdcc7cf0470ba52fe),
  [LWN](https://lwn.net/Articles/595414/)). The container then writes straight
  through to your file. On Linux this happened the instant your git wrote; on a
  macOS podman machine, about 1.5 seconds later. No engine setting changes it.
- **Case variants go around file mounts on macOS**, as above.

Directories do not have either problem: git never replaces the `.git` directory
itself, and every case variant of a directory name landed on its mount. So
guarded mounts all of `.git` read-only, and isolated shares nothing.

Options that were tried and set aside:

- re-applying a broken mount before each command — works, but needs a
  privileged helper container and leaves the gap open during a session, and
  does nothing about case variants;
- watching the configuration and reporting changes — detection always loses the
  race with your editor's background `git status`;
- marking files immutable on macOS — blocks your own git as well;
- hooks or permission rules in the agent itself — they match the command text,
  which has endless spellings, and cover neither scripts the agent runs nor
  other agents.

The tests that showed each of these are the smoke tests in PR #28. The design
is `docs/specs/2026-10-12-container-modes-design.md`.

## Reporting a problem

Open an issue on the repository. For anything that would let a container run a
program on your machine, say so in the title.
