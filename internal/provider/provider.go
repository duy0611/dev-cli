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

// Syncer is implemented by providers whose containers hold a copy of the
// project rather than the project itself.
//
// Deliberately not part of Provider: the local provider bind-mounts the folder,
// so there is nothing to copy and a no-op Sync would be a lie. Callers type
// assert, and tell the operator plainly when the provider does not have it.
type Syncer interface {
	Sync(ctx context.Context, c model.Container) error
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
