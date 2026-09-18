package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/duy0611/dev-cli/internal/env"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
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
}

// release removes anything resolve wrote for this container. Safe on a nil
// cleanup, so every caller can defer it without checking.
func (t *target) release() {
	if t != nil && t.cleanup != nil {
		t.cleanup()
	}
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
	c, cleanup, err := materialise(c)
	if err != nil {
		return nil, err
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
func materialise(c model.Container) (model.Container, func(), error) {
	if c.GeneratedConfig == "" {
		return c, func() {}, nil
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

// containerEnv resolves the workspace's settings into environment variables.
//
// Done per invocation rather than cached at create, so a rotated secret is
// picked up by the next command with no rebuild and no restart.
func (a *app) containerEnv(ctx context.Context, workspace string) ([]provider.EnvVar, error) {
	st, err := a.store()
	if err != nil {
		return nil, err
	}
	settings, err := st.ListSettings(workspace)
	if err != nil {
		return nil, err
	}
	return env.Assemble(ctx, secret.NewResolver(), settings)
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
