package cli

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/duy0611/dev-cli/internal/dcconfig"
	"github.com/duy0611/dev-cli/internal/gitwt"
	"github.com/duy0611/dev-cli/internal/herdr"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/store"
	"github.com/duy0611/dev-cli/internal/xpath"
	"github.com/spf13/cobra"
)

func newWorktreeCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worktree",
		Short: "Run a container on a git worktree checkout",
		// A group, not a command: see newContainerCmd for why both are needed.
		Args: noArgs(),
		RunE: groupRunE,
	}
	cmd.AddCommand(
		newWorktreeCreateCmd(a),
		newWorktreeListCmd(a),
		newWorktreeRemoveCmd(a),
	)
	return cmd
}

// worktreeOpts is what `worktree create` was asked for beyond the name. The
// container half is a createOpts so the two commands cannot drift.
type worktreeOpts struct {
	repo    string
	branch  string
	path    string
	base    string
	noHerdr bool
	create  createOpts
}

func newWorktreeCreateCmd(a *app) *cobra.Command {
	var (
		workspace string
		toolList  string
		opts      worktreeOpts
	)

	cmd := &cobra.Command{
		Use:   "create NAME",
		Short: "Add a git worktree and run a container on it",
		Long: "Add a git worktree and run a container on it.\n\n" +
			"NAME names both the checkout's record and the container. The branch is\n" +
			"created from --base when it does not exist, and checked out as it is when\n" +
			"it does; --path says where, and nothing is written anywhere else.\n\n" +
			"git works inside the container: the repository is bind-mounted at its own\n" +
			"host path, which is what the checkout's .git file points at.\n\n" +
			"Local providers only. Kubernetes has no host bind mounts, so the paths a\n" +
			"worktree depends on cannot resolve there.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.create.tools = parseToolList(toolList)
			return runWorktreeCreate(cmd.Context(), a, workspace, args[0], opts)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	cmd.Flags().StringVar(&opts.repo, "repo", "",
		"repository to add the worktree to (default: the one holding the working directory)")
	cmd.Flags().StringVar(&opts.branch, "branch", "", "branch to check out or create")
	cmd.Flags().StringVar(&opts.path, "path", "", "where to create the checkout")
	cmd.Flags().StringVar(&opts.base, "base", "", "start point for a new branch")
	cmd.Flags().BoolVar(&opts.noHerdr, "no-herdr", false, "do not register the checkout with Herdr")
	cmd.Flags().BoolVar(&opts.create.noStart, "no-start", false, "record the container without starting it")
	cmd.Flags().BoolVar(&opts.create.generate, "generate", false,
		"generate a base Ubuntu configuration when the checkout ships none")
	cmd.Flags().StringVar(&toolList, "tools", "",
		"comma-separated tools to install in a generated container (see: dev container tools)")
	cmd.Flags().BoolVar(&opts.create.noPersistState, "no-persist-state", false,
		"do not give the container a volume for its agents' configuration")
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
		// No default. One would have to invent a directory on the operator's
		// disk, and a checkout appearing somewhere they did not choose is worse
		// than one more flag.
		return usageErrorf("--path is required; it says where the checkout goes")
	}

	// First, before the store is opened or anything is created: this is the
	// most common way the command will be typed wrong, and every later step's
	// error would be more confusing than this one.
	//
	// The resolved repository is what is checked, not the working directory as
	// such — with --repo given, cwd is never consulted.
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
			// Git's own message names ".git" rather than the directory the
			// operator was standing in, which is why this one is written.
			return usageErrorf("not a git repository: %s "+
				"(run dev worktree create from a repository, or pass --repo)", dir)
		}
		return err
	}
	if opts.base != "" && gitwt.BranchExists(ctx, repo, opts.branch) {
		// Reads as one intent and means another: --base only has an effect when
		// the branch is being created, so silently ignoring it here would hide
		// a mistake rather than report one.
		return usageErrorf("--base has no effect on %s, which already exists", opts.branch)
	}

	wsName, err := a.workspaceName(workspace)
	if err != nil {
		return err
	}
	if err := a.requireLocalProvider(wsName); err != nil {
		return err
	}

	// Before the checkout, so a mistake costs nothing: git refuses an existing
	// directory anyway, and finding that out after the add would mean a
	// rollback for a failure that was knowable first.
	if _, err := os.Stat(opts.path); err == nil {
		return usageErrorf("%s already exists; --path must name a directory to create",
			xpath.Shorten(opts.path))
	}

	if err := gitwt.Add(ctx, repo, opts.path, opts.branch, opts.base); err != nil {
		return usageError(err)
	}

	// Resolved *after* the add, not before: git records the resolved path in
	// the repository's backlink — given a symlinked path it stores the real one
	// — and the mount has to match what git wrote, not what was typed.
	path, err := xpath.Resolve(opts.path)
	if err != nil {
		rollback(ctx, a, repo, opts.path)
		return usageError(err)
	}

	herdrWS := ""
	if !opts.noHerdr && herdr.Available(ctx) {
		// Best-effort throughout: Herdr is a view onto a checkout that exists
		// either way, so a failed view is not a failed command.
		if herdrWS, err = herdr.Open(ctx, path); err != nil {
			warnf(a, "%v (the checkout is fine; it is not in the Herdr sidebar)", err)
		}
	}

	if err := a.createWorktreeRows(wsName, name, repo, path, herdrWS, opts); err != nil {
		rollback(ctx, a, repo, path)
		return err
	}

	a.printf("worktree %s on branch %s at %s\n", name, opts.branch, xpath.Shorten(path))

	if opts.create.noStart {
		return nil
	}
	st, err := a.store()
	if err != nil {
		return err
	}
	c, err := st.GetContainer(wsName, name)
	if err != nil {
		return err
	}
	// Past this point the checkout is not rolled back: the operator has a
	// container worth keeping, and a start failure is something to retry rather
	// than something to undo.
	return a.start(ctx, wsName, c)
}

// createWorktreeRows writes the container and the worktree that names it.
//
// The container first, because the worktree's foreign key points at it. Not one
// transaction: the store's methods each own their statement, and the cascade
// means a worktree row cannot outlive its container even if this returns
// halfway.
func (a *app) createWorktreeRows(wsName, name, repo, path, herdrWS string, opts worktreeOpts) error {
	configPath, err := dcconfig.Find(path)
	switch {
	case err == nil && opts.create.generate:
		return usageErrorf("%s already has a devcontainer config; --generate would shadow it",
			xpath.Shorten(path))
	case err != nil && !errors.Is(err, dcconfig.ErrNoConfig):
		return usageError(err)
	}

	generated := ""
	if errors.Is(err, dcconfig.ErrNoConfig) {
		generated, err = generatedConfigFor(name, opts.create.generate, opts.create.tools,
			worktreeMount(repo, path),
			stateFor(!opts.create.noPersistState, wsName, name), os.Stdin, a.out)
		if err != nil {
			return err
		}
		if generated == "" {
			return usageErrorf("no devcontainer config in %s "+
				"(--generate builds a base Ubuntu one; dev container tools lists what it can add)",
				xpath.Shorten(path))
		}
		configPath = ""
	}

	st, err := a.store()
	if err != nil {
		return err
	}
	c := model.Container{
		Name:            name,
		WorkspaceName:   wsName,
		SourceKind:      model.SourceFolder,
		Source:          path,
		ConfigPath:      configPath,
		GeneratedConfig: generated,
		PersistState:    !opts.create.noPersistState,
	}
	if err := st.CreateContainer(c); err != nil {
		if errors.Is(err, store.ErrExists) {
			return usageErrorf("container %s already exists in workspace %s", name, wsName)
		}
		return err
	}
	return st.CreateWorktree(model.Worktree{
		WorkspaceName:  wsName,
		ContainerName:  name,
		Repo:           repo,
		Branch:         opts.branch,
		Path:           path,
		HerdrWorkspace: herdrWS,
	})
}

// rollback removes a checkout a failed create had already made, so a command
// that reported an error leaves nothing behind.
//
// --force, because the checkout is seconds old and holds only what git put
// there. A failure here is warned about and swallowed: the command is already
// returning an error, and a second one would bury the first.
func rollback(ctx context.Context, a *app, repo, path string) {
	if err := gitwt.Remove(ctx, repo, path, true); err != nil {
		warnf(a, "could not remove the half-made checkout at %s: %v", xpath.Shorten(path), err)
	}
}

// requireLocalProvider refuses a workspace whose provider is not local.
//
// Kubernetes has no host bind mounts, so neither of the two absolute paths a
// worktree depends on can resolve in a pod. Seeding the files would deliver a
// checkout whose every git command fails, which looks like it worked.
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
		Short: "List worktree containers and their checkouts",
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

	// Whether git still knows about a checkout is read from git every time,
	// for the reason live container status is: an operator can run
	// `git worktree remove` themselves, and a stored answer would be wrong
	// from that moment on.
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

	return a.table(func(out io.Writer) {
		header(out, "WORKSPACE", "NAME", "BRANCH", "CHECKOUT", "PATH")
		for _, wt := range rows {
			row(out, wt.WorkspaceName, wt.ContainerName, wt.Branch,
				live(wt), xpath.Shorten(wt.Path))
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
		Short: "Remove a worktree's container and its checkout",
		Long: "Remove a worktree's container and its checkout.\n\n" +
			"The container goes first, then the checkout. git refuses a checkout\n" +
			"holding modified or untracked files, and that refusal leaves the record\n" +
			"in place so the command can be retried; --force passes it through.\n\n" +
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
			return notFoundErrorf("no such worktree: %s (workspace %s); "+
				"dev container remove takes a container that has no checkout", name, wsName)
		}
		return err
	}

	// The container from the engine first, so a --force removal of a dirty
	// checkout is not racing an agent still writing into it.
	t, err := a.resolve(workspace, name)
	if err != nil {
		return err
	}
	defer t.release()
	if err := t.provider.Remove(ctx, t.container); err != nil {
		if !force {
			return err
		}
		// The same forgiveness `container remove --force` gives an engine that
		// refuses: an orphan left by a vanished engine must still be cleanable.
		warnf(a, "%v (forgetting the record anyway)", err)
	}

	// Then git, and *before* the row: git's own refusal on a dirty checkout has
	// to leave the record intact and the command retryable, the same shape
	// `container remove` already has when the engine refuses. provider.Remove
	// above is safe to call again on retry — the container is already gone
	// from the engine by the time this runs.
	if err := gitwt.Remove(ctx, w.Repo, w.Path, force); err != nil {
		return usageError(err)
	}

	// The row last. DeleteContainer cascades the worktree row away with it.
	if err := st.DeleteContainer(wsName, name); err != nil {
		return err
	}

	// Herdr last and best-effort, for the same reason as on create.
	if err := herdr.Close(ctx, w.HerdrWorkspace); err != nil {
		warnf(a, "%v (the checkout is gone; its Herdr workspace may linger)", err)
	}

	// The row is already gone — DeleteContainer cascaded it away — so there is
	// nothing left to delete here. Said out loud because its absence looks
	// like an omission.
	a.printf("worktree %s removed; branch %s kept\n", name, w.Branch)
	return nil
}
