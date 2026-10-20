package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
// container writes under .git. commondir is checked on the host even for a
// mounted checkout, since no mount can close it.
type gitGuardPlan struct {
	mounts      []string
	gitdir      string
	fingerprint bool
	tree        []gitguard.Bind
	treeWatch   []string
	// folderTargets are where the extra folders' mounts land. Kept out of the
	// digest, so adding a folder is not configuration drift.
	folderTargets []string
}

// ownTargets are the container paths of the tree and folder mounts, which
// the configuration digest leaves out.
func (p gitGuardPlan) ownTargets() []string {
	out := make([]string, 0, len(p.tree)+len(p.folderTargets))
	for _, b := range p.tree {
		out = append(out, b.Container)
	}
	return append(out, p.folderTargets...)
}

// treeScope is the directories under the container's reach that host git could
// be pointed into.
type treeScope struct {
	checkout string
	roots    []gitguard.Root
	shielded []string
	watch    bool
}

// planGitGuard returns the guard plan for c. seed copies the hooks at create,
// rebuild and a start that creates the container.
//
// Three layouts: a worktree's is known; a generated or unmoved project's is
// gitguard.LayoutOf's; a project that moves its workspace is fingerprinted
// whole, since no mount can be aimed at it.
//
// The working tree is also held read-only where host git could be pointed into
// it. This is checked on every command, not at create, since the setting often
// arrives after the container exists. The container cannot set it, since
// .git/config is read-only inside.
func (a *app) planGitGuard(ctx context.Context, c model.Container, worktreeRepo string, seed, overrides bool) (gitGuardPlan, error) {
	plan, scopes, err := a.planGitDir(c, worktreeRepo, seed)
	if err != nil {
		return plan, err
	}
	// After the primary's guard, before the tree's binds, which may lie
	// inside a folder's mount: docker applies mounts in order.
	folderEntries, folderTargets := folderMounts(c)
	plan.mounts = append(plan.mounts, folderEntries...)
	plan.folderTargets = folderTargets
	// A Kubernetes workspace is a copy host git never reads.
	if len(scopes) == 0 || !overrides {
		return plan, nil
	}
	// watchGitGuard reports a planted commondir by name, which is clearer than
	// letting git fail.
	if plan.gitdir != "" && gitguard.CommondirPlanted(plan.gitdir) {
		return plan, nil
	}
	// One search per repository, since each has its own config, over every
	// folder's mount: one repository's include can name a file in another.
	var found []gitguard.TreePath
	seen := map[string]bool{}
	for _, scope := range scopes {
		more, err := gitguard.FindTree(ctx, scope.checkout, scope.roots, scope.shielded)
		if err != nil {
			// Deferred like an unreadable project file, so stop and remove
			// still reach the container.
			return gitGuardPlan{}, &overlayError{err}
		}
		for _, p := range more {
			if !seen[p.Host] {
				seen[p.Host] = true
				found = append(found, p)
			}
		}
	}
	scope := scopes[0]
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

// planGitDir plans the .git half of the guard, and the scopes the working tree
// half searches, one per repository.
func (a *app) planGitDir(c model.Container, worktreeRepo string, seed bool) (gitGuardPlan, []treeScope, error) {
	// Isolated is checked too, so a wrongly written row still shares nothing
	// with the host.
	if c.SourceKind != model.SourceFolder || c.Mode == model.ModeIsolated {
		return gitGuardPlan{}, nil, nil
	}
	// Before the GitGuard check: create sets it for unprotected too, so a later
	// switch to guarded finds the primary.
	if c.Mode == model.ModeUnprotected {
		return gitGuardPlan{}, nil, nil
	}
	if c.Mode == model.ModeGuarded {
		return planGuardedGitDir(c, worktreeRepo)
	}
	if !c.GitGuard {
		return gitGuardPlan{}, nil, nil
	}
	plan, scope, err := a.planLegacyGitDir(c, worktreeRepo, seed)
	if scope == nil {
		return plan, nil, err
	}
	return plan, []treeScope{*scope}, err
}

// planLegacyGitDir plans a container from before modes: the single-file guard
// and its hooks copy.
func (a *app) planLegacyGitDir(c model.Container, worktreeRepo string, seed bool) (gitGuardPlan, *treeScope, error) {
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
		// Both mounted at their own host paths. The common directory is a root
		// too, since an include can name a file in it.
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
		// GitGuard was set when it was a repository; a .git removed since
		// leaves nothing to guard.
		return gitGuardPlan{}, nil, nil
	}
	// The repository root, mounted at the parent of the container's .git.
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
			// Container paths unknown, so nothing found is mounted.
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

// planGuardedGitDir plans a guarded container: each repository's .git mounted
// read-only whole, so no hooks copy, commondir check or fingerprint is needed.
// The working tree is still searched, once per repository. The extra folders'
// own mounts come from folderMounts.
func planGuardedGitDir(c model.Container, worktreeRepo string) (gitGuardPlan, []treeScope, error) {
	var (
		plan     gitGuardPlan
		roots    []gitguard.Root
		shielded []string
		repos    []string
	)
	if c.GitGuard {
		switch layout, ok := gitguard.LayoutOf(c.Source); {
		case worktreeRepo != "":
			plan.mounts = gitguard.ReadOnlyWorktreeMounts(worktreeRepo)
			roots = append(roots, gitguard.Root{Host: c.Source, Container: c.Source})
			shielded = append(shielded, worktreeRepo)
			repos = append(repos, c.Source)
		case ok:
			if c.GeneratedConfig == "" && c.ConfigPath != "" {
				custom, err := namesWorkspaceMount(c.ConfigPath)
				if err != nil {
					return gitGuardPlan{}, nil, err
				}
				if custom {
					// Refused at create too: no mount can be aimed at a .git dev
					// cannot locate.
					return gitGuardPlan{}, nil, &overlayError{movedWorkspaceError(c.ConfigPath)}
				}
			}
			plan.mounts = gitguard.ReadOnlyMounts(layout)
			roots = append(roots, gitguard.Root{Host: filepath.Dir(layout.GitDir), Container: path.Dir(layout.ContainerGitDir)})
			shielded = append(shielded, layout.GitDir)
			repos = append(repos, c.Source)
		}
	}
	if len(roots) == 0 && len(c.Folders) > 0 {
		// The primary's mount is still writable, so still a place an include
		// can point into.
		container, extent := primaryPath(c)
		roots = append(roots, gitguard.Root{Host: extent, Container: container})
	}
	for _, f := range c.Folders {
		roots = append(roots, gitguard.Root{Host: f.HostPath, Container: f.ContainerPath})
		if f.WorktreeRepo != "" {
			shielded = append(shielded, f.WorktreeRepo)
			repos = append(repos, f.HostPath)
		} else if layout, ok := gitguard.RootLayout(f.HostPath, f.ContainerPath); ok {
			shielded = append(shielded, layout.GitDir)
			repos = append(repos, f.HostPath)
		}
	}
	var scopes []treeScope
	for _, repo := range repos {
		scopes = append(scopes, treeScope{checkout: repo, roots: roots, shielded: shielded})
	}
	return plan, scopes, nil
}

// namesWorkspaceMount reports whether the project's devcontainer.json moves its
// workspace.
func namesWorkspaceMount(path string) (bool, error) {
	project, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	custom, err := dcgen.NamesWorkspaceMount(project)
	if err != nil {
		return false, &overlayError{fmt.Errorf("reading %s: %w", path, err)}
	}
	return custom, nil
}

// movedWorkspaceError is the refusal for a guarded container whose project
// moves its workspace.
func movedWorkspaceError(path string) error {
	return usageErrorf("%s sets workspaceMount or workspaceFolder, which puts .git where dev cannot "+
		"mount it read-only; create the container with --mode isolated", xpath.Shorten(path))
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

// recordRefusal records a tree refusal in the audit log and returns err. It
// records where a command is refused, since stop and remove plan too and are
// never refused.
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

// checkTreeMounts refuses a container that lacks the tree mounts this
// invocation needs. Mounts are fixed at create, so only a rebuild can add them.
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

// probeHeldFiles refuses to run in a guarded container where a held file has
// become writable.
func (a *app) probeHeldFiles(ctx context.Context, t *target) error {
	if t.container.Mode != model.ModeGuarded {
		return nil
	}
	var opened []string
	for _, b := range t.gitGuard.tree {
		if !b.ReadOnly {
			continue
		}
		if info, err := os.Stat(b.Host); err != nil || info.IsDir() {
			continue
		}
		err := t.provider.Exec(ctx, t.container,
			[]string{"sh", "-c", "( : >> \"$1\" ) 2>/dev/null", "probe", b.Container},
			provider.ExecOpts{Stdout: io.Discard, Stderr: io.Discard})
		if err == nil {
			opened = append(opened, b.Container+", for "+b.Why)
		}
	}
	if len(opened) == 0 {
		return nil
	}
	a.record("refused", t.container.WorkspaceName, t.container.Name, map[string]any{
		"reason":   "git",
		"findings": opened,
	})
	return fmt.Errorf("host git runs files in container %s's workspace that it can now write:\n  %s\n"+
		"a file saved on the host lifts its read-only mount until the container restarts: "+
		"dev container stop %s, then start it again",
		t.container.Name, strings.Join(opened, "\n  "), t.container.Name)
}

// hooksCopyDir is a container's hooks copy directory, beside the database.
// Resolved, since the engine resolves mount sources inside its VM.
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

// removeHooksCopy deletes a container's hooks copy. Best effort.
func removeHooksCopy(workspace, container string) {
	dbPath, err := store.DefaultPath()
	if err != nil {
		return
	}
	_ = os.RemoveAll(filepath.Join(filepath.Dir(dbPath), "hooks", workspace, container))
}

// guardedRun runs a command between the two git checks. The command's error
// wins over a git failure; the git finding is still audited.
func (a *app) guardedRun(ctx context.Context, t *target, run func() error) error {
	warnLegacy(a, t.container)
	if err := a.checkTreeMounts(ctx, t.provider, t.container, t.gitGuard); err != nil {
		return err
	}
	if err := a.probeHeldFiles(ctx, t); err != nil {
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

// gitGuardWatch is the state taken before a command, compared after it.
type gitGuardWatch struct {
	plan   gitGuardPlan
	before gitguard.Fingerprint
}

// watchGitGuard refuses when .git is already tampered with, and returns the
// watch to call after.
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

// after compares .git with its state before the command. An error, not a
// warning, so a script sees a non-zero exit.
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

// snapshotPlan fingerprints gitdir and the working-tree paths host git runs.
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

// isGitCheckout reports whether folder is inside a git repository.
func isGitCheckout(folder string) bool {
	_, ok := gitguard.LayoutOf(folder)
	return ok
}

// trustMountedRepository adds the mounted repositories to safe.directory in the
// container. A podman machine on macOS passes host ownership through, and git
// then refuses the repository.
//
// Written to GIT_CONFIG_GLOBAL, the state volume, never the project's
// .git/config. Each path is added only when missing.
func trustMountedRepository(ctx context.Context, p provider.Provider, c model.Container) error {
	if c.SourceKind != model.SourceFolder || c.Mode == model.ModeIsolated {
		return nil
	}
	var dirs []string
	if c.WorktreeRepo != "" {
		dirs = append(dirs, c.WorktreeRepo, c.Source)
	} else if layout, ok := gitguard.LayoutOf(c.Source); ok {
		dirs = append(dirs, path.Dir(layout.ContainerGitDir))
	}
	for _, f := range c.Folders {
		if f.WorktreeRepo != "" {
			dirs = append(dirs, f.WorktreeRepo, f.ContainerPath)
		} else if _, ok := gitguard.RootLayout(f.HostPath, f.ContainerPath); ok {
			dirs = append(dirs, f.ContainerPath)
		}
	}
	for _, dir := range dirs {
		var stderr bytes.Buffer
		err := p.Exec(ctx, c, []string{"sh", "-c",
			`command -v git >/dev/null || exit 0
git config --global --get-all safe.directory | grep -qxF "$1" || git config --global --add safe.directory "$1"`,
			"trust", dir}, provider.ExecOpts{Stdout: io.Discard, Stderr: &stderr})
		if err != nil {
			return fmt.Errorf("trusting %s in container %s: %w: %s", dir, c.Name, err, strings.TrimSpace(stderr.String()))
		}
	}
	return nil
}

// modeOf is the MODE column: the container's mode, or "-" for a folderless one.
func modeOf(c model.Container) string {
	if c.Mode == model.ModeNone || c.SourceKind == model.SourceNone {
		return "-"
	}
	return string(c.Mode)
}

// warnLegacy warns once per command that a container from before modes runs the
// single-file guard.
func warnLegacy(a *app, c model.Container) {
	if c.Mode != model.ModeLegacy || !c.GitGuard {
		return
	}
	warnf(a, "%s predates container modes; its git guard can lapse after a git command on your machine "+
		"writes repository configuration — recreate it with --mode guarded or --mode isolated", c.Name)
}
