// Package provider abstracts where a devcontainer runs. Each provider drives
// its own external binary rather than parsing the devcontainer spec.
package provider

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/duy0611/dev-cli/internal/model"
)

// EnvVar is one resolved environment variable to inject into a container.
type EnvVar struct {
	Key   string
	Value string
}

// ExecOpts carries the stream wiring for a command run inside a container.
type ExecOpts struct {
	Env    []EnvVar
	TTY    bool
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Provider runs and inspects containers for one engine.
type Provider interface {
	// Up creates the container if missing, and starts it if stopped. Safe on a
	// running one.
	Up(ctx context.Context, c model.Container, env []EnvVar) error

	// Rebuild recreates the container from its configuration, optionally
	// without the image layer cache. Takes the same env as Up, since lifecycle
	// commands run again.
	Rebuild(ctx context.Context, c model.Container, env []EnvVar, noCache bool) error

	// Exec runs a command inside a running container.
	Exec(ctx context.Context, c model.Container, cmd []string, opts ExecOpts) error

	Stop(ctx context.Context, c model.Container) error

	// Remove deletes the container. Removing one that is absent is not an
	// error.
	Remove(ctx context.Context, c model.Container) error

	Status(ctx context.Context, c model.Container) (model.Status, error)

	Logs(ctx context.Context, c model.Container, follow bool, out io.Writer) error
}

// Syncer reconciles a container's copy of the workspace's settings. It is a
// postcondition: after Sync returns, any copy the provider keeps matches the
// workspace. A provider that keeps no copy satisfies this by doing nothing.
type Syncer interface {
	Sync(ctx context.Context, c model.Container, env []EnvVar) error
}

// AgentForwarder is implemented by providers that can carry the host's SSH
// agent into a container. Callers must Close the session: until they do, the
// container can ask the operator's agent to sign.
type AgentForwarder interface {
	ForwardAgent(ctx context.Context, c model.Container, agentSocket string) (AgentSession, error)
}

// ConfigOverrider is a provider that consumes a merged devcontainer.json,
// through model.Container's OverrideConfigPath. The method is a marker:
// implementing it is the answer.
type ConfigOverrider interface {
	OverridesConfig()
}

// IdleStopper is a provider whose containers can stop themselves when idle.
type IdleStopper interface {
	// ProbeIdle runs argv in c as root through the engine directly, cheaply,
	// without the devcontainer CLI.
	ProbeIdle(ctx context.Context, c model.Container, argv []string, stdout io.Writer) error
}

// ConfigReader is a provider that can report a container's configuration as the
// devcontainer CLI will merge it: the project's document, every feature it
// names, and the override dev passes alongside.
type ConfigReader interface {
	// MergedConfig returns the CLI's merged configuration as raw JSON, and the
	// resolved compose files, which are empty unless docker compose is used.
	MergedConfig(ctx context.Context, c model.Container) (merged []byte, composeFiles []string, err error)
}

// AgentSession is a live agent relay.
type AgentSession interface {
	// Socket is the path inside the container to put in SSH_AUTH_SOCK. Fixed
	// for the session's life, across relay respawns.
	Socket() string
	Close() error
	// End reports how the session ended, once Close has returned.
	End() string
	// Drops reports each time the relay's exec channel collapsed, oldest first.
	Drops() []AgentDrop
}

// AgentDrop is one collapse of a relay's exec channel. Mirrors relay.Drop,
// which this package cannot import.
type AgentDrop struct {
	At       time.Time
	Err      string    // empty when the channel simply closed
	Restored time.Time // zero when no replacement came up before the session ended
}

// Factory builds the Provider for a configured provider record.
type Factory func(p model.Provider) (Provider, error)

// registry maps a provider kind to its constructor, populated by each
// implementation's init.
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

// Mount is one mount of an existing container, as its engine reports it.
type Mount struct {
	Destination string
	ReadOnly    bool
}

// MountReader reports the mounts an existing container was created with. Mounts
// are fixed at create, so one dev now wants may be absent. exists is false when
// the engine has no such container.
type MountReader interface {
	Mounts(ctx context.Context, c model.Container) (mounts []Mount, exists bool, err error)
}
