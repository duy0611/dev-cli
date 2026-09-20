package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

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
	environ = append(environ, provider.EnvVar{
		Key:   sshAuthSockKey,
		Value: session.Socket(),
	})

	// Signing is skipped when the operator configures git through the same
	// variables themselves. Both would arrive as --remote-env and the last would
	// win, so adding ours would silently drop theirs — and a half-applied
	// GIT_CONFIG_COUNT is a fatal git error rather than a lost setting.
	if hasGitConfigEnv(environ) {
		warnf(a, "GIT_CONFIG_COUNT is set for this workspace; not configuring commit signing")
		return environ, stop, nil
	}
	return append(environ, signingEnv(agentSocket)...), stop, nil
}

// hasGitConfigEnv reports whether the environment already drives git's
// environment-based configuration.
func hasGitConfigEnv(environ []provider.EnvVar) bool {
	for _, e := range environ {
		if strings.HasPrefix(e.Key, "GIT_CONFIG_") {
			return true
		}
	}
	return false
}

// signingEnv configures git to sign commits with the forwarded agent.
//
// Since git 2.34 an SSH key can sign a commit, so the agent that authenticates
// the push signs the commit too and no GPG agent is needed. The key never
// enters the container either way — the container asks the relay to sign, and
// the signing happens on the host.
//
// Best effort: an agent holding no keys can still authenticate a push using a
// key added later in the session, and refusing to run the operator's command
// over a signing key would be out of proportion. Git says so itself if a commit
// is then attempted.
func signingEnv(agentSocket string) []provider.EnvVar {
	key, err := relay.SigningKey(agentSocket)
	if err != nil {
		return nil
	}

	// Set through GIT_CONFIG_* rather than by writing a file: git reads these
	// from the environment (since 2.31), so nothing is written into the
	// container and nothing into the project's own .git/config.
	//
	// The count must match the number of pairs exactly, numbered from zero with
	// no gaps — a missing index is a fatal git error, not a skipped entry. So
	// the list is built in one place and counted from itself.
	pairs := [][2]string{
		{"gpg.format", "ssh"},
		{"user.signingkey", key},
		{"commit.gpgsign", "true"},
	}

	out := []provider.EnvVar{{
		Key:   "GIT_CONFIG_COUNT",
		Value: itoa(len(pairs)),
	}}
	for i, p := range pairs {
		out = append(out,
			provider.EnvVar{Key: fmt.Sprintf("GIT_CONFIG_KEY_%d", i), Value: p[0]},
			provider.EnvVar{Key: fmt.Sprintf("GIT_CONFIG_VALUE_%d", i), Value: p[1]},
		)
	}
	return out
}
