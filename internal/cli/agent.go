package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/duy0611/dev-cli/internal/agent"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newContainerAgentCmd(a *app) *cobra.Command {
	var (
		workspace string
		agentID   string
	)

	cmd := &cobra.Command{
		Use:   "agent NAME [-- ARGS...]",
		Short: "Run a coding agent inside a container",
		Long: "Run a coding agent inside a container, starting it first if needed.\n\n" +
			"The agent has to be installed in the image already. This tool does not\n" +
			"edit a project's devcontainer.json, so installing one means adding it\n" +
			"there and rebuilding.",
		Args:                  minArgs(1),
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var extra []string
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				extra = args[dash:]
			}
			return runContainerAgent(cmd.Context(), a, workspace, args[0], agentID, extra)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	cmd.Flags().StringVar(&agentID, "agent", "claude",
		"which agent to run ("+strings.Join(agent.IDs(), ", ")+")")
	return cmd
}

func runContainerAgent(ctx context.Context, a *app, workspace, name, agentID string, extra []string) error {
	ag, err := agent.Lookup(agentID)
	if err != nil {
		return usageError(err)
	}

	t, err := a.resolve(workspace, name)
	if err != nil {
		return err
	}
	defer t.release()

	environ, err := a.containerEnv(ctx, t.workspace.Name)
	if err != nil {
		return err
	}

	// The command starts the container rather than refusing: asking for an
	// agent is asking for a working container.
	status, err := t.provider.Status(ctx, t.container)
	if err != nil {
		return err
	}
	if status != model.StatusRunning {
		if err := t.provider.Up(ctx, t.container, environ); err != nil {
			return err
		}
	}

	if err := a.ensureAgentPresent(ctx, t, ag, environ); err != nil {
		return err
	}

	// After the container is up and after any rebuild: the relay runs inside
	// the container, so it has nothing to attach to before the first, and a
	// rebuild would take the running relay down with the old container.
	environ, stopAgent, err := a.forwardAgent(ctx, t, environ)
	if err != nil {
		return err
	}
	defer stopAgent()

	// Herdr classifies a pane by inspecting the foreground process it can see
	// on the host, not anything inside the container — its own docs say so
	// plainly: "Herdr cannot see it if you set it only inside a VM or
	// container." `dev` is the process sitting in the pane, so this is the
	// only copy of the variable that can reach it.
	if err := os.Setenv("HERDR_AGENT", ag.ID); err != nil {
		return err
	}

	return t.provider.Exec(ctx, t.container, ag.Command(extra), provider.ExecOpts{
		Env:    environ,
		TTY:    true,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	})
}

// ensureAgentPresent checks the image actually has the agent, offering one
// rebuild if it does not.
func (a *app) ensureAgentPresent(ctx context.Context, t *target, ag agent.Agent, environ []provider.EnvVar) error {
	if agentInstalled(ctx, t, ag) {
		return nil
	}

	a.printf("agent %q is not installed in %s's image.\n", ag.ID, t.container.Name)
	if !confirm(os.Stdin, a.out, "rebuild the container and look again?") {
		// The rebuild only helps if the agent is already declared in the
		// project's own devcontainer.json, so say where the fix lives rather
		// than implying this tool can apply it.
		return fmt.Errorf("add %s to %s and rebuild", ag.Binary, t.container.ConfigPath)
	}

	if err := t.provider.Rebuild(ctx, t.container, environ, false); err != nil {
		return err
	}
	if !agentInstalled(ctx, t, ag) {
		return fmt.Errorf("%s is still missing after the rebuild; add it to %s",
			ag.Binary, t.container.ConfigPath)
	}
	return nil
}

// agentInstalled reports whether the binary is on PATH inside the container.
//
// `command -v`, not `which`: it is a shell builtin, so it works in the many
// images that ship no which(1) at all.
func agentInstalled(ctx context.Context, t *target, ag agent.Agent) bool {
	err := t.provider.Exec(ctx, t.container,
		[]string{"sh", "-c", "command -v " + ag.Binary},
		provider.ExecOpts{Stdout: io.Discard, Stderr: io.Discard})
	return err == nil
}

// confirm asks a yes/no question.
//
// Returns false without asking when stdin is not a terminal, so a scripted run
// fails with the explanation rather than hanging on a prompt nobody will see.
func confirm(in *os.File, out io.Writer, question string) bool {
	if !isTerminal(in) {
		return false
	}
	fmt.Fprintf(out, "%s [y/N] ", question)

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// isTerminal reports whether f is an interactive terminal.
//
// Not a ModeCharDevice check: /dev/null is a character device too, so
// `dev container agent ... < /dev/null` would read as interactive and print a
// prompt into a run that can never answer it.
func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}
