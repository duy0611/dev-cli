package local

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

// ForwardAgent carries the host's SSH agent into the container.
//
// The transport is the devcontainer CLI's own `exec`, not a bind mount of the
// agent socket. Mounting is the obvious approach and the wrong one: the macOS
// engine only exposes a synthesised /run/host-services/ssh-auth.sock and
// refuses to mount the real socket, podman and colima expose neither, a mounted
// socket arrives owned by root against a container running as vscode, and a
// mount is fixed at creation so the setting could not change without a rebuild.
// Relaying over exec has none of those problems.
func (p *Provider) ForwardAgent(ctx context.Context, c model.Container, agentSocket string) (provider.AgentSession, error) {
	if err := requireBinary(devcontainerBin); err != nil {
		return nil, err
	}

	run := func(ctx context.Context, command []string, stdin io.Reader) (string, error) {
		var out, errBuf bytes.Buffer
		err := p.Exec(ctx, c, command, provider.ExecOpts{
			Stdin:  stdin,
			Stdout: &out,
			Stderr: &errBuf,
		})
		if err != nil {
			return "", fmt.Errorf("%w: %s", err, errBuf.String())
		}
		return out.String(), nil
	}

	start := func(ctx context.Context, command []string) (*relay.Pipes, error) {
		// No env: the relay needs nothing from the workspace's settings, and
		// passing them would put resolved secrets in the host process list for
		// the whole session rather than for one command.
		cmd := exec.CommandContext(ctx, devcontainerBin, p.execArgs(c, command)...)

		// StdinPipe and StdoutPipe rather than an io.Pipe on cmd.Stdin: os/exec
		// drains a plain io.Reader from a goroutine of its own and abandons it
		// when the command exits, which would leave the host half blocked on a
		// pipe nobody reads. The same reasoning the k8s provider's pipeInto
		// comment records, for the same reason.
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		// The relay says nothing on stderr unless something is wrong, so it is
		// discarded rather than interleaved with the command the operator is
		// actually running.
		cmd.Stderr = io.Discard

		if err := cmd.Start(); err != nil {
			return nil, err
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
