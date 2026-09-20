package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/relay"
)

// sshAuthSockKey is the variable ssh and git read to find an agent.
const sshAuthSockKey = "SSH_AUTH_SOCK"

// forwardAgent starts an agent relay for one command, when the workspace asks
// for it, and returns the environment with SSH_AUTH_SOCK pointing at it.
//
// The returned stop must be called before the command returns. The agent is
// reachable from inside the container until it is, which is the whole feature
// and also the reason the window is one command rather than the container's
// lifetime.
//
// Returns environ unchanged, and a stop that does nothing, when the workspace
// has forwarding off — so every caller can invoke this the same way.
func (a *app) forwardAgent(ctx context.Context, t *target, environ []provider.EnvVar) ([]provider.EnvVar, func(), error) {
	noop := func() {}
	if !t.workspace.SSHForward {
		return environ, noop, nil
	}

	// Refused rather than skipped: the workspace asked for the agent, so
	// carrying on without it would produce a "Permission denied (publickey)"
	// later that says nothing about the real cause.
	agentSocket, err := relay.AgentSocket()
	if err != nil {
		return nil, noop, err
	}

	// Discovered by type assertion, as Syncer is. Both providers implement it,
	// but a provider added later is not obliged to before it can run anything.
	fwd, ok := t.provider.(provider.AgentForwarder)
	if !ok {
		return nil, noop, fmt.Errorf(
			"workspace %s forwards the ssh agent, which provider %s does not support",
			t.workspace.Name, t.workspace.ProviderName)
	}

	session, err := fwd.ForwardAgent(ctx, t.container, agentSocket)
	if err != nil {
		return nil, noop, err
	}

	stop := func() {
		if err := session.Close(); err != nil {
			// Worth saying, not worth failing the command over: whatever the
			// operator ran has already finished, and the channel dies with the
			// exec regardless.
			fmt.Fprintf(os.Stderr, "dev: stopping the ssh agent relay: %v\n", err)
		}
	}

	// Appended last, so this wins over a workspace setting of the same name.
	// The opposite precedence to the git identity in env.Assemble, and
	// deliberately: the identity is a default the operator may improve on,
	// whereas this is the socket that actually exists. A hand-set SSH_AUTH_SOCK
	// would name a path in the container that nothing is listening on.
	return append(environ, provider.EnvVar{
		Key:   sshAuthSockKey,
		Value: session.Socket(),
	}), stop, nil
}
