package k8s

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/relay"
)

// ForwardAgent carries the host's SSH agent into the pod over `kubectl exec`,
// a bidirectional stream: a pod has no host socket to bind-mount.
func (p *Provider) ForwardAgent(ctx context.Context, c model.Container, agentSocket string) (provider.AgentSession, error) {
	if err := requireBinary(kubectlBin); err != nil {
		return nil, err
	}

	dev, _, err := readConfiguration(ctx, c.Source, c.ConfigPath)
	if err != nil {
		return nil, err
	}

	run := func(ctx context.Context, command []string, stdin io.Reader) (string, error) {
		args := []string{"exec"}
		if stdin != nil {
			args = append(args, "-i")
		}
		args = append(args, "deployment/"+objectName(c), "--")
		args = append(args, asRemoteUser(dev.RemoteUser, dev.HomeDir(), dev.WorkspaceFolder, command)...)

		var out bytes.Buffer
		err := p.kube.stream(ctx, streamOpts{
			Stdin:  stdin,
			Stdout: &out,
			Stderr: io.Discard,
		}, args...)
		return out.String(), err
	}

	start := func(ctx context.Context, command []string) (*relay.Pipes, error) {
		args := p.kube.args(append([]string{
			"exec", "-i", "deployment/" + objectName(c), "--",
		}, asRemoteUser(dev.RemoteUser, dev.HomeDir(), dev.WorkspaceFolder, command)...)...)

		cmd := exec.CommandContext(ctx, kubectlBin, args...)

		// StdinPipe, not an io.Reader on cmd.Stdin: os/exec abandons a plain
		// reader when the command exits, leaving the writer blocked. A real
		// pipe gives EPIPE.
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		// kubectl's stderr is discarded: a lost connection surfaces as git
		// losing the agent.
		cmd.Stderr = io.Discard

		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("starting the relay: %w", err)
		}
		return &relay.Pipes{
			Stdin:  stdin,
			Stdout: stdout,
			Wait:   cmd.Wait,
			Kill:   func() error { return cmd.Process.Kill() },
		}, nil
	}

	s, err := relay.Start(ctx, run, start, agentSocket)
	if err != nil {
		return nil, err
	}
	return &agentSession{s}, nil
}

// agentSession adapts relay.Session to provider.AgentSession.
type agentSession struct{ s *relay.Session }

func (a *agentSession) Socket() string { return a.s.Socket }
func (a *agentSession) Close() error   { return a.s.Close() }
func (a *agentSession) End() string    { return a.s.End() }

func (a *agentSession) Drops() []provider.AgentDrop {
	var out []provider.AgentDrop
	for _, d := range a.s.Drops() {
		out = append(out, provider.AgentDrop{At: d.At, Err: d.Err, Restored: d.Restored})
	}
	return out
}
