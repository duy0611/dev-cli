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

// ForwardAgent carries the host's SSH agent into the container over the CLI's
// exec. A bind mount of the socket does not work: macOS engines refuse the real
// socket, podman and colima expose none, and a mounted socket is root-owned.
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
		// No env: passing resolved secrets would keep them in the host process
		// list for the whole session.
		cmd := exec.CommandContext(ctx, devcontainerBin, p.execArgs(c, command)...)

		// StdinPipe, not an io.Pipe on cmd.Stdin: os/exec abandons a plain
		// io.Reader when the command exits, leaving the writer blocked forever.
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
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
