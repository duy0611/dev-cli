package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/duy0611/dev-cli/internal/dcgen"
	"github.com/duy0611/dev-cli/internal/env"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/provider/local"
	"github.com/duy0611/dev-cli/internal/secret"
	"github.com/duy0611/dev-cli/internal/store"
)

// target is everything a container command needs: the record, the provider
// that runs it, and the workspace it belongs to.
type target struct {
	container model.Container
	workspace model.Workspace
	provider  provider.Provider
	// cleanup removes whatever resolve had to write to disk for this
	// container, which is the generated configuration and nothing else. Every
	// caller defers release; see materialise.
	cleanup func()
	// overrideErr is a failure to build the merged devcontainer.json, held
	// rather than returned. The commands that drive up or exec surface it
	// through requireOverride; the ones that do not need it carry on, so a
	// project file that does not parse never strands a container in the engine.
	overrideErr error
}

// release removes anything resolve wrote for this container. Safe on a nil
// cleanup, so every caller can defer it without checking.
func (t *target) release() {
	if t != nil && t.cleanup != nil {
		t.cleanup()
	}
}

// requireOverride reports a deferred overlay failure.
//
// Called by every path that drives up or exec, and by no other: those are the
// commands that need the merged document, and starting a container without it
// would silently leave the agents' state volume unmounted.
func (t *target) requireOverride() error {
	return t.overrideErr
}

// overridesConfig reports whether this provider consumes a merged config.
func overridesConfig(p provider.Provider) bool {
	_, ok := p.(provider.ConfigOverrider)
	return ok
}

// resolve looks up a container by name in the given workspace (or the active
// one) and builds its provider.
func (a *app) resolve(workspace, name string) (*target, error) {
	wsName, err := a.workspaceName(workspace)
	if err != nil {
		return nil, err
	}
	st, err := a.store()
	if err != nil {
		return nil, err
	}

	c, err := st.GetContainer(wsName, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, notFoundErrorf("no such container: %s (workspace %s)", name, wsName)
		}
		return nil, err
	}

	ws, err := st.GetWorkspace(wsName)
	if err != nil {
		return nil, err
	}
	p, err := a.providerFor(ws)
	if err != nil {
		return nil, err
	}

	// Done here rather than in each command: every provider call needs a
	// container whose ConfigPath a child process can open, and a command that
	// forgot would fail only for generated containers.
	c, cleanup, err := materialise(c, overridesConfig(p))
	if err != nil {
		if !isOverlayError(err) {
			return nil, err
		}
		// Deferred, not fatal. See target.overrideErr.
		return &target{container: c, workspace: ws, provider: p, cleanup: cleanup,
			overrideErr: err}, nil
	}
	return &target{container: c, workspace: ws, provider: p, cleanup: cleanup}, nil
}

// materialise gives a container a config path a child process can open.
//
// A generated configuration lives in the database, and the devcontainer CLI
// takes a path and nothing else — so it becomes a file for the length of one
// invocation. Written into a .devcontainer directory because the CLI reads the
// layout around the file, and named devcontainer.json because it rejects any
// other filename outright.
//
// For a folderless container, the temporary directory holding the configuration
// is also supplied as the workspace folder, since the CLI requires one and the
// container has no host directory.
//
// The returned cleanup is always safe to call, including for a project-owned
// container where nothing was written.
//
// overrides says whether this container's provider consumes a merged
// devcontainer.json. False for Kubernetes, which builds its own pod spec.
func materialise(c model.Container, overrides bool) (model.Container, func(), error) {
	if c.GeneratedConfig == "" {
		return overlayProjectConfig(c, overrides)
	}

	dir, err := os.MkdirTemp("", "dev-config-")
	if err != nil {
		return c, func() {}, fmt.Errorf("preparing the generated config: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	// 0700 on the directory and 0600 on the file: the configuration says what
	// the operator is building, and on a shared host that is nobody else's
	// business.
	nested := filepath.Join(dir, ".devcontainer")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		cleanup()
		return c, func() {}, fmt.Errorf("preparing the generated config: %w", err)
	}
	path := filepath.Join(nested, "devcontainer.json")
	if err := os.WriteFile(path, []byte(c.GeneratedConfig), 0o600); err != nil {
		cleanup()
		return c, func() {}, fmt.Errorf("writing the generated config: %w", err)
	}

	// A folderless container has no host directory, but the devcontainer CLI
	// takes --workspace-folder on every invocation and the k8s provider uses
	// it as a build context. The temporary directory holding the configuration
	// serves as both: it exists, it is empty, and the generated document names
	// an explicit workspaceFolder so nothing downstream depends on its name.
	if c.SourceKind == model.SourceNone {
		c.Source = dir
	}

	c.ConfigPath = path
	return c, cleanup, nil
}

// overlayProjectConfig merges dev's state mount into a project's own
// devcontainer.json for the length of one invocation.
//
// Per invocation rather than stored: a project-owned container picks up edits
// to its own devcontainer.json today because the CLI reads the live file, and a
// merge cached at create would freeze a snapshot — add a feature, rebuild, and
// get the document as it stood weeks ago with no error to explain it.
// GeneratedConfig is the wrong home for a second reason: rewriteGeneratedTools
// re-renders it from the tool list and would destroy a project-derived copy.
//
// Nothing is written for a container that does not persist state, has no config
// of its own, or runs on a provider that ignores a document's mounts.
func overlayProjectConfig(c model.Container, overrides bool) (model.Container, func(), error) {
	if !overrides || !c.PersistState || c.ConfigPath == "" {
		return c, func() {}, nil
	}

	raw, err := os.ReadFile(c.ConfigPath)
	if err != nil {
		return c, func() {}, &overlayError{fmt.Errorf("reading %s: %w", c.ConfigPath, err)}
	}
	// The same helper the generated document uses, so the volume has one
	// spelling across create, up and remove.
	merged, err := dcgen.Overlay(raw, dcgen.State{
		Volume: local.StateVolumeName(c.WorkspaceName, c.Name),
	})
	if err != nil {
		return c, func() {}, &overlayError{fmt.Errorf("merging %s: %w", c.ConfigPath, err)}
	}

	dir, err := os.MkdirTemp("", "dev-override-")
	if err != nil {
		return c, func() {}, fmt.Errorf("preparing the merged config: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	// Flat rather than inside a .devcontainer directory: --override-config takes
	// a document, while --config is what names the project's own file and keeps
	// its path anchoring. 0600 for the reason the generated config uses it.
	path := filepath.Join(dir, "devcontainer.json")
	if err := os.WriteFile(path, merged, 0o600); err != nil {
		cleanup()
		return c, func() {}, fmt.Errorf("writing the merged config: %w", err)
	}

	c.OverrideConfigPath = path
	return c, cleanup, nil
}

// providerFor builds the Provider a workspace runs on.
func (a *app) providerFor(ws model.Workspace) (provider.Provider, error) {
	st, err := a.store()
	if err != nil {
		return nil, err
	}
	rec, err := st.GetProvider(ws.ProviderName)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, notFoundErrorf("workspace %s names provider %s, which no longer exists",
				ws.Name, ws.ProviderName)
		}
		return nil, err
	}
	return provider.New(rec)
}

// containerEnv resolves the workspace's settings into environment variables,
// with the variables pointing agents at the state volume underneath.
//
// Done per invocation rather than cached at create, so a rotated secret is
// picked up by the next command with no rebuild and no restart.
//
// Takes the container rather than a flag so that no command can forget the
// state variables: a generated document also carries them in containerEnv, but
// a project-owned one has no document for dev to put them in, and they would
// otherwise reach only half the containers.
func (a *app) containerEnv(ctx context.Context, c model.Container) ([]provider.EnvVar, error) {
	st, err := a.store()
	if err != nil {
		return nil, err
	}
	settings, err := st.ListSettings(c.WorkspaceName)
	if err != nil {
		return nil, err
	}
	environ, err := env.Assemble(ctx, secret.NewResolver(), settings)
	if err != nil {
		return nil, err
	}
	if !c.PersistState {
		return environ, nil
	}

	// First, so an explicit workspace setting of the same name still wins: the
	// same precedence env.Assemble gives the git identity, and for the same
	// reason — this is a default the operator may have a better answer for.
	out := make([]provider.EnvVar, 0, len(environ)+len(dcgen.StateEnv()))
	defined := make(map[string]bool, len(environ))
	for _, e := range environ {
		defined[e.Key] = true
	}
	stateEnv := dcgen.StateEnv()
	for _, k := range dcgen.StateEnvKeys() {
		if !defined[k] {
			out = append(out, provider.EnvVar{Key: k, Value: stateEnv[k]})
		}
	}
	return append(out, environ...), nil
}

// requireRunning starts nothing; it reports the state in the terms the operator
// needs to act on.
func (a *app) requireRunning(ctx context.Context, t *target) error {
	status, err := t.provider.Status(ctx, t.container)
	if err != nil {
		return err
	}
	if status != model.StatusRunning {
		return notFoundErrorf("container %s is not running; run: dev container start %s",
			t.container.Name, t.container.Name)
	}
	return nil
}
