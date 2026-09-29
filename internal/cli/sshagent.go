package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/relay"
)

// sshAuthSockKey is the variable ssh and git read to find an agent.
const sshAuthSockKey = "SSH_AUTH_SOCK"

// sshAgentFlags is --ssh-agent and --no-ssh-agent on a command that launches a
// process in a container, overriding the workspace's setting for that one
// command.
type sshAgentFlags struct {
	on, off bool
}

// addSSHAgentFlags registers the pair on cmd.
func addSSHAgentFlags(cmd *cobra.Command, f *sshAgentFlags) {
	cmd.Flags().BoolVar(&f.on, "ssh-agent", false,
		"forward the host's ssh agent for this command, whatever the workspace says")
	cmd.Flags().BoolVar(&f.off, "no-ssh-agent", false,
		"do not forward the host's ssh agent for this command, whatever the workspace says")
}

// override reports the choice the flags make: nil when neither was given, so
// the workspace's setting stands.
//
// Both at once is refused rather than resolved by order: the operator asked for
// two opposite things, and whichever one won would be a guess. Checked here and
// not with cobra's MarkFlagsMutuallyExclusive, whose error exits 1 instead of
// the 2 a malformed request owes (invariant 7).
func (f sshAgentFlags) override() (*bool, error) {
	switch {
	case f.on && f.off:
		return nil, usageErrorf("--ssh-agent and --no-ssh-agent cannot be used together")
	case f.on:
		v := true
		return &v, nil
	case f.off:
		v := false
		return &v, nil
	}
	return nil, nil
}

// forwardAgent starts an agent relay for one command, when the workspace asks
// for it or override does, and returns the environment with SSH_AUTH_SOCK
// pointing at it. A non-nil override wins over the workspace in both
// directions.
//
// The returned stop must be called before the command returns. The agent is
// reachable from inside the container until it is, which is the whole feature
// and also the reason the window is one command rather than the container's
// lifetime.
//
// Returns environ unchanged, and a stop that does nothing, when forwarding is
// off — so every caller can invoke this the same way.
func (a *app) forwardAgent(ctx context.Context, t *target, override *bool,
	environ []provider.EnvVar) ([]provider.EnvVar, func(), error) {
	noop := func() {}
	forward := t.workspace.SSHForward
	if override != nil {
		forward = *override
	}
	if !forward {
		return environ, noop, nil
	}

	// Refused rather than skipped: the operator asked for the agent, so
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
			"forwarding the ssh agent into workspace %s, which provider %s does not support",
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
//
// The three numbered keys specifically, not the GIT_CONFIG_ prefix:
// GIT_CONFIG_GLOBAL shares that prefix and is set by every container that
// persists its state, so matching the prefix would turn commit signing off for
// all of them and blame a variable the operator never set. The two compose —
// GIT_CONFIG_GLOBAL names the file, the numbered keys override what is in it.
func hasGitConfigEnv(environ []provider.EnvVar) bool {
	for _, e := range environ {
		if e.Key == "GIT_CONFIG_COUNT" ||
			strings.HasPrefix(e.Key, "GIT_CONFIG_KEY_") ||
			strings.HasPrefix(e.Key, "GIT_CONFIG_VALUE_") {
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
