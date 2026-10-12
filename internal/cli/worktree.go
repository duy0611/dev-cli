package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/duy0611/dev-cli/internal/gitwt"
	"github.com/duy0611/dev-cli/internal/herdr"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/store"
	"github.com/duy0611/dev-cli/internal/wtseed"
	"github.com/duy0611/dev-cli/internal/xpath"
	"github.com/spf13/cobra"
)

func newWorktreeCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worktree",
		Short: "Make and remove git worktree checkouts",
		Args:  noArgs(),
		RunE:  groupRunE,
	}
	cmd.AddCommand(
		newWorktreeCreateCmd(a),
		newWorktreeListCmd(a),
		newWorktreeRemoveCmd(a),
	)
	return cmd
}

// worktreeOpts holds the flags of `worktree create` beyond the name.
type worktreeOpts struct {
	repo        string
	branch      string
	path        string
	base        string
	includeFile string
	noHerdr     bool
}

func newWorktreeCreateCmd(a *app) *cobra.Command {
	var (
		workspace string
		opts      worktreeOpts
	)

	cmd := &cobra.Command{
		Use:   "create NAME",
		Short: "Add a git worktree checkout",
		Long: "Add a git worktree checkout.\n\n" +
			"NAME names the checkout's record. The branch is created from --base when\n" +
			"it does not exist, and checked out as it is when it does; --path says\n" +
			"where, and nothing is written anywhere else.\n\n" +
			"No container is made. Run one on the checkout with dev container create,\n" +
			"from inside it or with --folder, choosing its mode there.\n\n" +
			"Ignored files the source checkout's .worktreeinclude lists — .env files,\n" +
			"node_modules — are copied into the new one; --include-file adds patterns\n" +
			"on top. The source is the checkout the command runs from, or --repo.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorktreeCreate(cmd.Context(), a, workspace, args[0], opts)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	cmd.Flags().StringVar(&opts.repo, "repo", "",
		"repository to add the worktree to (default: the one holding the working directory)")
	cmd.Flags().StringVar(&opts.branch, "branch", "", "branch to check out or create")
	cmd.Flags().StringVar(&opts.path, "path", "", "where to create the checkout")
	cmd.Flags().StringVar(&opts.base, "base", "", "start point for a new branch")
	cmd.Flags().StringVar(&opts.includeFile, "include-file", "",
		"more ignored-file patterns to copy in, on top of the source's "+wtseed.FileName)
	cmd.Flags().BoolVar(&opts.noHerdr, "no-herdr", false, "do not register the checkout with Herdr")
	return cmd
}

func runWorktreeCreate(ctx context.Context, a *app, workspace, name string, opts worktreeOpts) error {
	if err := xpath.ValidateName(name); err != nil {
		return usageError(err)
	}
	if opts.branch == "" {
		return usageErrorf("--branch is required")
	}
	if opts.path == "" {
		// No default: dev would have to invent a directory on the operator's
		// disk.
		return usageErrorf("--path is required; it says where the checkout goes")
	}
	// First, before the store is opened or anything is created. The resolved
	// repository is checked, not the working directory: with --repo given, cwd
	// is never consulted.
	dir := opts.repo
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return err
		}
	}
	repo, err := gitwt.CommonDir(ctx, dir)
	if err != nil {
		if errors.Is(err, gitwt.ErrNotRepo) {
			// Git's own message names ".git" rather than the directory, so this
			// one is written instead.
			return usageErrorf("not a git repository: %s "+
				"(run dev worktree create from a repository, or pass --repo)", dir)
		}
		return err
	}
	// The checkout whose ignored files are copied: the one dir is in. A bare
	// repository outside its worktrees has none, which is an error only when
	// patterns were asked for.
	source, err := gitwt.Toplevel(ctx, dir)
	if err != nil && !errors.Is(err, gitwt.ErrNoWorkTree) {
		return err
	}
	// Read before anything is created, so a bad --include-file costs no
	// rollback.
	patterns, err := wtseed.PatternFiles(source, opts.includeFile)
	if err != nil {
		if errors.Is(err, wtseed.ErrNoSource) {
			return usageErrorf("--include-file: %s has no checkout to copy from "+
				"(run from inside a worktree of this repository)", dir)
		}
		return usageErrorf("--include-file: %v", err)
	}

	if opts.base != "" && gitwt.BranchExists(ctx, repo, opts.branch) {
		// --base only applies when the branch is created; ignoring it silently
		// would hide a mistake.
		return usageErrorf("--base has no effect on %s, which already exists", opts.branch)
	}

	wsName, err := a.workspaceName(workspace)
	if err != nil {
		return err
	}
	st, err := a.store()
	if err != nil {
		return err
	}
	if _, err := st.GetWorktree(wsName, name); err == nil {
		return usageErrorf("worktree %s already exists in workspace %s", name, wsName)
	}

	if _, err := os.Stat(opts.path); err == nil {
		return usageErrorf("%s already exists; --path must name a directory to create",
			xpath.Shorten(opts.path))
	}

	if err := gitwt.Add(ctx, repo, opts.path, opts.branch, opts.base); err != nil {
		return usageError(err)
	}

	// Resolved after the add: git records the resolved path in the backlink,
	// and the mount must match it.
	path, err := xpath.Resolve(opts.path)
	if err != nil {
		rollback(ctx, a, repo, opts.path)
		return usageError(err)
	}

	copied, err := wtseed.Seed(ctx, source, path, patterns)
	if err != nil {
		rollback(ctx, a, repo, path)
		return err
	}

	herdrWS := ""
	if !opts.noHerdr && herdr.Available(ctx) {
		// Best effort: a failed Herdr view is not a failed command.
		if herdrWS, err = herdr.Open(ctx, path); err != nil {
			warnf(a, "%v (the checkout is fine; it is not in the Herdr sidebar)", err)
		}
	}

	if err := st.CreateWorktree(model.Worktree{
		WorkspaceName:  wsName,
		Name:           name,
		Repo:           repo,
		Branch:         opts.branch,
		Path:           path,
		HerdrWorkspace: herdrWS,
	}); err != nil {
		rollback(ctx, a, repo, path)
		return err
	}

	a.printf("worktree %s on branch %s at %s\n", name, opts.branch, xpath.Shorten(path))
	if len(copied) > 0 {
		noun := "paths"
		if len(copied) == 1 {
			noun = "path"
		}
		a.printf("copied %d ignored %s from %s (%s)\n", len(copied), noun,
			xpath.Shorten(source), strings.Join(copied, ", "))
	}
	a.printf("run a container on it: cd %s && dev container create %s\n", xpath.Shorten(path), name)
	return nil
}

// rollback removes a checkout a failed create left behind. --force, since it
// holds only what git put there. A failure is warned about, not returned, so it
// does not bury the original error.
func rollback(ctx context.Context, a *app, repo, path string) {
	if err := gitwt.Remove(ctx, repo, path, true); err != nil {
		warnf(a, "could not remove the half-made checkout at %s: %v", xpath.Shorten(path), err)
	}
}

// providerKind is the kind of the provider a workspace names.
func (a *app) providerKind(workspace string) (model.ProviderKind, error) {
	st, err := a.store()
	if err != nil {
		return "", err
	}
	ws, err := st.GetWorkspace(workspace)
	if err != nil {
		return "", err
	}
	rec, err := st.GetProvider(ws.ProviderName)
	if err != nil {
		return "", err
	}
	return rec.Kind, nil
}

// requireLocalProvider refuses a workspace whose provider is not local.
//
// requireLocalProvider refuses a non-local workspace for a worktree container.
// Kubernetes has no host bind mounts, so the worktree's absolute paths cannot
// resolve in a pod, and git would fail there silently.
func (a *app) requireLocalProvider(workspace string) error {
	st, err := a.store()
	if err != nil {
		return err
	}
	ws, err := st.GetWorkspace(workspace)
	if err != nil {
		return err
	}
	rec, err := st.GetProvider(ws.ProviderName)
	if err != nil {
		return err
	}
	if rec.Kind != model.KindLocal {
		return usageErrorf("workspace %s runs on a %s provider; worktrees need a local one "+
			"(a pod has no host bind mounts, so the repository cannot be reached)",
			workspace, rec.Kind)
	}
	return nil
}

// --- list ---------------------------------------------------------------------

func newWorktreeListCmd(a *app) *cobra.Command {
	var (
		workspace string
		all       bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List worktree checkouts and the containers on them",
		Args:  noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorktreeList(cmd.Context(), a, workspace, all)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	cmd.Flags().BoolVar(&all, "all", false, "list every workspace's worktrees")
	return cmd
}

func runWorktreeList(ctx context.Context, a *app, workspace string, all bool) error {
	st, err := a.store()
	if err != nil {
		return err
	}

	scope := ""
	if !all {
		if scope, err = a.workspaceName(workspace); err != nil {
			return err
		}
	}

	rows, err := st.ListWorktrees(scope)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		a.printf("no worktrees; run: dev worktree create NAME --branch B --path P\n")
		return nil
	}

	// Read from git each time, not stored: `git worktree remove` may run
	// outside dev.
	known := map[string]bool{}
	checked := map[string]bool{}
	live := func(w model.Worktree) string {
		if !checked[w.Repo] {
			checked[w.Repo] = true
			entries, err := gitwt.List(ctx, w.Repo)
			if err != nil {
				return "?"
			}
			for _, e := range entries {
				known[e.Path] = true
			}
		}
		if known[w.Path] {
			return "ok"
		}
		return "gone"
	}

	// Any number of containers may share a checkout.
	on := map[string][]string{}
	containers, err := st.ListContainers(scope)
	if err != nil {
		return err
	}
	for _, c := range containers {
		if c.SourceKind == model.SourceFolder {
			key := c.WorkspaceName + "\x00" + c.Source
			on[key] = append(on[key], c.Name)
		}
	}

	return a.table(func(out io.Writer) {
		header(out, "WORKSPACE", "NAME", "BRANCH", "CHECKOUT", "PATH", "CONTAINERS")
		for _, wt := range rows {
			names := strings.Join(on[wt.WorkspaceName+"\x00"+wt.Path], ",")
			if names == "" {
				names = "-"
			}
			row(out, wt.WorkspaceName, wt.Name, wt.Branch,
				live(wt), xpath.Shorten(wt.Path), names)
		}
	})
}

// --- remove -------------------------------------------------------------------

func newWorktreeRemoveCmd(a *app) *cobra.Command {
	var (
		workspace string
		force     bool
	)

	cmd := &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove a worktree checkout",
		Long: "Remove a worktree checkout.\n\n" +
			"Refused while any container is on the checkout: remove those first with\n" +
			"dev container remove. git refuses a checkout holding modified or\n" +
			"untracked files, and that refusal leaves the record in place so the\n" +
			"command can be retried; --force passes it through.\n\n" +
			"The branch is never deleted.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorktreeRemove(cmd.Context(), a, workspace, args[0], force)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	cmd.Flags().BoolVar(&force, "force", false, "remove the checkout even with uncommitted work in it")
	return cmd
}

func runWorktreeRemove(ctx context.Context, a *app, workspace, name string, force bool) error {
	wsName, err := a.workspaceName(workspace)
	if err != nil {
		return err
	}
	st, err := a.store()
	if err != nil {
		return err
	}

	w, err := st.GetWorktree(wsName, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return notFoundErrorf("no such worktree: %s (workspace %s)", name, wsName)
		}
		return err
	}

	// A container on the checkout would lose its folder. Refused rather than
	// removed with it, since it may hold work.
	on, err := containersOn(st, wsName, w.Path)
	if err != nil {
		return err
	}
	if len(on) > 0 {
		return usageErrorf("worktree %s still has %d container(s) on it: %s; "+
			"remove them first with dev container remove", name, len(on), strings.Join(on, ", "))
	}

	// Before the row, so a refusal from git leaves the record intact and the
	// command retryable.
	if err := gitwt.Remove(ctx, w.Repo, w.Path, force); err != nil {
		return usageError(err)
	}
	if err := st.DeleteWorktree(wsName, name); err != nil {
		return err
	}

	// Herdr last and best-effort, as on create.
	if err := herdr.Close(ctx, w.HerdrWorkspace); err != nil {
		warnf(a, "%v (the checkout is gone; its Herdr workspace may linger)", err)
	}
	a.printf("worktree %s removed; branch %s kept\n", name, w.Branch)
	return nil
}

// containersOn names the containers in a workspace whose folder is path.
func containersOn(st *store.Store, workspace, path string) ([]string, error) {
	containers, err := st.ListContainers(workspace)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range containers {
		if c.SourceKind == model.SourceFolder && c.Source == path {
			out = append(out, c.Name)
		}
	}
	return out, nil
}
