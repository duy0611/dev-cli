package cli

import (
	"context"
	"errors"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/env"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/model"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/provider"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/secret"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/store"
)

// target is everything a container command needs: the record, the provider
// that runs it, and the workspace it belongs to.
type target struct {
	container model.Container
	workspace model.Workspace
	provider  provider.Provider
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
	return &target{container: c, workspace: ws, provider: p}, nil
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
