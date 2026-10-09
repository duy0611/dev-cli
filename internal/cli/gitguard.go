package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/duy0611/dev-cli/internal/dcgen"
	"github.com/duy0611/dev-cli/internal/gitguard"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
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
	// tree are the mounts, also in mounts, that hold read-only what host git
	// runs from the working tree; see treeScope. Kept apart so a running
	// container can be checked for them and a digest can leave them out.
	tree []gitguard.Bind
	// treeWatch are host paths in the working tree that host git runs,
	// fingerprinted with gitdir when the layout is unknown.
	treeWatch []string
}

// treeTargets are the container paths of the tree mounts.
func (p gitGuardPlan) treeTargets() []string {
	out := make([]string, len(p.tree))
	for i, b := range p.tree {
		out[i] = b.Container
	}
	return out
}

// treeScope is where host git might be pointed into the container's reach for
// one container: the directories it can write, and the paths in them already
// guarded another way. watch says the container paths are unknown, so what is
// found is fingerprinted rather than mounted.
type treeScope struct {
	checkout string
	roots    []gitguard.Root
	shielded []string
	watch    bool
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
//
// Then the working tree, for a provider whose engine takes the document's
// mounts: whatever host git runs from it — a core.hooksPath pointed into the
// checkout, a file an include names there — is held read-only too, or refused
// when it cannot be. Asked of host git on every command rather than decided at
// create, because the setting usually arrives later: `make hooks` or a husky
// `npm install`, run on the host after the container exists. The container
// cannot set one itself, since .git/config is read-only inside.
func (a *app) planGitGuard(ctx context.Context, c model.Container, worktreeRepo string, seed, overrides bool) (gitGuardPlan, error) {
	plan, scope, err := a.planGitDir(c, worktreeRepo, seed)
	// A Kubernetes workspace is a copy host git never reads, so nothing in it
	// is run on the host.
	if err != nil || scope == nil || !overrides {
		return plan, err
	}
	// Host git follows a planted commondir and fails, or reads the config it
	// names. watchGitGuard refuses on it by name, which is the clearer report.
	if plan.gitdir != "" && gitguard.CommondirPlanted(plan.gitdir) {
		return plan, nil
	}
	found, err := gitguard.FindTree(ctx, scope.checkout, scope.roots, scope.shielded)
	if err != nil {
		// Deferred like an unreadable project file: stop and remove must
		// still reach a container whose checkout host git cannot read.
		return gitGuardPlan{}, &overlayError{err}
	}
	if scope.watch {
		for _, p := range found {
			plan.treeWatch = append(plan.treeWatch, p.Host)
			// The link too: repointing it is the change, whatever it named.
			if p.Symlink != "" {
				plan.treeWatch = append(plan.treeWatch, p.Symlink)
			}
		}
		return plan, nil
	}
	var refused []string
	for _, p := range found {
		if why := p.Unmountable(); why != "" {
			refused = append(refused, fmt.Sprintf("%s, for %s: %s", xpath.Shorten(p.Host), p.Why, why))
		}
	}
	if len(refused) > 0 {
		return gitGuardPlan{}, &overlayError{&treeRefusal{container: c.Name, findings: refused}}
	}
	plan.tree = gitguard.TreeBinds(found, plan.mounts)
	for _, b := range plan.tree {
		plan.mounts = append(plan.mounts, b.Entry())
	}
	return plan, nil
}

// planGitDir is planGitGuard's half for .git itself, and the scope its working
// tree half searches; a nil scope when there is nothing to guard.
func (a *app) planGitDir(c model.Container, worktreeRepo string, seed bool) (gitGuardPlan, *treeScope, error) {
	if !c.GitGuard || c.SourceKind != model.SourceFolder {
		return gitGuardPlan{}, nil, nil
	}
	hooks, err := hooksCopyDir(c.WorkspaceName, c.Name)
	if err != nil {
		return gitGuardPlan{}, nil, err
	}

	if worktreeRepo != "" {
		if seed {
			if err := gitguard.SeedHooks(filepath.Join(worktreeRepo, "hooks"), hooks); err != nil {
				return gitGuardPlan{}, nil, err
			}
		}
		mounts, err := gitguard.WorktreeMounts(worktreeRepo, c.Source, hooks)
		if err != nil {
			return gitGuardPlan{}, nil, err
		}
		// Both mounted at their own host paths (invariant 11). The common
		// directory is a root as well as the checkout: it is writable inside,
		// and an include can name a file in it.
		return gitGuardPlan{mounts: mounts}, &treeScope{
			checkout: c.Source,
			roots: []gitguard.Root{
				{Host: c.Source, Container: c.Source},
				{Host: worktreeRepo, Container: worktreeRepo},
			},
			shielded: []string{filepath.Join(worktreeRepo, "config"), filepath.Join(worktreeRepo, "hooks")},
		}, nil
	}

	layout, ok := gitguard.LayoutOf(c.Source)
	if !ok {
		// Not a repository today. GitGuard was set because it was one at
		// create; a .git removed since leaves nothing to guard.
		return gitGuardPlan{}, nil, nil
	}
	// The repository's root, mounted at the parent of the container's .git;
	// .git lies inside it, so an include naming a file there is found too.
	root := gitguard.Root{Host: filepath.Dir(layout.GitDir), Container: path.Dir(layout.ContainerGitDir)}
	shielded := []string{filepath.Join(layout.GitDir, "config"), filepath.Join(layout.GitDir, "hooks")}
	if c.GeneratedConfig == "" && c.ConfigPath != "" {
		project, err := os.ReadFile(c.ConfigPath)
		if err != nil {
			return gitGuardPlan{}, nil, fmt.Errorf("reading %s: %w", c.ConfigPath, err)
		}
		custom, err := dcgen.NamesWorkspaceMount(project)
		if err != nil {
			return gitGuardPlan{}, nil, &overlayError{fmt.Errorf("reading %s: %w", c.ConfigPath, err)}
		}
		if custom {
			// Container paths unknown, so the root's container side is
			// left empty and nothing found is mounted.
			return gitGuardPlan{gitdir: layout.GitDir, fingerprint: true}, &treeScope{
				checkout: c.Source,
				roots:    []gitguard.Root{{Host: root.Host}},
				shielded: shielded,
				watch:    true,
			}, nil
		}
	}
	if seed {
		if err := gitguard.SeedHooks(filepath.Join(layout.GitDir, "hooks"), hooks); err != nil {
			return gitGuardPlan{}, nil, err
		}
	}
	return gitGuardPlan{mounts: gitguard.Mounts(layout, hooks), gitdir: layout.GitDir},
		&treeScope{checkout: c.Source, roots: []gitguard.Root{root}, shielded: shielded}, nil
}

// treeRefusal is a working tree path host git runs that no mount can hold.
type treeRefusal struct {
	container string
	findings  []string
}

func (e *treeRefusal) Error() string {
	return fmt.Sprintf("host git runs files in container %s's workspace that dev cannot make read-only inside it:\n  %s\n"+
		"fix the setting on the host, or point it outside the workspace",
		e.container, strings.Join(e.findings, "\n  "))
}

// recordRefusal records err in the audit log when it is a tree refusal, and
// returns it either way. Recorded where a command is refused, not where the
// plan is made, since stop and remove make one too and are never refused.
func (a *app) recordRefusal(c model.Container, err error) error {
	var tr *treeRefusal
	if errors.As(err, &tr) {
		a.record("refused", c.WorkspaceName, c.Name, map[string]any{
			"reason":   "git",
			"findings": tr.findings,
		})
	}
	return err
}

// checkTreeMounts refuses to run anything in a container that exists without
// the tree mounts this invocation needs. Mounts are fixed when a container is
// created, so one created before `make hooks` ran on the host has none, and
// starting or exec'ing into it would leave host git running files the agent
// can edit. Only a rebuild can add them.
//
// A provider that cannot report mounts is not asked, and a container that
// does not exist yet passes: start is about to create it with them.
func (a *app) checkTreeMounts(ctx context.Context, p provider.Provider, c model.Container, plan gitGuardPlan) error {
	if len(plan.tree) == 0 {
		return nil
	}
	reader, ok := p.(provider.MountReader)
	if !ok {
		return nil
	}
	mounts, exists, err := reader.Mounts(ctx, c)
	if err != nil || !exists {
		return err
	}
	readOnly := map[string]bool{}
	for _, m := range mounts {
		readOnly[m.Destination] = m.ReadOnly
	}
	var missing []string
	for _, b := range plan.tree {
		if ro, ok := readOnly[b.Container]; !ok || (b.ReadOnly && !ro) {
			missing = append(missing, b.Container+", for "+b.Why)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	a.record("refused", c.WorkspaceName, c.Name, map[string]any{
		"reason":   "git",
		"findings": missing,
	})
	return fmt.Errorf("host git runs files in container %s's workspace that it can still edit:\n  %s\n"+
		"the container predates the setting; rebuild it to make them read-only inside: dev container rebuild %s",
		c.Name, strings.Join(missing, "\n  "), c.Name)
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
func (a *app) guardedRun(ctx context.Context, t *target, run func() error) error {
	if err := a.checkTreeMounts(ctx, t.provider, t.container, t.gitGuard); err != nil {
		return err
	}
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
		before, err := snapshotPlan(plan)
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
		now, err := snapshotPlan(w.plan)
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

// snapshotPlan fingerprints gitdir and the working tree paths host git runs.
// One map for both: gitdir's keys are relative to it, the tree's absolute.
func snapshotPlan(plan gitGuardPlan) (gitguard.Fingerprint, error) {
	s, err := gitguard.Snapshot(plan.gitdir)
	if err != nil {
		return nil, err
	}
	tree, err := gitguard.SnapshotPaths(plan.treeWatch)
	if err != nil {
		return nil, err
	}
	for k, v := range tree {
		s[k] = v
	}
	return s, nil
}

func (a *app) refuseGitGuard(c model.Container, gitdir string, changed []string) error {
	for i, k := range changed {
		if filepath.IsAbs(k) {
			changed[i] = xpath.Shorten(k)
		}
	}
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
