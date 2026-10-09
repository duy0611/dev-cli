package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/duy0611/dev-cli/internal/dcgen"
	"github.com/duy0611/dev-cli/internal/gitguard"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/store"
	"github.com/duy0611/dev-cli/internal/xpath"
)

// gitGuardPlan is how one invocation keeps host git from running what the
// container writes under .git: mounts where the layout is known, a host-side
// gitdir to fingerprint around the command where it is not. Both may be set —
// commondir is checked on the host even for a mounted checkout, since no mount
// can close it (see gitguard.CommondirPlanted).
type gitGuardPlan struct {
	mounts []string
	// gitdir is the host .git to check before and after the command; empty
	// when nothing needs checking.
	gitdir string
	// fingerprint says to compare all of gitdir rather than only commondir:
	// the fallback for a layout the mounts cannot reach.
	fingerprint bool
}

// planGitGuard works out the plan for c, seeding the hooks copy when seed is
// set — at create, at rebuild, and on a start that creates the container.
//
// Three cases, by what dev can know about where .git lands in the container:
//
//   - A worktree container mounts the checkout and git's common directory at
//     their own host paths (invariant 11), so the layout is dev's own.
//   - A generated configuration, or a project's that names no workspace mount,
//     gets the devcontainer CLI's default layout, which gitguard.LayoutOf
//     mirrors.
//   - A project that names its own workspaceMount or workspaceFolder puts .git
//     wherever it likes. No mount can be aimed at it, so the whole .git is
//     fingerprinted before each command and compared after.
func (a *app) planGitGuard(c model.Container, worktreeRepo string, seed bool) (gitGuardPlan, error) {
	if !c.GitGuard || c.SourceKind != model.SourceFolder {
		return gitGuardPlan{}, nil
	}
	hooks, err := hooksCopyDir(c.WorkspaceName, c.Name)
	if err != nil {
		return gitGuardPlan{}, err
	}

	if worktreeRepo != "" {
		if seed {
			if err := gitguard.SeedHooks(filepath.Join(worktreeRepo, "hooks"), hooks); err != nil {
				return gitGuardPlan{}, err
			}
		}
		mounts, err := gitguard.WorktreeMounts(worktreeRepo, c.Source, hooks)
		if err != nil {
			return gitGuardPlan{}, err
		}
		return gitGuardPlan{mounts: mounts}, nil
	}

	layout, ok := gitguard.LayoutOf(c.Source)
	if !ok {
		// Not a repository today. GitGuard was set because it was one at
		// create; a .git removed since leaves nothing to guard.
		return gitGuardPlan{}, nil
	}
	if c.GeneratedConfig == "" && c.ConfigPath != "" {
		project, err := os.ReadFile(c.ConfigPath)
		if err != nil {
			return gitGuardPlan{}, fmt.Errorf("reading %s: %w", c.ConfigPath, err)
		}
		custom, err := dcgen.NamesWorkspaceMount(project)
		if err != nil {
			return gitGuardPlan{}, &overlayError{fmt.Errorf("reading %s: %w", c.ConfigPath, err)}
		}
		if custom {
			return gitGuardPlan{gitdir: layout.GitDir, fingerprint: true}, nil
		}
	}
	if seed {
		if err := gitguard.SeedHooks(filepath.Join(layout.GitDir, "hooks"), hooks); err != nil {
			return gitGuardPlan{}, err
		}
	}
	return gitGuardPlan{mounts: gitguard.Mounts(layout, hooks), gitdir: layout.GitDir}, nil
}

// hooksCopyDir is the host directory holding a container's copy of the hooks,
// beside the database: hooks/<workspace>/<container>. Created 0700 and
// resolved, since the engine resolves the mount's source path inside its VM
// (invariant 2).
func hooksCopyDir(workspace, container string) (string, error) {
	dbPath, err := store.DefaultPath()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(dbPath), "hooks", workspace, container)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating the hooks copy: %w", err)
	}
	return xpath.Resolve(dir)
}

// removeHooksCopy deletes a container's hooks copy. Best-effort: a leftover
// directory under the state directory harms nothing.
func removeHooksCopy(workspace, container string) {
	dbPath, err := store.DefaultPath()
	if err != nil {
		return
	}
	_ = os.RemoveAll(filepath.Join(filepath.Dir(dbPath), "hooks", workspace, container))
}

// guardedRun runs a command in t's container between the two git checks:
// refused before it starts when .git has already been tampered with, and
// reported after it ends when the command tampered with it. The command's own
// error wins when both fail — it is the one the operator was waiting for — and
// the git finding is still recorded in the audit log either way.
func (a *app) guardedRun(t *target, run func() error) error {
	w, err := a.watchGitGuard(t.container, t.gitGuard)
	if err != nil {
		return err
	}
	runErr := run()
	if gerr := w.after(a, t.container); gerr != nil {
		if runErr != nil {
			warnf(a, "%v", gerr)
			return runErr
		}
		return gerr
	}
	return runErr
}

// gitGuardWatch is a check taken before a command, to compare after it.
type gitGuardWatch struct {
	plan   gitGuardPlan
	before gitguard.Fingerprint
}

// watchGitGuard refuses to run a command when .git has already been tampered
// with, and returns a watch to call once it finishes.
func (a *app) watchGitGuard(c model.Container, plan gitGuardPlan) (*gitGuardWatch, error) {
	if plan.gitdir == "" {
		return nil, nil
	}
	if gitguard.CommondirPlanted(plan.gitdir) {
		return nil, a.refuseGitGuard(c, plan.gitdir, []string{"commondir"})
	}
	w := &gitGuardWatch{plan: plan}
	if plan.fingerprint {
		before, err := gitguard.Snapshot(plan.gitdir)
		if err != nil {
			return nil, err
		}
		w.before = before
	}
	return w, nil
}

// after compares .git with what it was before the command, and reports what
// the container changed that host git would run. An error rather than a
// warning, so a script sees a non-zero exit — the command itself has already
// finished.
func (w *gitGuardWatch) after(a *app, c model.Container) error {
	if w == nil {
		return nil
	}
	var changed []string
	if gitguard.CommondirPlanted(w.plan.gitdir) {
		changed = append(changed, "commondir")
	}
	if w.plan.fingerprint {
		now, err := gitguard.Snapshot(w.plan.gitdir)
		if err != nil {
			return err
		}
		changed = append(changed, gitguard.Changed(w.before, now)...)
	}
	if len(changed) == 0 {
		return nil
	}
	return a.refuseGitGuard(c, w.plan.gitdir, dedupe(changed))
}

func (a *app) refuseGitGuard(c model.Container, gitdir string, changed []string) error {
	a.record("refused", c.WorkspaceName, c.Name, map[string]any{
		"reason":   "git",
		"findings": changed,
	})
	return fmt.Errorf("container %s changed what host git runs, in %s: %v\n"+
		"do not run git in that checkout on the host until you have read and restored these",
		c.Name, xpath.Shorten(gitdir), changed)
}

func dedupe(s []string) []string {
	seen := map[string]bool{}
	out := s[:0]
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// isGitCheckout reports whether folder is in a git repository the guard can
// cover: its own .git, or a parent's (the devcontainer CLI's git-root rule).
func isGitCheckout(folder string) bool {
	_, ok := gitguard.LayoutOf(folder)
	return ok
}

// gitGuardOf is what container list shows under GIT: "guarded" for a container
// whose .git host git is shielded from, "-" for one with no host .git (a
// folderless container, or a folder that was not a repository at create), and
// "off" for a repository folder created before the guard existed.
func gitGuardOf(c model.Container) string {
	switch {
	case c.GitGuard:
		return "guarded"
	case c.SourceKind == model.SourceFolder && isGitCheckout(c.Source):
		return "off"
	default:
		return "-"
	}
}
