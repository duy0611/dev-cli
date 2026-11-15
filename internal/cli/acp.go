package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/duy0611/dev-cli/internal/acp"
	"github.com/duy0611/dev-cli/internal/agent"
	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/store"
	"github.com/duy0611/dev-cli/internal/xpath"
	"github.com/spf13/cobra"
)

// newContainerACPCmd serves a container's agent over ACP on stdin and stdout.
// Experimental, like dev diff.
func newContainerACPCmd(a *app) *cobra.Command {
	var (
		workspace    string
		agentID      string
		trace        string
		printHarness bool
		integration  string
		passEnv      []string
		kubeToken    bool
		sshAgent     sshAgentFlags
	)

	cmd := &cobra.Command{
		Use:   "acp [NAME] [-- COMMAND [ARGS...]]",
		Short: "Serve an agent over the Agent Client Protocol on stdin/stdout (experimental)",
		Long: "Serve a coding agent inside a container to an Agent Client Protocol\n" +
			"client on the host. The client starts this command; stdin and stdout\n" +
			"carry the protocol, everything else goes to stderr.\n\n" +
			"Experimental: the command, its flags and its output may change without a\n" +
			"migration. Starts a stopped container, or re-creates one whose engine\n" +
			"container is gone; refuses one that was never set up with create.\n" +
			"The agent's ACP adapter must be installed in the container's image.\n\n" +
			"A COMMAND after -- replaces the agent's ACP adapter, for example:\n" +
			"  dev container acp app -- npx -y @agentclientprotocol/claude-agent-acp",
		Args:                  wrapArgs(cobra.ArbitraryArgs),
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var command []string
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				command = args[dash:]
			}
			sshOverride, err := sshAgent.override()
			if err != nil {
				return err
			}
			if kubeToken {
				return usageErrorf("--kube-token is not supported by acp: it asks for the cluster and namespace at a terminal, " +
					"and stdin here is the protocol; use dev container start-agent --kube-token instead")
			}
			if err := validatePassEnv(passEnv); err != nil {
				return err
			}
			integ, err := lookupACPIntegration(integration)
			if err != nil {
				return err
			}
			if len(args) == 0 && !printHarness {
				return usageErrorf("missing container NAME")
			}
			if len(args) == 0 && len(command) > 0 {
				return usageErrorf("a COMMAND after -- needs a container NAME")
			}
			if printHarness {
				exe, err := os.Executable()
				if err == nil {
					exe, err = filepath.EvalSymlinks(exe)
				}
				if err != nil {
					return err
				}
				if len(args) == 0 {
					if _, err := agent.Lookup(agentID); err != nil {
						return usageError(err)
					}
					_, err := io.WriteString(cmd.OutOrStdout(),
						acpGenericHarness(exe, agentID, trace, passEnv, sshOverride, integration))
					return err
				}
				return printACPHarness(cmd.Context(), a, cmd.OutOrStdout(), workspace, args[0], agentID, command,
					passEnv, trace, sshOverride, integration, exe)
			}
			env := passedEnv(os.Environ(), integ, passEnv)
			return runContainerACP(cmd.Context(), a, workspace, args[0], agentID, command, env, trace, sshOverride,
				integ, os.Stdin, os.Stdout)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	// Registered only to be refused with the reason; hidden from help.
	addKubeTokenFlag(cmd, &kubeToken)
	_ = cmd.Flags().MarkHidden("kube-token")
	addSSHAgentFlags(cmd, &sshAgent)
	cmd.Flags().StringVar(&agentID, "agent", "claude",
		"which agent to serve ("+strings.Join(agent.IDs(), ", ")+")")
	cmd.Flags().StringVar(&trace, "trace", "",
		"append every protocol message, after rewriting, to this file")
	cmd.Flags().StringSliceVar(&passEnv, "pass-env", nil,
		"pass this host variable to the agent, by exact name (repeatable); none are passed "+
			"unless named here or by --integration")
	cmd.Flags().StringVar(&integration, "integration", "",
		"add behaviour for one ACP client ("+strings.Join(integrationNames(), ", ")+"); without it this is a plain ACP agent")
	cmd.Flags().BoolVar(&printHarness, "print-harness", false,
		"print a shell script that runs this command and exit; without NAME, a generic one that reads the "+
			"container and workspace from DEV_ACP_CONTAINER and DEV_ACP_WORKSPACE (--agent and --trace set its defaults)")
	return cmd
}

func runContainerACP(ctx context.Context, a *app, workspace, name, agentID string, command []string,
	secrets []provider.EnvVar, trace string, sshOverride *bool, integ *acpIntegration, in io.Reader, out io.Writer) error {
	// stdout is the protocol channel: the launch prints through a.out, which
	// would corrupt it, so progress goes to stderr instead.
	a.out = a.stderr()

	ag, err := agent.Lookup(agentID)
	if err != nil {
		return usageError(err)
	}
	if len(command) == 0 {
		if ag.ACP == nil {
			return usageErrorf("no ACP adapter known for %s; give one after --", ag.ID)
		}
		command = ag.ACP
	}
	// The adapter's own env applies to a replaced command too. It is not
	// secret, so it rides the exec's env; the passed variables do not.
	var wrapped []string
	var envPath string
	if len(secrets) > 0 {
		envPath = acp.EnvPath()
		wrapped = acp.Wrapper(envPath, command)
	}

	var traceW io.Writer
	if trace != "" {
		f, err := os.OpenFile(trace, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		traceW = f
	}

	t, err := a.resolve(ctx, workspace, name)
	if err != nil {
		return err
	}
	defer t.release()
	if err := t.requireOverride(a); err != nil {
		return err
	}

	// An *os.File, not an io.Pipe: os/exec hands a file to the child directly,
	// where any other reader gets a copying goroutine that Wait blocks on
	// until the client next writes, long after a crashed agent is gone.
	agentIn, toAgent, err := os.Pipe()
	if err != nil {
		return err
	}
	fromAgent, agentOut := io.Pipe()
	var toClient sync.WaitGroup

	argv := command
	if wrapped != nil {
		argv = wrapped
	}
	err = a.launchAgent(ctx, t, ag, argv, launchOpts{
		sshOverride: sshOverride,
		event:       "acp",
		env:         integ.agentEnv(ag.ID),
		// STUB
		auditArgv: command,
		stdin:     agentIn,
		stdout:    agentOut,
		binary:    command[0],
		onStart: func() error {
			cwd, err := containerCwd(ctx, t)
			if err != nil {
				return err
			}
			// The host side is the folder the container's workspace folder
			// holds: for a guarded container the git root, which may be above
			// Source.
			_, extent := primaryPath(t.container)
			roots := []acp.Root{{Host: extent, Container: cwd}}
			for _, f := range t.container.Folders {
				roots = append(roots, acp.Root{Host: f.HostPath, Container: f.ContainerPath})
			}
			b := acp.New(roots, cwd, a.stderr())
			b.Trace = traceW
			for _, r := range b.Roots {
				fmt.Fprintf(a.stderr(), "acp: %s -> %s\n", r.Host, r.Container)
			}
			for _, e := range secrets {
				fmt.Fprintf(a.stderr(), "acp: passing %s\n", e.Key)
			}
			if integ != nil && integ.warnBinary != "" && !binaryInstalled(ctx, t, integ.warnBinary) {
				fmt.Fprintf(a.stderr(), "acp: warning: no %s CLI on %s's PATH; the agent cannot reply in Buzz\n",
					integ.warnBinary, t.container.Name)
			}
			if envPath != "" {
				if err := writeACPEnv(ctx, t, envPath, secrets); err != nil {
					return err
				}
			}

			go func() {
				_ = b.ToAgent(toAgent, in)
				_ = toAgent.Close()
			}()
			toClient.Go(func() {
				if err := b.ToClient(out, fromAgent); err != nil {
					// Keep draining so the exec's stdout copy never blocks on a
					// client that is gone.
					_, _ = io.Copy(io.Discard, fromAgent)
				}
			})
			return nil
		},
	})
	if envPath != "" {
		// Normally gone already; left behind only if the adapter never started.
		_ = t.provider.Exec(context.WithoutCancel(ctx), t.container, []string{"rm", "-f", envPath},
			provider.ExecOpts{Stdout: io.Discard, Stderr: io.Discard})
	}
	// A client still connected after the agent exits sees stdout end; its next
	// write fails once toAgent is closed.
	_ = agentOut.Close()
	_ = agentIn.Close()
	_ = toAgent.Close()
	toClient.Wait()
	return err
}

// containerCwd is the directory an exec starts in: the container's workspace
// folder. Asked of the container rather than derived, since a project config
// may set workspaceFolder to anything and dev-cli does not parse it.
func containerCwd(ctx context.Context, t *target) (string, error) {
	var out, errBuf bytes.Buffer
	if err := t.provider.Exec(ctx, t.container, []string{"sh", "-c", "pwd"},
		provider.ExecOpts{Stdout: &out, Stderr: &errBuf}); err != nil {
		return "", fmt.Errorf("finding %s's workspace folder: %w: %s", t.container.Name, err, strings.TrimSpace(errBuf.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// acpIntegration is what the command adds for one ACP client beyond plain
// ACP: variables passed by default, per-agent adapter env, and a CLI the agent
// is expected to find in the container. A nil *acpIntegration adds nothing.
type acpIntegration struct {
	passEnv    []string
	env        map[string][]provider.EnvVar
	warnBinary string
}

// acpIntegrations is the set --integration accepts.
var acpIntegrations = map[string]*acpIntegration{
	"buzz": {
		passEnv: []string{"BUZZ_PRIVATE_KEY", "BUZZ_AUTH_TAG", "BUZZ_RELAY_URL", "BUZZ_OUTPUT_FORMAT"},
		// buzz-acp and claude-agent-acp implement different v2 drafts; drop
		// the pin when they agree.
		env:        map[string][]provider.EnvVar{"claude": {{Key: "CLAUDE_AGENT_ACP_PROTOCOL", Value: "v1"}}},
		warnBinary: "buzz",
	},
}

func integrationNames() []string {
	names := make([]string, 0, len(acpIntegrations))
	for n := range acpIntegrations {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func lookupACPIntegration(name string) (*acpIntegration, error) {
	if name == "" {
		return nil, nil
	}
	if integ, ok := acpIntegrations[name]; ok {
		return integ, nil
	}
	return nil, usageErrorf("--integration %q: want one of %s", name, strings.Join(integrationNames(), ", "))
}

func (i *acpIntegration) agentEnv(agentID string) []provider.EnvVar {
	if i == nil {
		return nil
	}
	return slices.Clone(i.env[agentID])
}

// validatePassEnv refuses anything but a plain variable name, patterns
// included.
func validatePassEnv(names []string) error {
	for _, n := range names {
		if err := xpath.ValidateEnvKey(n); err != nil {
			return usageErrorf("--pass-env %q: want an exact variable name (letters, digits and _)", n)
		}
	}
	return nil
}

// passedEnv picks the integration's default variables and the named extras from environ,
// those set only, values unchanged.
func passedEnv(environ []string, integ *acpIntegration, extra []string) []provider.EnvVar {
	var names []string
	if integ != nil {
		names = integ.passEnv
	}
	names = slices.Concat(names, extra)
	var env []provider.EnvVar
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if ok && slices.Contains(names, k) && !slices.ContainsFunc(env, func(e provider.EnvVar) bool { return e.Key == k }) {
			env = append(env, provider.EnvVar{Key: k, Value: v})
		}
	}
	sort.Slice(env, func(i, j int) bool { return env[i].Key < env[j].Key })
	return env
}

// writeACPEnv puts the variables in a private file in the container, over
// stdin so no process list shows them. The adapter's wrapper reads and deletes
// it.
func writeACPEnv(ctx context.Context, t *target, path string, env []provider.EnvVar) error {
	vars := make([]acp.Var, len(env))
	for i, e := range env {
		vars[i] = acp.Var{Key: e.Key, Value: e.Value}
	}
	var errBuf bytes.Buffer
	if err := t.provider.Exec(ctx, t.container, acp.WriteCommand(path), provider.ExecOpts{
		Stdin: bytes.NewReader(acp.EnvFile(vars)), Stdout: io.Discard, Stderr: &errBuf,
	}); err != nil {
		return fmt.Errorf("passing variables to %s: %w: %s", t.container.Name, err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// printACPHarness writes the script Buzz Desktop runs as a harness. It checks
// the container exists and touches nothing else.
func printACPHarness(ctx context.Context, a *app, out io.Writer, workspace, name, agentID string,
	command, passEnv []string, trace string, sshOverride *bool, integration, exe string) error {
	if err := validatePassEnv(passEnv); err != nil {
		return err
	}
	if _, err := agent.Lookup(agentID); err != nil {
		return usageError(err)
	}
	ws, err := a.workspaceName(workspace)
	if err != nil {
		return err
	}
	st, err := a.store()
	if err != nil {
		return err
	}
	if _, err := st.GetContainer(ws, name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return notFoundErrorf("no such container: %s (workspace %s)", name, ws)
		}
		return err
	}
	_, err = io.WriteString(out, acpHarness(exe, ws, name, agentID, command, passEnv, trace, sshOverride, integration))
	return err
}

// acpHarness is the POSIX sh script itself.
func acpHarness(exe, workspace, name, agentID string, command, passEnv []string, trace string, sshOverride *bool, integration string) string {
	args := []string{exe, "container", "acp", name, "--workspace", workspace, "--agent", agentID}
	if integration != "" {
		args = append(args, "--integration", integration)
	}
	for _, n := range passEnv {
		args = append(args, "--pass-env", n)
	}
	if trace != "" {
		args = append(args, "--trace", trace)
	}
	args = append(args, sshFlag(sshOverride)...)
	if len(command) > 0 {
		args = append(args, "--")
		args = append(args, command...)
	}
	quoted := make([]string, len(args))
	for i, x := range args {
		quoted[i] = acp.Quote(x)
	}
	return "#!/bin/sh\nexec " + strings.Join(quoted, " ") + "\n"
}

func sshFlag(o *bool) []string {
	switch {
	case o == nil:
		return nil
	case *o:
		return []string{"--ssh-agent"}
	}
	return []string{"--no-ssh-agent"}
}

// acpGenericHarness is a script for clients that set per-agent environment
// variables. agentID and trace are only the defaults for DEV_ACP_AGENT and
// DEV_ACP_TRACE. Resolves nothing: the container is named at run time.
func acpGenericHarness(exe, agentID, trace string, passEnv []string, sshOverride *bool, integration string) string {
	if trace == "" {
		trace = "/tmp/acp-$DEV_ACP_CONTAINER.log"
	} else {
		trace = dqEscape.Replace(trace)
	}
	var extra strings.Builder
	if integration != "" {
		extra.WriteString(" \\\n  --integration " + acp.Quote(integration))
	}
	for _, n := range passEnv {
		extra.WriteString(" \\\n  --pass-env " + acp.Quote(n))
	}
	for _, f := range sshFlag(sshOverride) {
		extra.WriteString(" \\\n  " + f)
	}
	return "#!/bin/sh\n" +
		"DEV=" + acp.Quote(exe) + "\n" +
		"if [ -z \"$DEV_ACP_CONTAINER\" ]; then\n" +
		"  echo \"dev-acp: DEV_ACP_CONTAINER is not set in this agent's environment\" >&2\n  exit 2\nfi\n" +
		"if [ -z \"$DEV_ACP_WORKSPACE\" ]; then\n" +
		"  echo \"dev-acp: DEV_ACP_WORKSPACE is not set in this agent's environment\" >&2\n  exit 2\nfi\n" +
		"set -- container acp \"$DEV_ACP_CONTAINER\" \\\n" +
		"  --workspace \"$DEV_ACP_WORKSPACE\" \\\n" +
		"  --agent \"${DEV_ACP_AGENT:-" + dqEscape.Replace(agentID) + "}\" \\\n" +
		"  --trace \"${DEV_ACP_TRACE:-" + trace + "}\"" + extra.String() + "\n" +
		"exec \"$DEV\" \"$@\"\n"
}

// dqEscape makes a string literal inside double quotes.
var dqEscape = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`")
