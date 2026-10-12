package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/duy0611/dev-cli/internal/agent"
	"github.com/duy0611/dev-cli/internal/herdr"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newContainerStartAgentCmd(a *app) *cobra.Command {
	var (
		workspace string
		agentID   string
		kubeToken bool
		sshAgent  sshAgentFlags
	)

	cmd := &cobra.Command{
		Use:   "start-agent NAME [-- ARGS...]",
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
			sshOverride, err := sshAgent.override()
			if err != nil {
				return err
			}
			return runContainerStartAgent(cmd.Context(), a, workspace, args[0], agentID, extra, kubeToken, sshOverride)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	addKubeTokenFlag(cmd, &kubeToken)
	addSSHAgentFlags(cmd, &sshAgent)
	cmd.Flags().StringVar(&agentID, "agent", "claude",
		"which agent to run ("+strings.Join(agent.IDs(), ", ")+")")
	return cmd
}

func runContainerStartAgent(ctx context.Context, a *app, workspace, name, agentID string, extra []string,
	kubeToken bool, sshOverride *bool) error {
	ag, err := agent.Lookup(agentID)
	if err != nil {
		return usageError(err)
	}

	t, err := a.resolve(ctx, workspace, name)
	if err != nil {
		return err
	}
	defer t.release()
	if err := t.requireOverride(a); err != nil {
		return err
	}

	environ, err := a.containerEnv(ctx, t.container)
	if err != nil {
		return err
	}

	// Asked before Up, so a host-side failure starts no container. Minted
	// later, so the token does not age through an image pull.
	kubeReq, err := askKubeTokenIf(ctx, kubeToken)
	if err != nil {
		return err
	}

	status, err := t.provider.Status(ctx, t.container)
	if err != nil {
		return err
	}
	if status != model.StatusRunning {
		if err := t.provider.Up(ctx, t.container, environ); err != nil {
			return err
		}
	}

	if t.container.AgentConfigPending {
		spec, err := loadAgentConfig(t.container)
		if err != nil {
			return err
		}
		if err := a.applyAgentConfig(ctx, t.provider, t.container, environ, spec); err != nil {
			return err
		}
	}

	if err := a.ensureAgentPresent(ctx, t, ag, environ); err != nil {
		return err
	}

	// After ensureAgentPresent, which may rebuild and discard a written
	// kubeconfig.
	environ, err = a.applyKubeToken(ctx, t, kubeReq, environ)
	if err != nil {
		return err
	}

	// After the container is up: the relay runs inside it, and a rebuild would
	// stop it.
	environ, stopAgent, err := a.forwardAgent(ctx, t, sshOverride, environ)
	if err != nil {
		return err
	}
	defer stopAgent()

	// Herdr reads the pane's process on the host and cannot see variables set
	// only in the container.
	if err := os.Setenv("HERDR_AGENT", ag.ID); err != nil {
		return err
	}
	defer herdr.NameAgent(ctx, t.container.Name)()

	argv := ag.Command(extra)
	started := time.Now()
	err = a.guardedRun(ctx, t, func() error {
		return t.provider.Exec(ctx, t.container, argv, provider.ExecOpts{
			Env:    environ,
			TTY:    true,
			Stdin:  os.Stdin,
			Stdout: os.Stdout,
			Stderr: os.Stderr,
		})
	})
	a.recordRun("start-agent", t, started, map[string]any{
		"agent":      ag.ID,
		"argv":       argv,
		"ssh_agent":  agentForwarded(t, sshOverride),
		"kube_token": kubeReq != nil,
	}, err)
	return err
}

// ensureAgentPresent checks the image actually has the agent, offering one
// rebuild if it does not.
func (a *app) ensureAgentPresent(ctx context.Context, t *target, ag agent.Agent, environ []provider.EnvVar) error {
	if agentInstalled(ctx, t, ag) {
		return nil
	}

	a.printf("agent %q is not installed in %s's image.\n", ag.ID, t.container.Name)
	if !confirm(os.Stdin, a.out, "rebuild the container and look again?") {
		// The fix lives in the project's devcontainer.json, not with this tool.
		return fmt.Errorf("add %s to %s and rebuild", ag.Binary, t.container.ConfigPath)
	}

	if err := t.provider.Rebuild(ctx, t.container, environ, false); err != nil {
		return err
	}
	if err := trustMountedRepository(ctx, t.provider, t.container); err != nil {
		return err
	}
	if !agentInstalled(ctx, t, ag) {
		return fmt.Errorf("%s is still missing after the rebuild; add it to %s",
			ag.Binary, t.container.ConfigPath)
	}
	return nil
}

// agentInstalled reports whether the binary is on PATH inside the container.
// Uses `command -v`, since many images ship no which(1).
func agentInstalled(ctx context.Context, t *target, ag agent.Agent) bool {
	err := t.provider.Exec(ctx, t.container,
		[]string{"sh", "-c", "command -v " + ag.Binary},
		provider.ExecOpts{Stdout: io.Discard, Stderr: io.Discard})
	return err == nil
}

// confirm asks a yes/no question, and answers no without asking when stdin is
// not a terminal.
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

// isTerminal reports whether f is an interactive terminal. It is not a
// ModeCharDevice check, since /dev/null is a character device too.
func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}
