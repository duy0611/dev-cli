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

// ForwardAgent carries the host's SSH agent into the pod.
//
// A pod has no host socket to bind, so the mount approach the local provider
// also rejects is not even available here — but `kubectl exec` is a
// bidirectional stream, which is all the relay needs. Both providers end up
// implementing this, unlike Syncer, which only this one can.
func (p *Provider) ForwardAgent(ctx context.Context, c model.Container, agentSocket string) (provider.AgentSession, error) {
	if err := requireBinary(kubectlBin); err != nil {
		return nil, err
	}

	// Read once and reused by every call below. readConfiguration shells out to
	// the devcontainer CLI, which in turn asks docker — four of those to start
	// one relay would be seconds of latency on every command.
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
		args = append(args, asRemoteUser(dev.RemoteUser, dev.WorkspaceFolder, command)...)

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
		}, asRemoteUser(dev.RemoteUser, dev.WorkspaceFolder, command)...)...)

		cmd := exec.CommandContext(ctx, kubectlBin, args...)

		// StdinPipe and StdoutPipe rather than an io.Reader on cmd.Stdin, for
		// the reason pipeInto records: os/exec drains a plain reader from a
		// goroutine of its own and abandons it when the command exits, leaving
		// the writer blocked on a pipe nobody reads. A real pipe gives EPIPE.
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		// kubectl reports a lost connection on stderr. Discarded rather than
		// interleaved with whatever the operator is running, and the relay
		// failing shows up as git losing the agent.
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

// agentSession adapts relay.Session to the provider interface, which cannot
// name the relay package without every provider importing it.
type agentSession struct{ s *relay.Session }

func (a *agentSession) Socket() string { return a.s.Socket }
func (a *agentSession) Close() error   { return a.s.Close() }
