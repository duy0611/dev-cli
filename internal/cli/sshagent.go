package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/relay"
)

// sshAuthSockKey is the variable ssh and git read to find an agent.
const sshAuthSockKey = "SSH_AUTH_SOCK"

// sshAgentFlags holds --ssh-agent and --no-ssh-agent for one command.
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

// override reports the flags' choice: nil when neither was given. Both at once
// is refused, not resolved by order. Checked here, not by cobra's
// MarkFlagsMutuallyExclusive, which exits 1, not 2.
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

// agentForwarded reports whether a command on t forwards the ssh agent.
func agentForwarded(t *target, override *bool) bool {
	if t.container.Mode == model.ModeGuarded {
		return false
	}
	if override != nil {
		return *override
	}
	return t.workspace.SSHForward
}

// forwardAgent starts an agent relay for one command when the workspace or
// override asks for one, and returns environ with SSH_AUTH_SOCK pointing at it.
// The returned stop must run before the command returns: the relay is reachable
// from inside the container until then.
func (a *app) forwardAgent(ctx context.Context, t *target, override *bool,
	environ []provider.EnvVar) ([]provider.EnvVar, func(), error) {
	noop := func() {}
	// A guarded container cannot commit or fetch, so the relay is refused
	// there: it could still push the operator's existing commits, which nobody
	// asked it to do.
	if t.container.Mode == model.ModeGuarded {
		if override != nil && *override {
			return nil, noop, usageErrorf("container %s is guarded and cannot commit, so it has no use for "+
				"the ssh agent; push from your machine, or use an isolated container", t.container.Name)
		}
		if override == nil && t.workspace.SSHForward {
			fmt.Fprintf(os.Stderr, "dev: not forwarding the ssh agent into %s: a guarded container cannot commit, "+
				"so pushing is done from your machine\n", t.container.Name)
		}
		return environ, noop, nil
	}
	forward := t.workspace.SSHForward
	if override != nil {
		forward = *override
	}
	if !forward {
		return environ, noop, nil
	}

	// Refused rather than skipped: the operator asked for the agent, and
	// skipping it would yield a misleading publickey error later.
	agentSocket, err := relay.AgentSocket()
	if err != nil {
		return nil, noop, err
	}

	// Discovered by type assertion, as Syncer is, since a provider need not
	// implement it.
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
	started := time.Now()

	stop := func() {
		if err := session.Close(); err != nil {
			// Worth reporting, not failing: the command has already finished.
			fmt.Fprintf(os.Stderr, "dev: stopping the ssh agent relay: %v\n", err)
		}
		// After Close, so End can tell a relay that died mid-session from one
		// shut down.
		fields := map[string]any{
			"started": started.UTC().Format(time.RFC3339Nano),
			"ended":   time.Now().UTC().Format(time.RFC3339Nano),
			"end":     session.End(),
		}
		if drops := relayDrops(session.Drops()); drops != nil {
			fields["drops"] = drops
		}
		a.record("relay", t.workspace.Name, t.container.Name, fields)
	}

	// Appended last, so it beats a workspace setting of the same name. A
	// hand-set SSH_AUTH_SOCK would name a socket nothing listens on.
	environ = append(environ, provider.EnvVar{
		Key:   sshAuthSockKey,
		Value: session.Socket(),
	})

	// Skipped when the operator configures git's environment variables: a
	// second set would silently override theirs, and a partial GIT_CONFIG_COUNT
	// is fatal to git.
	if hasGitConfigEnv(environ) {
		warnf(a, "GIT_CONFIG_COUNT is set for this workspace; not configuring commit signing")
		return environ, stop, nil
	}
	return append(environ, signingEnv(agentSocket)...), stop, nil
}

// hasGitConfigEnv reports whether the environment already sets git's numbered
// GIT_CONFIG_* pairs. Not the GIT_CONFIG_ prefix: GIT_CONFIG_GLOBAL shares it
// and every stateful container sets it.
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

// signingEnv has git sign commits with the forwarded agent (git 2.34+ signs
// with SSH keys). The key stays on the host. Best effort: a push can still
// authenticate with a key added later, and git reports a failed signature
// itself.
func signingEnv(agentSocket string) []provider.EnvVar {
	key, err := relay.SigningKey(agentSocket)
	if err != nil {
		return nil
	}

	// GIT_CONFIG_* rather than a file, so nothing is written into the container
	// or the project. The count must match the pairs exactly, with no gaps: a
	// bad index is fatal to git.
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

// relayDrops returns a session's drops as audit fields, oldest first; nil when
// there were none.
func relayDrops(drops []provider.AgentDrop) []map[string]any {
	if len(drops) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(drops))
	for _, d := range drops {
		m := map[string]any{"at": d.At.UTC().Format(time.RFC3339Nano)}
		if d.Err != "" {
			m["error"] = d.Err
		}
		if !d.Restored.IsZero() {
			m["restored"] = d.Restored.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, m)
	}
	return out
}
