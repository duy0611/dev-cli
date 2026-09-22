// Package provider abstracts where a devcontainer runs.
//
// The abstraction sits at the execution level rather than at devcontainer-spec
// parsing, because each provider drives a different external binary: the local
// one shells out to the devcontainer CLI and docker, and the Kubernetes one
// will shell out to kubectl.
package provider

import (
	"context"
	"fmt"
	"io"

	"github.com/duy0611/dev-cli/internal/model"
)

// EnvVar is one resolved environment variable to inject into a container.
// Resolved, because turning a setting's spec into a value needs the secret
// backend, which is a host concern rather than a provider one.
type EnvVar struct {
	Key   string
	Value string
}

// ExecOpts carries the stream wiring for a command run inside a container, so
// that `shell`, `exec` and `agent` are one code path with different options.
type ExecOpts struct {
	Env    []EnvVar
	TTY    bool
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Provider runs and inspects containers for one engine.
type Provider interface {
	// Up creates the container if it is missing and starts it if it is
	// stopped. Safe to call on a running container.
	Up(ctx context.Context, c model.Container, env []EnvVar) error

	// Rebuild recreates the container from its configuration, optionally
	// without the image layer cache. It takes the same env as Up, because the
	// lifecycle commands run again and a postCreateCommand that needs a token
	// must not start working on create and failing on rebuild.
	Rebuild(ctx context.Context, c model.Container, env []EnvVar, noCache bool) error

	// Exec runs a command inside a running container.
	Exec(ctx context.Context, c model.Container, cmd []string, opts ExecOpts) error

	Stop(ctx context.Context, c model.Container) error

	// Remove deletes the container. Removing one that is not there is not an
	// error: the caller's goal is that it be gone.
	Remove(ctx context.Context, c model.Container) error

	Status(ctx context.Context, c model.Container) (model.Status, error)

	Logs(ctx context.Context, c model.Container, follow bool, out io.Writer) error
}

// Syncer reconciles a container's copy of the workspace's settings.
//
// A postcondition rather than an action: after Sync returns, whatever copy the
// provider keeps outside the process dev launches matches the workspace. A
// provider that keeps no such copy satisfies that by doing nothing — the local
// one injects the settings per invocation, so there is nothing that can drift.
//
// Deliberately not part of Provider, for the reason AgentForwarder is not
// either: a provider added later should be able to run a container before it
// owes this. Callers type assert.
//
// Resolved variables are passed in rather than read here, as Up and Rebuild
// take them: turning a spec into a value needs the secret backend, which is a
// host concern.
type Syncer interface {
	Sync(ctx context.Context, c model.Container, env []EnvVar) error
}

// AgentForwarder is implemented by providers that can carry the host's SSH
// agent into a container.
//
// Optional for the same reason Syncer is, and discovered the same way — but
// note the asymmetry runs the other way here: both providers implement this
// one, because it needs only an exec channel and both have one. It is separate
// from Provider anyway, so that a provider added later is not obliged to
// support it before it can run a container at all.
//
// The returned socket path is where the agent is reachable inside the
// container, for SSH_AUTH_SOCK. Callers must Close the session: until they do,
// anything in the container can ask the operator's agent to sign.
type AgentForwarder interface {
	ForwardAgent(ctx context.Context, c model.Container, agentSocket string) (AgentSession, error)
}

// ConfigOverrider is a provider that consumes a merged devcontainer.json,
// through model.Container's OverrideConfigPath.
//
// Optional and discovered by type assertion, for the reason Syncer is: the
// Kubernetes provider builds its own pod spec and ignores a document's mounts
// entirely, so merging one would be work with no effect — and worse, a project
// file that does not parse would fail a k8s command over a document k8s never
// reads.
//
// The method reports nothing; its presence is the answer.
type ConfigOverrider interface {
	OverridesConfig()
}

// AgentSession is a live agent relay.
type AgentSession interface {
	// Socket is the path inside the container to put in SSH_AUTH_SOCK.
	Socket() string
	Close() error
}

// Factory builds the Provider for a configured provider record.
type Factory func(p model.Provider) (Provider, error)

// registry maps a provider kind to its constructor. Populated by each
// implementation's init, so that adding the Kubernetes provider later is one
// new package and no edit here.
var registry = map[model.ProviderKind]Factory{}

// Register wires a kind to its constructor. Called from package init.
func Register(kind model.ProviderKind, f Factory) {
	registry[kind] = f
}

// New returns the Provider for a configured provider record.
func New(p model.Provider) (Provider, error) {
	f, ok := registry[p.Kind]
	if !ok {
		return nil, fmt.Errorf("provider kind %q is not implemented", p.Kind)
	}
	return f(p)
}
