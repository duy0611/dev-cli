// Package provider abstracts where a devcontainer runs. Each provider drives
// its own external binary rather than parsing the devcontainer spec.
package provider

import (
	"context"
	"errors"
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

// RebuildOpts are the choices a rebuild takes beyond the container itself.
type RebuildOpts struct {
	NoCache bool
	// Seed copies the folders into fresh workspace volumes, as a first create
	// does. Only a switch into isolated sets it.
	Seed bool
}

// RemoveOpts are the choices a removal takes beyond the container itself.
type RemoveOpts struct {
	// KeepState leaves the agent-state volume behind for a container of the
	// same name to reattach.
	KeepState bool
}

// LifecycleError is a lifecycle hook failing after a seeded rebuild: the
// volumes now hold the container's copy, so the mode has changed.
type LifecycleError struct{ Err error }

func (e *LifecycleError) Error() string { return e.Err.Error() }

func (e *LifecycleError) Unwrap() error { return e.Err }

// Provider runs and inspects containers for one engine.
type Provider interface {
	// Up creates the container if missing, and starts it if stopped. Safe on a
	// running one.
	Up(ctx context.Context, c model.Container, env []EnvVar) error

	// Rebuild recreates the container from its configuration. Takes the same
	// env as Up, since lifecycle commands run again.
	Rebuild(ctx context.Context, c model.Container, env []EnvVar, opts RebuildOpts) error

	// Exec runs a command inside a running container.
	Exec(ctx context.Context, c model.Container, cmd []string, opts ExecOpts) error

	Stop(ctx context.Context, c model.Container) error

	// Remove deletes the container. Removing one that is absent is not an
	// error.
	Remove(ctx context.Context, c model.Container, opts RemoveOpts) error

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

// Errors a Copier reports for a source it cannot copy from, so a caller can
// tell the operator what to do.
var (
	// ErrNoContainer: the source has no container in the engine.
	ErrNoContainer = errors.New("the source has no container")
	// ErrNoImage: the source's image is no longer present locally.
	ErrNoImage = errors.New("the source's image no longer exists locally")
	// ErrNoStateVolume: the source has no state volume.
	ErrNoStateVolume = errors.New("the source has no state volume")
	// ErrTargetExists: the new container's state volume or snapshot tag is
	// already in the engine, so a copy would merge into or replace leftovers.
	ErrTargetExists = errors.New("the new container's state volume or snapshot tag already exists")
	// ErrTargetVolumeExists is the ErrTargetExists whose object is the state
	// volume, which remove --keep-state leaves on purpose.
	ErrTargetVolumeExists = fmt.Errorf("the new container's state volume already exists: %w", ErrTargetExists)
)

// Copier is a provider that can start a container from another one's image and
// state. The local provider only.
type Copier interface {
	// SourceImage returns the ID of the image src's container was created from.
	// It wraps ErrNoContainer or ErrNoImage.
	SourceImage(ctx context.Context, src model.Container) (string, error)

	// SnapshotImage makes imageID available under a name owned by dst, as an
	// image of its own that carries the source's Features' metadata but not the
	// source's own config. Up starts dst from that tag while the engine has no
	// container for it. The tag stays: it is dst's own image, and Remove deletes
	// it. built is whether the source's config builds an image; when it does
	// not, the image is tagged as it is.
	SnapshotImage(ctx context.Context, dst model.Container, imageID string, built bool) error

	// ImageFeatureIDs returns the id values of the image's devcontainer.metadata
	// label, in order and deduplicated; empty when it has no label.
	ImageFeatureIDs(ctx context.Context, imageID string) ([]string, error)

	// CheckState reports, creating nothing, whether src has a state volume. It
	// wraps ErrNoStateVolume.
	CheckState(ctx context.Context, src model.Container) error

	// CheckTarget reports, creating nothing, whether dst's state volume or
	// snapshot tag already exists. It wraps ErrTargetExists.
	CheckTarget(ctx context.Context, dst model.Container) error

	// CopyState creates dst's state volume and fills it with a copy of src's,
	// through a short-lived container of helperImage, which must be present
	// locally. It wraps ErrNoStateVolume and ErrTargetExists, and deletes nothing
	// when it refuses.
	CopyState(ctx context.Context, src, dst model.Container, helperImage string) error

	// DiscardCopy removes dst's snapshot tag and state volume, whichever
	// exist. For rollback.
	DiscardCopy(ctx context.Context, dst model.Container) error
}
