package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/dcconfig"
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
	)
	return cmd
}

// --- create -------------------------------------------------------------------

func newContainerCreateCmd(a *app) *cobra.Command {
	var (
		workspace string
		folder    string
		noStart   bool
	)

	cmd := &cobra.Command{
		Use:   "create NAME",
		Short: "Create a container from a folder and start it",
		Long: "Create a container from a folder and start it.\n\n" +
			"The folder must ship its own .devcontainer configuration. This tool\n" +
			"never writes one: the project owns its container definition.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContainerCreate(cmd.Context(), a, workspace, args[0], folder, noStart)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	cmd.Flags().StringVar(&folder, "folder", "", "host folder holding the project (required)")
	cmd.Flags().BoolVar(&noStart, "no-start", false, "record the container without starting it")
	return cmd
}

func runContainerCreate(ctx context.Context, a *app, workspace, name, folder string, noStart bool) error {
	if err := xpath.ValidateName(name); err != nil {
		return usageError(err)
	}
	if folder == "" {
		return usageErrorf("--folder is required")
	}

	// Physical path, before anything stores or mounts it: the engine resolves
	// the string inside its VM on macOS, where /tmp is a real directory rather
	// than a symlink to /private/tmp, so an unresolved path mounts an empty
	// directory with no error to show for it.
	source, err := xpath.Resolve(folder)
	if err != nil {
		return usageError(err)
	}
	configPath, err := dcconfig.Find(source)
	if err != nil {
		return usageError(err)
	}

	wsName, err := a.workspaceName(workspace)
	if err != nil {
		return err
	}
	st, err := a.store()
	if err != nil {
		return err
	}

	c := model.Container{
		Name:          name,
		WorkspaceName: wsName,
		SourceKind:    model.SourceFolder,
		Source:        source,
		ConfigPath:    configPath,
	}
	err = st.CreateContainer(c)
	if errors.Is(err, store.ErrExists) {
		return usageErrorf("container %s already exists in workspace %s", name, wsName)
	}
	if err != nil {
		return err
	}
	a.printf("container %s (workspace %s) from %s\n", name, wsName, xpath.Shorten(source))

	if noStart {
		return nil
	}
	return a.start(ctx, wsName, c)
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
	)

	cmd := &cobra.Command{
		Use:   "rebuild NAME",
		Short: "Recreate a container from its configuration",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := a.resolve(workspace, args[0])
			if err != nil {
				return err
			}
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
			return t.provider.Logs(cmd.Context(), t.container, follow, a.out)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new output")
	return cmd
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
