package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/duy0611/dev-cli/internal/dcconfig"
	"github.com/duy0611/dev-cli/internal/dcgen"
	"github.com/duy0611/dev-cli/internal/gitwt"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/store"
	"github.com/duy0611/dev-cli/internal/xpath"
	"github.com/spf13/cobra"
)

func newContainerCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "container",
		Short: "Create and run devcontainers",
		// RunE stops cobra inheriting the root's RunE, which exits 0 for a
		// mistyped subcommand.
		Args: noArgs(),
		RunE: groupRunE,
	}
	cmd.AddCommand(
		newContainerCreateCmd(a),
		newContainerListCmd(a),
		newContainerStartCmd(a),
		newContainerStopCmd(a),
		newContainerRemoveCmd(a),
		newContainerRebuildCmd(a),
		newContainerLogsCmd(a),
		newContainerShellCmd(a),
		newContainerExecCmd(a),
		newContainerStartAgentCmd(a),
		newContainerSyncCmd(a),
		newContainerFetchCmd(a),
		newContainerToolsCmd(a),
		newContainerConfigCmd(a),
	)
	return cmd
}

// --- create -------------------------------------------------------------------

// createOpts holds the flags of `container create` beyond the name and folder.
type createOpts struct {
	noStart         bool
	noFolder        bool
	generate        bool
	noPersistState  bool
	tools           []string
	agentConfig     string
	noAgentConfig   bool
	from            string
	allowPrivileged bool
	mode            model.Mode
	// extraFolders are the --folder arguments after the first.
	extraFolders []string
	// Set only on rows from before modes; no flag sets it.
	legacy bool
}

func newContainerCreateCmd(a *app) *cobra.Command {
	var (
		workspace string
		folders   []string
		toolList  string
		opts      createOpts
	)

	cmd := &cobra.Command{
		Use:   "create NAME",
		Short: "Create a container and start it",
		Long: "Create a container and start it.\n\n" +
			"The folder is --folder, or the current directory when neither --folder\n" +
			"nor --no-folder is given; the host's root and your home are refused.\n" +
			"--folder may be repeated: the first is the primary, whose configuration\n" +
			"the container is built from, and each other appears at\n" +
			"/workspaces/<basename>, guarded or isolated as the primary is. A\n" +
			"project that ships its own .devcontainer configuration is used as it\n" +
			"is; dev never edits it. For a folder with none, --generate\n" +
			"builds a base Ubuntu configuration from the tools --tools names.\n\n" +
			"With --no-folder there is no host directory at all: the container's work\n" +
			"lives in a volume dev creates and removes with it. That configuration is\n" +
			"always generated, and kept in dev's own database.\n\n" +
			"The agents' configuration — plugins, marketplaces, MCP servers, and a\n" +
			"global gitconfig — lives on a volume of its own and outlives a rebuild.\n" +
			"Credentials do not go there: agents read those from the workspace's\n" +
			"settings on every command. --no-persist-state opts out. Either way it is\n" +
			"fixed at create; to change it, remove the container and create it again.\n\n" +
			"A project's .devcontainer/agents.yaml, when it has one, declares its\n" +
			"agents' skills, MCP servers and plugins; dev applies it inside the\n" +
			"container once it is running, and again on every rebuild. --agent-config\n" +
			"names another file, --no-agent-config applies none.\n\n" +
			"--from SOURCE starts from another container in the workspace whose\n" +
			"configuration dev generated: its tools, its state volume setting and its\n" +
			"agents.yaml choice. The configuration is rendered afresh for this\n" +
			"container, so the two never share a volume. --tools then takes +/-\n" +
			"changes to the source's list, and the other flags override what is\n" +
			"inherited.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.tools = parseToolList(toolList)
			folder := ""
			if len(folders) > 0 {
				folder, opts.extraFolders = folders[0], folders[1:]
			}
			return runContainerCreate(cmd.Context(), a, workspace, args[0], folder, opts)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	cmd.Flags().StringArrayVar(&folders, "folder", nil,
		"host folder holding the project (default: the current directory); repeat for more folders, the first is the primary")
	cmd.Flags().Var(modeFlag{&opts.mode}, "mode",
		"guarded (the folder live, .git read-only), unprotected (the folder live, .git writable) or isolated (a copy the agent owns)")
	cmd.Flags().BoolVar(&opts.noStart, "no-start", false, "record the container without starting it")
	cmd.Flags().BoolVar(&opts.noFolder, "no-folder", false,
		"create a container with no host folder; its work lives in a volume dev owns")
	cmd.Flags().BoolVar(&opts.generate, "generate", false,
		"generate a base Ubuntu configuration when the folder ships none")
	cmd.Flags().StringVar(&toolList, "tools", "",
		"comma-separated tools to install in a generated container (see: dev container tools)")
	cmd.Flags().BoolVar(&opts.noPersistState, "no-persist-state", false,
		"do not give the container a volume for its agents' configuration")
	cmd.Flags().StringVar(&opts.agentConfig, "agent-config", "",
		"apply this agents.yaml instead of the project's .devcontainer/agents.yaml")
	cmd.Flags().BoolVar(&opts.noAgentConfig, "no-agent-config", false,
		"apply no agents.yaml, even if the project has one")
	cmd.Flags().StringVar(&opts.from, "from", "",
		"start from another generated container's tools and settings")
	cmd.Flags().BoolVar(&opts.allowPrivileged, "allow-privileged", false,
		"let the configuration ask for privileged mode, the engine socket, host namespaces or a mount of / or your home")
	return cmd
}

func runContainerCreate(ctx context.Context, a *app, workspace, name, folder string, opts createOpts) error {
	if err := xpath.ValidateName(name); err != nil {
		return usageError(err)
	}
	if (folder != "" || len(opts.extraFolders) > 0) && opts.noFolder {
		return usageErrorf("--folder and --no-folder contradict each other")
	}
	defaulted := false
	if folder == "" && !opts.noFolder {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		folder, defaulted = cwd, true
	}
	if !opts.noFolder {
		if err := refuseBroadFolder(folder); err != nil {
			return err
		}
	}
	if len(opts.tools) > 0 && !opts.generate && !opts.noFolder && opts.from == "" {
		return usageErrorf("--tools only applies with --generate or --from")
	}
	// Checked first, so a bad flag is reported as itself, not as a later
	// failure.
	if _, err := agentConfigChoice(opts.agentConfig, opts.noAgentConfig); err != nil {
		return err
	}

	wsName, err := a.workspaceName(workspace)
	if err != nil {
		return err
	}
	mode, err := a.modeFor(wsName, opts)
	if err != nil {
		return err
	}
	if opts.legacy && mode == model.ModeGuarded {
		mode = model.ModeLegacy
	}
	if len(opts.extraFolders) > 0 {
		if err := a.allowExtraFolders(wsName, mode); err != nil {
			return err
		}
	}
	// Before the source is resolved, so the render sees the tools --from
	// carries.
	if err := applyFrom(a, wsName, &opts); err != nil {
		return err
	}
	// Re-checked: --from may have filled in what the flags left empty.
	agentConfig, err := agentConfigChoice(opts.agentConfig, opts.noAgentConfig)
	if err != nil {
		return err
	}

	var (
		source       string
		configPath   string
		generated    string
		worktreeRepo string
		extras       []model.Folder
	)
	if opts.noFolder {
		// No project to shadow, so --generate is redundant here. Passing
		// opts.generate, not true, keeps the picker reachable from a terminal.
		generated, err = generatedConfigFor(name, opts.generate, opts.tools,
			folderlessMount(wsName, name), stateFor(!opts.noPersistState, wsName, name),
			os.Stdin, a.out)
		if err != nil {
			return err
		}
		if generated == "" {
			// Empty only for a scripted run without --tools; here that means
			// the bare image, not an error.
			generated, err = dcgen.Render(name, nil, folderlessMount(wsName, name),
				stateFor(!opts.noPersistState, wsName, name))
			if err != nil {
				return usageError(err)
			}
		}
	} else {
		source, configPath, generated, worktreeRepo, err = folderSource(ctx, wsName, name, folder, mode, opts, a)
		if err != nil {
			return err
		}
		if extras, err = resolveExtraFolders(ctx, opts.extraFolders, mode); err != nil {
			return err
		}
		sortFolders(extras)
		if defaulted {
			a.printf("using %s (the current directory)\n", xpath.Shorten(source))
		}
	}

	// A worktree's .git and backlink are absolute host paths a pod cannot
	// reach.
	if worktreeRepo != "" {
		if err := a.requireLocalProvider(wsName); err != nil {
			return err
		}
	}
	if mode == model.ModeGuarded {
		if err := checkGuarded(a, source, configPath, worktreeRepo, len(extras) > 0); err != nil {
			return err
		}
		for _, f := range extras {
			warnGuardGaps(a, f.HostPath, f.WorktreeRepo != "", true)
		}
	}

	// Advisory and not stored: the compose file can be added or removed later.
	if msg := composeWarning(configPath, !opts.noPersistState); msg != "" {
		warnf(a, "%s", msg)
	}

	st, err := a.store()
	if err != nil {
		return err
	}

	kind := model.SourceFolder
	if opts.noFolder {
		kind = model.SourceNone
	}
	c := model.Container{
		Name:            name,
		WorkspaceName:   wsName,
		SourceKind:      kind,
		Source:          source,
		ConfigPath:      configPath,
		GeneratedConfig: generated,
		// A column, not a document field: rebuild --tools re-renders the
		// document and would drop it.
		PersistState: !opts.noPersistState,
		AgentConfig:  agentConfig,
		// Set by flag only: a project file the agent can edit must not grant
		// it.
		AllowPrivileged: opts.allowPrivileged,
		// Fixed at create. Off for isolated, which never mounts the folder, and
		// for folderless, which has no host .git.
		GitGuard: !opts.noFolder && mode != model.ModeIsolated &&
			(isGitCheckout(source) || worktreeRepo != ""),
		// Stored so every later command mounts the same repository.
		WorktreeRepo: worktreeRepo,
		Mode:         mode,
		Folders:      extras,
	}
	if mode == model.ModeIsolated {
		c.WorkspacePath = isolatedPath(source)
	}
	if err := checkFolders(c); err != nil {
		return err
	}
	// After the source is known, before the row is written, so a bad file
	// creates nothing.
	if err := markAgentConfig(&c); err != nil {
		return err
	}
	// Only when starting now: start checks again, so --no-start defers the
	// check there.
	if !opts.noStart {
		if err := a.guardNewContainer(ctx, wsName, &c); err != nil {
			return err
		}
	}
	err = st.CreateContainer(c)
	if errors.Is(err, store.ErrExists) {
		return usageErrorf("container %s already exists in workspace %s", name, wsName)
	}
	if err != nil {
		return err
	}
	a.recordCreate(wsName, c)
	if mode == model.ModeUnprotected {
		warnUnprotected(a, name)
	}
	if opts.noFolder {
		a.printf("container %s (workspace %s) with no folder\n", name, wsName)
	} else {
		a.printf("container %s (workspace %s, %s) from %s\n", name, wsName, mode, xpath.Shorten(source))
		for _, f := range c.Folders {
			a.printf("  with %s at %s\n", xpath.Shorten(f.HostPath), f.ContainerPath)
		}
		switch repos := guardedRepos(c); {
		case len(repos) == 1:
			a.printf("guarded: the agent can read this repository's history but not commit; you commit and push\n")
		case len(repos) > 1:
			short := make([]string, len(repos))
			for i, r := range repos {
				short[i] = xpath.Shorten(r)
			}
			a.printf("guarded: the agent can read these repositories' history but not commit; you commit and push: %s\n",
				strings.Join(short, ", "))
		}
	}

	if opts.noStart {
		return nil
	}
	return a.start(ctx, wsName, c)
}

// modeFor decides a new container's mode: --mode if given, else the provider's
// default. Guarded on local, isolated on k8s, where a pod cannot mount a live
// folder.
func (a *app) modeFor(wsName string, opts createOpts) (model.Mode, error) {
	if opts.noFolder {
		if opts.mode != "" {
			return "", usageErrorf("--mode applies to a folder; a --no-folder container has no checkout to guard or isolate")
		}
		return model.ModeNone, nil
	}
	kind, err := a.providerKind(wsName)
	if err != nil {
		return "", err
	}
	switch opts.mode {
	case "":
		if kind == model.KindK8s {
			return model.ModeIsolated, nil
		}
		return model.ModeGuarded, nil
	case model.ModeGuarded:
		if kind == model.KindK8s {
			return "", usageErrorf("--mode guarded needs a local provider: a pod cannot mount your folder live; " +
				"use --mode isolated, the k8s default")
		}
		return model.ModeGuarded, nil
	case model.ModeUnprotected:
		if kind == model.KindK8s {
			return "", usageErrorf("--mode unprotected needs a local provider: a pod cannot mount your folder live; " +
				"use --mode isolated, the k8s default")
		}
		return model.ModeUnprotected, nil
	case model.ModeIsolated:
		return model.ModeIsolated, nil
	default:
		return "", usageErrorf("unknown mode %q; use guarded, unprotected or isolated", opts.mode)
	}
}

// warnUnprotected tells the operator what an unprotected container can reach.
func warnUnprotected(a *app, name string) {
	warnf(a, "container %s is unprotected: it can change what git on your machine runs; "+
		"dev container rebuild %s --mode guarded closes it again", name, name)
}

// checkGuarded refuses a project mount guarded mode cannot protect, and warns
// about nested .git files it protects only in part. Both point at isolated
// mode. named says the warnings name the folder, since there are several.
func checkGuarded(a *app, source, configPath, worktreeRepo string, named bool) error {
	// No .git to guard in a non-repository. A bad project file is reported by
	// its readers.
	if !isGitCheckout(source) && worktreeRepo == "" {
		return nil
	}
	if configPath != "" {
		custom, err := namesWorkspaceMount(configPath)
		if err != nil {
			return usageError(err)
		}
		if custom {
			return movedWorkspaceError(configPath)
		}
	}
	warnGuardGaps(a, source, worktreeRepo != "", named)
	return nil
}

// warnGuardGaps warns about the .git files in folder that guarded mode cannot
// hold: a submodule's, and a worktree checkout's own.
func warnGuardGaps(a *app, folder string, worktree, named bool) {
	this := "this repository"
	if named {
		this = xpath.Shorten(folder)
	}
	if _, err := os.Stat(filepath.Join(folder, ".gitmodules")); err == nil {
		warnf(a, "%s has submodules; guarded mode cannot stop the container repointing "+
			"a submodule's .git file at a config of its own, which your git would then run. "+
			"Use --mode isolated if that matters for this repository", this)
	}
	if worktree {
		if !named {
			this = "this folder"
		}
		warnf(a, "%s is a git worktree; guarded mode cannot stop the container repointing "+
			"its .git file at a config of its own, which your git would then run. "+
			"Use --mode isolated if that matters for this repository", this)
	}
}

// allowExtraFolders refuses a second folder where a container cannot take
// one: on k8s, and for a mode with no folders to place.
func (a *app) allowExtraFolders(wsName string, mode model.Mode) error {
	kind, err := a.providerKind(wsName)
	if err != nil {
		return err
	}
	if kind == model.KindK8s {
		return usageErrorf("the k8s provider is experimental and takes one folder; give --folder once")
	}
	if mode != model.ModeGuarded && mode != model.ModeUnprotected && mode != model.ModeIsolated {
		return usageErrorf("a %s container takes one folder", displayMode(mode))
	}
	return nil
}

// modeFlag parses --mode, accepting only what a create may ask for.
type modeFlag struct{ target *model.Mode }

func (f modeFlag) String() string {
	if f.target == nil {
		return ""
	}
	return string(*f.target)
}

func (f modeFlag) Set(v string) error {
	*f.target = model.Mode(v)
	return nil
}

func (modeFlag) Type() string { return "mode" }

// refuseBroadFolder refuses the host root or the operator's home as a
// container's folder: either mounts every project and every key in ~/.ssh.
func refuseBroadFolder(folder string) error {
	resolved, err := xpath.Resolve(folder)
	if err != nil {
		return nil
	}
	home, err := operatorHome()
	if err != nil {
		return err
	}
	if resolved == "/" || resolved == home {
		return usageErrorf("%s would mount everything under it; "+
			"run from the project's folder, or pass --folder PATH", xpath.Shorten(resolved))
	}
	return nil
}

// folderSource resolves a --folder to its source, project config or generated
// document.
func folderSource(ctx context.Context, wsName, name, folder string, mode model.Mode, opts createOpts, a *app) (source, configPath, generated, worktreeRepo string, err error) {
	// Physical path first: the macOS engine resolves it inside its VM, where an
	// unresolved /tmp mounts an empty directory silently.
	source, err = xpath.Resolve(folder)
	if err != nil {
		return "", "", "", "", usageError(err)
	}
	// A worktree's .git names its repository by absolute host path, so it is
	// mounted at the same path. Read from git, so a worktree made outside dev
	// is covered too.
	mount := dcgen.Mount{}
	repo, isWorktree := gitwt.WorktreeRepo(ctx, source)
	if mode == model.ModeIsolated {
		// Copied into a volume, never mounted, so there is no host path to
		// mount.
		mount = isolatedMount(wsName, name, isolatedPath(source))
		// Refused on k8s: a worktree's .git file names a host path the pod does
		// not have.
		if isWorktree {
			if err := a.requireLocalProvider(wsName); err != nil {
				return "", "", "", "", err
			}
		}
	} else if isWorktree {
		worktreeRepo = repo
		mount = worktreeMount(repo, source)
	}
	configPath, err = dcconfig.Find(source)
	switch {
	case err == nil && opts.generate:
		// Generating would shadow the project's own definition.
		return "", "", "", "", usageErrorf("%s already has a devcontainer config; --generate would shadow it",
			xpath.Shorten(source))
	case err != nil && !errors.Is(err, dcconfig.ErrNoConfig):
		return "", "", "", "", usageError(err)
	}

	if errors.Is(err, dcconfig.ErrNoConfig) {
		// No project config: generate one, kept in the database rather than
		// written into the folder. A zero Mount leaves the CLI's own bind mount
		// of the folder in place.
		generated, err = generatedConfigFor(name, opts.generate, opts.tools, mount,
			stateFor(!opts.noPersistState, wsName, name), os.Stdin, a.out)
		if err != nil {
			return "", "", "", "", err
		}
		if generated == "" {
			return "", "", "", "", usageErrorf("no devcontainer config in %s "+
				"(--generate builds a base Ubuntu one; dev container tools lists what it can add)",
				source)
		}
		configPath = ""
	}
	return source, configPath, generated, worktreeRepo, nil
}

// composeWarning says why a compose project will not get its state volume, or
// returns "". Compose implementations differ on applying `mounts`
// (devcontainers/spec#106), and nothing reports the omission. An unreadable
// config is silent: the commands that need it report it.
func composeWarning(configPath string, persistState bool) string {
	if configPath == "" || !persistState {
		return ""
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	compose, err := dcgen.UsesCompose(raw)
	if err != nil || !compose {
		return ""
	}
	return fmt.Sprintf("this project uses docker compose; the devcontainer CLI will not "+
		"apply dev's %s mount. Agent state will not survive a rebuild. Add the volume "+
		"to your compose file, or pass --no-persist-state.", dcgen.StateDir)
}

// --- list ---------------------------------------------------------------------

// defaultListTimeout bounds one workspace's engine call in `container list`.
const defaultListTimeout = 10 * time.Second

func newContainerListCmd(a *app) *cobra.Command {
	var (
		workspace string
		all       bool
		timeout   time.Duration
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List containers and their live status",
		Args:  noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContainerList(cmd.Context(), a, workspace, all, timeout)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	cmd.Flags().BoolVar(&all, "all", false, "list every workspace's containers")
	cmd.Flags().DurationVar(&timeout, "timeout", defaultListTimeout,
		"how long to wait for each workspace's statuses before printing ?")
	return cmd
}

func runContainerList(ctx context.Context, a *app, workspace string, all bool, timeout time.Duration) error {
	if timeout <= 0 {
		return usageErrorf("--timeout must be positive, got %s", timeout)
	}
	st, err := a.store()
	if err != nil {
		return err
	}

	scope := ""
	if !all {
		scope, err = a.workspaceName(workspace)
		if err != nil {
			return err
		}
	}

	containers, err := st.ListContainers(scope)
	if err != nil {
		return err
	}
	if len(containers) == 0 {
		a.printf("no containers; run: dev container create NAME --folder PATH (or --no-folder)\n")
		return nil
	}

	// One provider per workspace. Each workspace's deadline starts at its first
	// lookup, so one unreachable cluster costs one timeout.
	providers := map[string]provider.Provider{}
	deadlines := map[string]context.Context{}
	var cancels []context.CancelFunc
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()
	statusOf := func(c model.Container) string {
		p, ok := providers[c.WorkspaceName]
		if !ok {
			ws, err := st.GetWorkspace(c.WorkspaceName)
			if err != nil {
				return "?"
			}
			if p, err = a.providerFor(ws); err != nil {
				return "?"
			}
			providers[c.WorkspaceName] = p
		}
		wctx, ok := deadlines[c.WorkspaceName]
		if !ok {
			var cancel context.CancelFunc
			wctx, cancel = context.WithTimeout(ctx, timeout)
			deadlines[c.WorkspaceName] = wctx
			cancels = append(cancels, cancel)
		}
		s, err := statusWithin(wctx, p, c)
		if err != nil {
			// A slow or missing engine must not stop the list; the records are
			// the point.
			return "?"
		}
		return string(s)
	}

	// A dash, not an empty cell, which would read as a printing bug.
	sourceOf := func(c model.Container) string {
		if c.SourceKind == model.SourceNone {
			return "-"
		}
		return folderSummary(c)
	}

	// A folder path alone does not say whether dev generated the config.
	configOf := func(c model.Container) string {
		if c.GeneratedConfig != "" {
			return "generated"
		}
		return "project"
	}

	// Fixed at create and otherwise invisible; this column is the cheap way to
	// see it.
	return a.table(func(w io.Writer) {
		header(w, "WORKSPACE", "NAME", "STATUS", "SOURCE", "CONFIG", "STATE", "HOST", "MODE")
		for _, c := range containers {
			row(w, c.WorkspaceName, c.Name, statusOf(c), sourceOf(c), configOf(c), onOff(c.PersistState),
				hostAccessOf(c), modeOf(c))
		}
	})
}

// statusWithin returns at ctx's deadline even if Status has not: os/exec waits
// for stdout to close, and a child still holding it would hold the wait past
// the deadline.
func statusWithin(ctx context.Context, p provider.Provider, c model.Container) (model.Status, error) {
	if err := ctx.Err(); err != nil {
		return model.StatusAbsent, err
	}
	type result struct {
		s   model.Status
		err error
	}
	// Buffered, so the goroutine can exit after the caller has returned.
	done := make(chan result, 1)
	go func() {
		s, err := p.Status(ctx, c)
		done <- result{s, err}
	}()
	select {
	case r := <-done:
		return r.s, r.err
	case <-ctx.Done():
		return model.StatusAbsent, ctx.Err()
	}
}

// --- lifecycle ------------------------------------------------------------------

func newContainerStartCmd(a *app) *cobra.Command {
	var workspace string

	cmd := &cobra.Command{
		Use:   "start NAME",
		Short: "Start a container, creating it if the engine has none",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := a.resolve(cmd.Context(), workspace, args[0])
			if err != nil {
				return err
			}
			defer t.release()
			return a.start(cmd.Context(), t.workspace.Name, t.container)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	return cmd
}

// start is shared by `create` and `start`.
func (a *app) start(ctx context.Context, workspace string, c model.Container) error {
	warnLegacy(a, c)
	if err := requireFolders(c); err != nil {
		return err
	}
	st, err := a.store()
	if err != nil {
		return err
	}
	ws, err := st.GetWorkspace(workspace)
	if err != nil {
		return err
	}
	p, err := a.providerFor(ws)
	if err != nil {
		return err
	}
	environ, err := a.containerEnv(ctx, c)
	if err != nil {
		return err
	}

	// Create builds its own value and skips resolve, so the merged config is
	// written here too. Fatal, not deferred: starting without it leaves the
	// state volume unmounted.
	//
	// Seeded here, where a missing container is created, so the hooks copy
	// matches the project. Re-seeding a stopped one would discard what it
	// installed.
	plan, err := a.planGitGuard(ctx, c, c.WorktreeRepo, true, overridesConfig(p))
	if err != nil {
		return a.recordRefusal(c, err)
	}
	if overridesConfig(p) {
		c.GitGuardMounts = plan.mounts
	}
	// Stopped containers keep the mounts they were created with.
	if err := a.checkTreeMounts(ctx, p, c, plan); err != nil {
		return err
	}
	c, cleanup, err := materialise(c, overridesConfig(p))
	if err != nil {
		return err
	}
	defer cleanup()

	// Checked on every start, since a project's edited config applies to a
	// container that does not exist yet.
	cc, err := readConfig(ctx, p, c, plan)
	if err != nil {
		return err
	}
	if err := a.escape(cc); err != nil {
		return err
	}
	if err := p.Up(ctx, c, environ); err != nil {
		return err
	}
	if err := trustMountedRepository(ctx, p, c); err != nil {
		return err
	}
	trustGitHub(ctx, a, p, c)
	// A row with no digest takes the one just started from. Never overwritten
	// here: rebuild is what catches an edited document.
	if cc != nil && c.ConfigDigest == "" {
		if err := a.recordFirstDigest(cc); err != nil {
			return err
		}
	}
	a.record("start", workspace, c.Name, nil)
	a.printf("container %s is running\n", c.Name)

	// Only when owed: a plain start must not re-apply edits that wait for a
	// rebuild.
	if !c.AgentConfigPending {
		return nil
	}
	spec, err := loadAgentConfig(c)
	if err != nil {
		return err
	}
	return a.applyAgentConfig(ctx, p, c, environ, spec)
}

func newContainerStopCmd(a *app) *cobra.Command {
	var workspace string

	cmd := &cobra.Command{
		Use:   "stop NAME",
		Short: "Stop a running container",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := a.resolve(cmd.Context(), workspace, args[0])
			if err != nil {
				return err
			}
			defer t.release()
			if err := t.provider.Stop(cmd.Context(), t.container); err != nil {
				return err
			}
			a.record("stop", t.workspace.Name, t.container.Name, nil)
			a.printf("container %s stopped\n", t.container.Name)
			return nil
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	return cmd
}

func newContainerRemoveCmd(a *app) *cobra.Command {
	var (
		workspace string
		force     bool
	)

	cmd := &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove a container and forget it",
		Long: "Remove a container and forget it.\n\n" +
			"The project folder is never touched. If the engine refuses to remove\n" +
			"the container the record is kept, so the command can be retried;\n" +
			"--force drops the record anyway, which is how an orphan left by a\n" +
			"vanished engine is cleaned up.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContainerRemove(cmd.Context(), a, workspace, args[0], force)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	cmd.Flags().BoolVar(&force, "force", false, "forget the container even if the engine removal fails")
	return cmd
}

func runContainerRemove(ctx context.Context, a *app, workspace, name string, force bool) error {
	t, err := a.resolve(ctx, workspace, name)
	if err != nil {
		return err
	}
	defer t.release()

	// An isolated volume is the only copy of its work; anything the folder
	// lacks refuses the removal unless --force is given.
	checked := t.container.Mode == model.ModeIsolated && !force
	if checked {
		found, err := unexported(ctx, t)
		if err != nil {
			return fmt.Errorf("%w; --force removes it without checking", err)
		}
		if len(found) > 0 {
			return usageErrorf("container %s holds work its folders do not keep:\n  %s\n"+
				"bring the branches home with dev container fetch %s and merge or push them, "+
				"commit inside first for uncommitted changes, or pass --force to remove it and lose them",
				name, strings.Join(found, "\n  "), name)
		}
	}

	if err := t.provider.Remove(ctx, t.container); err != nil {
		if !force {
			return err
		}
		warnf(a, "%v (forgetting the record anyway)", err)
	}

	st, err := a.store()
	if err != nil {
		return err
	}

	// The checkout and its record belong to the worktree; `dev worktree remove`
	// takes them.
	if err := st.DeleteContainer(t.workspace.Name, name); err != nil {
		return err
	}
	removeHooksCopy(t.workspace.Name, name)
	// Only once the check has shown every branch is kept by another ref: after
	// --force, the fetched refs may be the last copy of the work.
	if checked {
		for _, f := range isolatedFolders(t.container) {
			if err := dropFetchedRefs(ctx, f.host, name); err != nil {
				warnf(a, "leaving the refs dev container fetch wrote under %s in %s: %v",
					fetchedRefs(name), xpath.Shorten(f.host), err)
			}
		}
	}
	a.record("remove", t.workspace.Name, name, nil)
	a.printf("container %s removed\n", name)
	return nil
}

func newContainerRebuildCmd(a *app) *cobra.Command {
	var (
		workspace    string
		noCache      bool
		toolList     string
		acceptConfig bool
		addFolders   []string
		dropFolders  []string
		mode         model.Mode
	)

	cmd := &cobra.Command{
		Use:   "rebuild NAME",
		Short: "Recreate a container from its configuration",
		Long: "Recreate a container from its configuration.\n\n" +
			"--add-folder and --remove-folder change a guarded or unprotected\n" +
			"container's extra folders; each is repeatable. An isolated container's\n" +
			"folders, and any container's primary folder, are fixed: recreate the\n" +
			"container instead.\n\n" +
			"--mode changes the container's mode. A guarded container can become\n" +
			"unprotected or isolated, and an unprotected one guarded or isolated.\n" +
			"Becoming isolated copies the folders into volumes and cannot be undone:\n" +
			"bring the work home with dev container fetch, then create a new container.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if tools := parseToolList(toolList); len(tools) > 0 {
				// Before resolve, so the rebuild materialises the new
				// configuration.
				if err := rewriteGeneratedTools(cmd.Context(), a, workspace, args[0], tools); err != nil {
					return err
				}
			}

			t, err := a.resolve(cmd.Context(), workspace, args[0])
			if err != nil {
				return err
			}
			defer func() { t.release() }()
			from := t.container.Mode
			to := mode
			if to == "" {
				to = from
			}
			kind, err := a.providerKind(t.workspace.Name)
			if err != nil {
				return err
			}
			if mode != "" {
				if err := checkModeChange(t.container.Name, from, to, kind); err != nil {
					return err
				}
			}
			modeChanged := to != from
			changing := len(addFolders) > 0 || len(dropFolders) > 0
			var next model.Container
			if changing || modeChanged {
				// Planned and materialised again from the resulting set, so the
				// rebuilt container has every added folder's guard from its
				// first command.
				if t, next, err = a.retarget(cmd.Context(), t, mode, addFolders, dropFolders); err != nil {
					return err
				}
			}
			if err := t.requireOverride(a); err != nil {
				return err
			}
			if err := requireFolders(t.container); err != nil {
				return err
			}
			environ, err := a.containerEnv(cmd.Context(), t.container)
			if err != nil {
				return err
			}
			// Loaded before the rebuild, so a bad file fails while the old
			// container still exists.
			spec, err := loadAgentConfig(t.container)
			if err != nil {
				return err
			}
			// Checked before the rebuild, so a refusal leaves the old container
			// in place. Drift first: --accept-config is the operator's review
			// of that. An escape refusal needs recreate, since only create
			// grants --allow-privileged.
			cc, err := readConfig(cmd.Context(), t.provider, t.container, t.gitGuard)
			if err != nil {
				return err
			}
			digest, err := a.checkDrift(cc, acceptConfig)
			if err != nil {
				return err
			}
			if err := a.escape(cc); err != nil {
				return err
			}
			// Only resets the hooks copy; resolve already planned the mounts
			// the rebuilt container gets.
			if _, _, err := a.planGitDir(t.container, t.container.WorktreeRepo, true); err != nil {
				return err
			}
			seeding := modeChanged && to == model.ModeIsolated
			rebuildErr := t.provider.Rebuild(cmd.Context(), t.container, environ,
				provider.RebuildOpts{NoCache: noCache, Seed: seeding})
			// A hook failure after seeding leaves the container recreated and
			// seeded, so the row must follow it.
			var lifecycle *provider.LifecycleError
			recreated := rebuildErr == nil || (seeding && errors.As(rebuildErr, &lifecycle))
			if !recreated {
				return rebuildErr
			}
			// After a successful rebuild only, so a failed one leaves the
			// record matching the container that still runs.
			if changing || modeChanged {
				st, err := a.store()
				if err != nil {
					return err
				}
				if modeChanged {
					err = st.SetContainerMode(next)
				} else {
					err = st.SetContainerFolders(t.workspace.Name, t.container.Name, t.container.Folders)
				}
				if err != nil {
					return err
				}
			}
			if rebuildErr != nil {
				fields := map[string]any{"mode_from": string(from), "mode_to": string(to)}
				a.record("rebuild", t.workspace.Name, t.container.Name, fields)
				if to == model.ModeUnprotected {
					warnUnprotected(a, t.container.Name)
				}
				return rebuildErr
			}
			if err := trustMountedRepository(cmd.Context(), t.provider, t.container); err != nil {
				return err
			}
			trustGitHub(cmd.Context(), a, t.provider, t.container)
			// After a successful rebuild only: the digest must describe the
			// configuration the container has.
			if cc != nil && digest.Sum != t.container.ConfigDigest {
				st, err := a.store()
				if err != nil {
					return err
				}
				if err := st.SetConfigDigest(t.workspace.Name, t.container.Name, digest.Sum, digest.Record()); err != nil {
					return err
				}
			}
			fields := map[string]any{"config_digest": digest.Sum}
			if changing {
				hosts := make([]string, len(t.container.Folders))
				for i, f := range t.container.Folders {
					hosts[i] = f.HostPath
				}
				fields["folders"] = hosts
			}
			if modeChanged {
				fields["mode_from"], fields["mode_to"] = string(from), string(to)
			}
			a.record("rebuild", t.workspace.Name, t.container.Name, fields)
			if modeChanged && to == model.ModeUnprotected {
				warnUnprotected(a, t.container.Name)
			}

			a.printf("container %s rebuilt\n", t.container.Name)
			// Re-read on every rebuild: the state volume may be gone, taking
			// the last apply with it.
			return a.applyAgentConfig(cmd.Context(), t.provider, t.container, environ, spec)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	cmd.Flags().BoolVar(&noCache, "no-cache", false, "rebuild the image without the layer cache")
	cmd.Flags().StringVar(&toolList, "tools", "",
		"change a generated container's tools, e.g. +yq,-helm (see: dev container tools)")
	cmd.Flags().BoolVar(&acceptConfig, "accept-config", false,
		"rebuild with a project configuration that changed since the container was last built")
	cmd.Flags().StringArrayVar(&addFolders, "add-folder", nil,
		"add a folder to a guarded or unprotected container; repeatable")
	cmd.Flags().StringArrayVar(&dropFolders, "remove-folder", nil,
		"remove a folder from a guarded or unprotected container; repeatable")
	cmd.Flags().Var(modeFlag{&mode}, "mode",
		"change the container's mode: guarded, unprotected, or isolated (one way)")
	return cmd
}

func newContainerLogsCmd(a *app) *cobra.Command {
	var (
		workspace string
		follow    bool
	)

	cmd := &cobra.Command{
		Use:   "logs NAME",
		Short: "Show a container's logs",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := a.resolve(cmd.Context(), workspace, args[0])
			if err != nil {
				return err
			}
			defer t.release()
			return t.provider.Logs(cmd.Context(), t.container, follow, a.out)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new output")
	return cmd
}

func newContainerSyncCmd(a *app) *cobra.Command {
	var workspace string

	cmd := &cobra.Command{
		Use:   "sync NAME",
		Short: "Push the workspace's settings into a container",
		Long: "Push the workspace's settings into a container.\n\n" +
			"Your files are never touched. A container an agent has been working in\n" +
			"holds the only copy of what it has done, and no command should overwrite\n" +
			"that.\n\n" +
			"Nothing to do on the local provider, which resolves the settings on every\n" +
			"command. On k8s it refreshes the Secret the pod is built from, so a pod\n" +
			"the cluster replaces on its own — an eviction, a drain — comes back with\n" +
			"current values rather than the ones it was created with. The running pod\n" +
			"keeps the environment it started with either way.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContainerSync(cmd.Context(), a, workspace, args[0])
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	return cmd
}

func runContainerSync(ctx context.Context, a *app, workspace, name string) error {
	t, err := a.resolve(ctx, workspace, name)
	if err != nil {
		return err
	}
	defer t.release()

	syncer, ok := t.provider.(provider.Syncer)
	if !ok {
		return usageErrorf("provider %s cannot sync settings", t.workspace.ProviderName)
	}
	if err := a.requireRunning(ctx, t); err != nil {
		return err
	}
	// The environment up builds, so the two cannot disagree.
	environ, err := a.containerEnv(ctx, t.container)
	if err != nil {
		return err
	}

	if err := syncer.Sync(ctx, t.container, environ); err != nil {
		return err
	}
	// Keys only, never values, sorted so the output is stable across runs.
	keys := make([]string, 0, len(environ))
	for _, e := range environ {
		keys = append(keys, e.Key)
	}
	slices.Sort(keys)
	a.record("sync", t.workspace.Name, t.container.Name, map[string]any{"settings": keys})
	// Says what is true afterwards, not what was done. The local provider
	// pushed nothing.
	a.printf("%d settings current in %s: %s\n", len(keys), name, strings.Join(keys, ", "))
	return nil
}

// --- shell and exec ---------------------------------------------------------------

func newContainerShellCmd(a *app) *cobra.Command {
	var (
		workspace string
		kubeToken bool
		sshAgent  sshAgentFlags
	)

	cmd := &cobra.Command{
		Use:   "shell NAME",
		Short: "Open an interactive shell in a container",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sshOverride, err := sshAgent.override()
			if err != nil {
				return err
			}
			return runContainerShell(cmd.Context(), a, workspace, args[0], kubeToken, sshOverride)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	addKubeTokenFlag(cmd, &kubeToken)
	addSSHAgentFlags(cmd, &sshAgent)
	return cmd
}

func runContainerShell(ctx context.Context, a *app, workspace, name string, kubeToken bool, sshOverride *bool) error {
	t, err := a.resolve(ctx, workspace, name)
	if err != nil {
		return err
	}
	defer t.release()
	if err := t.requireOverride(a); err != nil {
		return err
	}
	if err := a.requireRunning(ctx, t); err != nil {
		return err
	}
	environ, err := a.containerEnv(ctx, t.container)
	if err != nil {
		return err
	}
	req, err := askKubeTokenIf(ctx, kubeToken)
	if err != nil {
		return err
	}
	environ, err = a.applyKubeToken(ctx, t, req, environ)
	if err != nil {
		return err
	}
	environ, stopAgent, err := a.forwardAgent(ctx, t, sshOverride, environ)
	if err != nil {
		return err
	}
	defer stopAgent()

	shell := detectShell(ctx, t)
	started := time.Now()
	err = a.guardedRun(ctx, t, func() error {
		return t.provider.Exec(ctx, t.container, []string{shell}, provider.ExecOpts{
			Env:    environ,
			TTY:    true,
			Stdin:  os.Stdin,
			Stdout: os.Stdout,
			Stderr: os.Stderr,
		})
	})
	a.recordRun("shell", t, started, map[string]any{
		"argv":       []string{shell},
		"ssh_agent":  agentForwarded(t, sshOverride),
		"kube_token": kubeToken,
	}, err)
	return err
}

// detectShell asks the image which shell it has: an absent zsh fails with the
// engine's own error, which this tool cannot turn into a retry.
func detectShell(ctx context.Context, t *target) string {
	const fallback = "sh"

	var out strings.Builder
	err := t.provider.Exec(ctx, t.container,
		[]string{"sh", "-c", "command -v zsh || command -v bash || command -v sh"},
		provider.ExecOpts{Stdout: &out, Stderr: io.Discard})
	if err != nil {
		return fallback
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return fallback
}

func newContainerExecCmd(a *app) *cobra.Command {
	var (
		workspace string
		kubeToken bool
		sshAgent  sshAgentFlags
	)

	cmd := &cobra.Command{
		Use:   "exec NAME -- COMMAND [ARGS...]",
		Short: "Run one command in a container",
		Args:  minArgs(1),
		// Flags after -- belong to the inner command, not dev.
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dash := cmd.ArgsLenAtDash()
			if dash < 0 {
				return usageErrorf("no command given; use: dev container exec %s -- CMD [ARGS...]", args[0])
			}
			sshOverride, err := sshAgent.override()
			if err != nil {
				return err
			}
			return runContainerExec(cmd.Context(), a, workspace, args[0], args[dash:], kubeToken, sshOverride)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	addKubeTokenFlag(cmd, &kubeToken)
	addSSHAgentFlags(cmd, &sshAgent)
	return cmd
}

func runContainerExec(ctx context.Context, a *app, workspace, name string, command []string,
	kubeToken bool, sshOverride *bool) error {
	if len(command) == 0 {
		return usageErrorf("no command given; use: dev container exec %s -- CMD [ARGS...]", name)
	}

	t, err := a.resolve(ctx, workspace, name)
	if err != nil {
		return err
	}
	defer t.release()
	if err := t.requireOverride(a); err != nil {
		return err
	}
	if err := a.requireRunning(ctx, t); err != nil {
		return err
	}
	environ, err := a.containerEnv(ctx, t.container)
	if err != nil {
		return err
	}
	req, err := askKubeTokenIf(ctx, kubeToken)
	if err != nil {
		return err
	}
	environ, err = a.applyKubeToken(ctx, t, req, environ)
	if err != nil {
		return err
	}
	environ, stopAgent, err := a.forwardAgent(ctx, t, sshOverride, environ)
	if err != nil {
		return err
	}
	defer stopAgent()

	started := time.Now()
	err = a.guardedRun(ctx, t, func() error {
		return t.provider.Exec(ctx, t.container, command, provider.ExecOpts{
			Env:    environ,
			Stdin:  os.Stdin,
			Stdout: os.Stdout,
			Stderr: os.Stderr,
		})
	})
	// argv only: environ holds resolved settings and must not reach the log.
	a.recordRun("exec", t, started, map[string]any{
		"argv":       command,
		"ssh_agent":  agentForwarded(t, sshOverride),
		"kube_token": kubeToken,
	}, err)
	return err
}
