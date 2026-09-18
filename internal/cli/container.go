package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/dcconfig"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/dcgen"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/model"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/provider"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/store"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/xpath"
	"github.com/spf13/cobra"
)

func newContainerCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "container",
		Short: "Create and run devcontainers",
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
		newContainerAgentCmd(a),
		newContainerSyncCmd(a),
		newContainerToolsCmd(a),
		newContainerConfigCmd(a),
	)
	return cmd
}

// --- create -------------------------------------------------------------------

// createOpts is what `container create` was asked for beyond the name and the
// folder. A struct rather than more parameters: the list grows, and six
// positional booleans at a call site say nothing about which is which.
type createOpts struct {
	noStart  bool
	noFolder bool
	generate bool
	tools    []string
}

func newContainerCreateCmd(a *app) *cobra.Command {
	var (
		workspace string
		folder    string
		toolList  string
		opts      createOpts
	)

	cmd := &cobra.Command{
		Use:   "create NAME",
		Short: "Create a container and start it",
		Long: "Create a container and start it.\n\n" +
			"With --folder, a project that ships its own .devcontainer configuration\n" +
			"is used as it is; dev never edits it. For a folder with none, --generate\n" +
			"builds a base Ubuntu configuration from the tools --tools names.\n\n" +
			"With --no-folder there is no host directory at all: the container's work\n" +
			"lives in a volume dev creates and removes with it. That configuration is\n" +
			"always generated, and kept in dev's own database.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.tools = parseToolList(toolList)
			return runContainerCreate(cmd.Context(), a, workspace, args[0], folder, opts)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	cmd.Flags().StringVar(&folder, "folder", "", "host folder holding the project (required)")
	cmd.Flags().BoolVar(&opts.noStart, "no-start", false, "record the container without starting it")
	cmd.Flags().BoolVar(&opts.noFolder, "no-folder", false,
		"create a container with no host folder; its work lives in a volume dev owns")
	cmd.Flags().BoolVar(&opts.generate, "generate", false,
		"generate a base Ubuntu configuration when the folder ships none")
	cmd.Flags().StringVar(&toolList, "tools", "",
		"comma-separated tools to install in a generated container (see: dev container tools)")
	return cmd
}

func runContainerCreate(ctx context.Context, a *app, workspace, name, folder string, opts createOpts) error {
	if err := xpath.ValidateName(name); err != nil {
		return usageError(err)
	}
	switch {
	case folder == "" && !opts.noFolder:
		// Guessing either way is expensive: a forgotten --folder would build an
		// empty sandbox, which is only noticed when the agent cannot find the
		// project.
		return usageErrorf("give --folder PATH, or --no-folder for a container with no host folder")
	case folder != "" && opts.noFolder:
		return usageErrorf("--folder and --no-folder contradict each other")
	}
	if len(opts.tools) > 0 && !opts.generate && !opts.noFolder {
		return usageErrorf("--tools only applies with --generate")
	}

	wsName, err := a.workspaceName(workspace)
	if err != nil {
		return err
	}

	var (
		source     string
		configPath string
		generated  string
	)
	if opts.noFolder {
		// No project, so nothing can ship a configuration and there is nothing
		// for --generate to shadow: a folderless container is always generated.
		generated, err = generatedConfigFor(name, true, opts.tools,
			folderlessMount(wsName, name), os.Stdin, a.out)
		if err != nil {
			return err
		}
	} else {
		source, configPath, generated, err = folderSource(name, folder, opts, a)
		if err != nil {
			return err
		}
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
		Name:          name,
		WorkspaceName: wsName,
		SourceKind:    kind,
		Source:        source,
		ConfigPath:    configPath,
		// Exactly one of these two is set: a project-owned container has a
		// path, a generated one has the document itself.
		GeneratedConfig: generated,
	}
	err = st.CreateContainer(c)
	if errors.Is(err, store.ErrExists) {
		return usageErrorf("container %s already exists in workspace %s", name, wsName)
	}
	if err != nil {
		return err
	}
	if opts.noFolder {
		a.printf("container %s (workspace %s) with no folder\n", name, wsName)
	} else {
		a.printf("container %s (workspace %s) from %s\n", name, wsName, xpath.Shorten(source))
	}

	if opts.noStart {
		return nil
	}
	return a.start(ctx, wsName, c)
}

// folderSource works out what a --folder container is built from: the resolved
// path, the project's own config if it has one, and a generated document if it
// does not.
func folderSource(name, folder string, opts createOpts, a *app) (source, configPath, generated string, err error) {
	// Physical path, before anything stores or mounts it: the engine resolves
	// the string inside its VM on macOS, where /tmp is a real directory rather
	// than a symlink to /private/tmp, so an unresolved path mounts an empty
	// directory with no error to show for it.
	source, err = xpath.Resolve(folder)
	if err != nil {
		return "", "", "", usageError(err)
	}
	configPath, err = dcconfig.Find(source)
	switch {
	case err == nil && opts.generate:
		// The project ships one. Generating a second would shadow the
		// definition the project owns, with no way to tell from the outside
		// which of the two built the container.
		return "", "", "", usageErrorf("%s already has a devcontainer config; --generate would shadow it",
			xpath.Shorten(source))
	case err != nil && !errors.Is(err, dcconfig.ErrNoConfig):
		return "", "", "", usageError(err)
	}

	if errors.Is(err, dcconfig.ErrNoConfig) {
		// A folder with no configuration of its own is not the end of the
		// road: dev can render one, and keeps it in the database rather than
		// writing into a project it does not own. The zero Mount leaves the
		// CLI's own bind mount of the folder in place.
		generated, err = generatedConfigFor(name, opts.generate, opts.tools, dcgen.Mount{}, os.Stdin, a.out)
		if err != nil {
			return "", "", "", err
		}
		if generated == "" {
			return "", "", "", usageErrorf("no devcontainer config in %s "+
				"(--generate builds a base Ubuntu one; dev container tools lists what it can add)",
				source)
		}
		configPath = ""
	}
	return source, configPath, generated, nil
}

// --- list ---------------------------------------------------------------------

func newContainerListCmd(a *app) *cobra.Command {
	var (
		workspace string
		all       bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List containers and their live status",
		Args:  noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContainerList(cmd.Context(), a, workspace, all)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	cmd.Flags().BoolVar(&all, "all", false, "list every workspace's containers")
	return cmd
}

func runContainerList(ctx context.Context, a *app, workspace string, all bool) error {
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
		a.printf("no containers; run: dev container create NAME --folder PATH\n")
		return nil
	}

	// One provider per workspace, built once: every row otherwise rebuilds the
	// same one.
	providers := map[string]provider.Provider{}
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
		s, err := p.Status(ctx, c)
		if err != nil {
			// A missing engine should not stop the list from printing what it
			// knows: the records are the point, the status is the extra.
			return "?"
		}
		return string(s)
	}

	return a.table(func(w io.Writer) {
		header(w, "WORKSPACE", "NAME", "STATUS", "SOURCE")
		for _, c := range containers {
			row(w, c.WorkspaceName, c.Name, statusOf(c), xpath.Shorten(c.Source))
		}
	})
}

// --- lifecycle ------------------------------------------------------------------

func newContainerStartCmd(a *app) *cobra.Command {
	var workspace string

	cmd := &cobra.Command{
		Use:   "start NAME",
		Short: "Start a container, creating it if the engine has none",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := a.resolve(workspace, args[0])
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
	environ, err := a.containerEnv(ctx, workspace)
	if err != nil {
		return err
	}

	// The create path builds its own container value and never goes through
	// resolve, so a generated configuration has to become a file here too.
	c, cleanup, err := materialise(c)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := p.Up(ctx, c, environ); err != nil {
		return err
	}
	a.printf("container %s is running\n", c.Name)
	return nil
}

func newContainerStopCmd(a *app) *cobra.Command {
	var workspace string

	cmd := &cobra.Command{
		Use:   "stop NAME",
		Short: "Stop a running container",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := a.resolve(workspace, args[0])
			if err != nil {
				return err
			}
			defer t.release()
			if err := t.provider.Stop(cmd.Context(), t.container); err != nil {
				return err
			}
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
	t, err := a.resolve(workspace, name)
	if err != nil {
		return err
	}
	defer t.release()

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
	if err := st.DeleteContainer(t.workspace.Name, name); err != nil {
		return err
	}
	a.printf("container %s removed\n", name)
	return nil
}

func newContainerRebuildCmd(a *app) *cobra.Command {
	var (
		workspace string
		noCache   bool
		toolList  string
	)

	cmd := &cobra.Command{
		Use:   "rebuild NAME",
		Short: "Recreate a container from its configuration",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if tools := parseToolList(toolList); len(tools) > 0 {
				// Before resolve, so the rebuild materialises the new
				// configuration rather than the one being replaced.
				if err := rewriteGeneratedTools(a, workspace, args[0], tools); err != nil {
					return err
				}
			}

			t, err := a.resolve(workspace, args[0])
			if err != nil {
				return err
			}
			defer t.release()
			environ, err := a.containerEnv(cmd.Context(), t.workspace.Name)
			if err != nil {
				return err
			}
			if err := t.provider.Rebuild(cmd.Context(), t.container, environ, noCache); err != nil {
				return err
			}
			a.printf("container %s rebuilt\n", t.container.Name)
			return nil
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	cmd.Flags().BoolVar(&noCache, "no-cache", false, "rebuild the image without the layer cache")
	cmd.Flags().StringVar(&toolList, "tools", "",
		"change a generated container's tools, e.g. +yq,-helm (see: dev container tools)")
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
			t, err := a.resolve(workspace, args[0])
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
		Short: "Copy the host folder into a remote container",
		Long: "Copy the host folder into a remote container.\n\n" +
			"One direction, and only when asked: once an agent is working in the\n" +
			"container, its copy is the live one, and overwriting that on a timer\n" +
			"would destroy work nobody asked to discard.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContainerSync(cmd.Context(), a, workspace, args[0])
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	return cmd
}

func runContainerSync(ctx context.Context, a *app, workspace, name string) error {
	t, err := a.resolve(workspace, name)
	if err != nil {
		return err
	}
	defer t.release()

	syncer, ok := t.provider.(provider.Syncer)
	if !ok {
		// The local provider bind-mounts the folder: the container is already
		// looking at the same files.
		return usageErrorf("provider %s mounts the folder directly; there is nothing to sync",
			t.workspace.ProviderName)
	}
	if err := a.requireRunning(ctx, t); err != nil {
		return err
	}

	if err := syncer.Sync(ctx, t.container); err != nil {
		return err
	}
	a.printf("synced %s into %s\n", xpath.Shorten(t.container.Source), name)
	return nil
}

// --- shell and exec ---------------------------------------------------------------

func newContainerShellCmd(a *app) *cobra.Command {
	var workspace string

	cmd := &cobra.Command{
		Use:   "shell NAME",
		Short: "Open an interactive shell in a container",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContainerShell(cmd.Context(), a, workspace, args[0])
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	return cmd
}

func runContainerShell(ctx context.Context, a *app, workspace, name string) error {
	t, err := a.resolve(workspace, name)
	if err != nil {
		return err
	}
	defer t.release()
	if err := a.requireRunning(ctx, t); err != nil {
		return err
	}
	environ, err := a.containerEnv(ctx, t.workspace.Name)
	if err != nil {
		return err
	}

	shell := detectShell(ctx, t)
	return t.provider.Exec(ctx, t.container, []string{shell}, provider.ExecOpts{
		Env:    environ,
		TTY:    true,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	})
}

// detectShell picks the best shell the image actually has.
//
// Asked rather than attempted: starting zsh in an image without it produces the
// container's own "executable file not found" rather than anything this tool
// can turn into a retry, and plenty of project images ship only bash or sh.
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
	var workspace string

	cmd := &cobra.Command{
		Use:   "exec NAME -- COMMAND [ARGS...]",
		Short: "Run one command in a container",
		Args:  minArgs(1),
		// Everything after -- belongs to the command being run, not to dev.
		// Without this, a flag meant for the inner command is parsed here.
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dash := cmd.ArgsLenAtDash()
			if dash < 0 {
				return usageErrorf("no command given; use: dev container exec %s -- CMD [ARGS...]", args[0])
			}
			return runContainerExec(cmd.Context(), a, workspace, args[0], args[dash:])
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	return cmd
}

func runContainerExec(ctx context.Context, a *app, workspace, name string, command []string) error {
	if len(command) == 0 {
		return usageErrorf("no command given; use: dev container exec %s -- CMD [ARGS...]", name)
	}

	t, err := a.resolve(workspace, name)
	if err != nil {
		return err
	}
	defer t.release()
	if err := a.requireRunning(ctx, t); err != nil {
		return err
	}
	environ, err := a.containerEnv(ctx, t.workspace.Name)
	if err != nil {
		return err
	}

	return t.provider.Exec(ctx, t.container, command, provider.ExecOpts{
		Env:    environ,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	})
}
