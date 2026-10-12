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

// target is what a container command needs: the record, its provider and its
// workspace.
type target struct {
	container model.Container
	workspace model.Workspace
	provider  provider.Provider
	// cleanup removes the generated configuration written for this container.
	cleanup func()
	// overrideErr is a failed merged-config build, held so only the commands
	// that drive up or exec fail on it.
	overrideErr error
	// gitGuard is how this invocation keeps host git from running what the
	// container writes.
	gitGuard gitGuardPlan
}

// release removes what resolve wrote for this container. Safe on a nil cleanup.
func (t *target) release() {
	if t != nil && t.cleanup != nil {
		t.cleanup()
	}
}

// requireOverride reports a deferred overlay failure, recording a git-guard
// refusal in the audit log. Called only by commands that drive up or exec:
// starting without the merged document would leave the state volume unmounted.
func (t *target) requireOverride(a *app) error {
	return a.recordRefusal(t.container, t.overrideErr)
}

// overridesConfig reports whether this provider consumes a merged config.
func overridesConfig(p provider.Provider) bool {
	_, ok := p.(provider.ConfigOverrider)
	return ok
}

// resolve finds a container by name in the workspace, or the active one, and
// builds its provider.
func (a *app) resolve(ctx context.Context, workspace, name string) (*target, error) {
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

	// Before materialise, which merges these mounts in. Not seeded: exec must
	// not reset the hooks copy. Deferred on failure, like an overlay error.
	plan, planErr := a.planGitGuard(ctx, c, c.WorktreeRepo, false, overridesConfig(p))
	if planErr == nil && overridesConfig(p) {
		c.GitGuardMounts = plan.mounts
	}

	// Every provider call needs a ConfigPath a child process can open, so this
	// runs here, not per command.
	c, cleanup, err := materialise(c, overridesConfig(p))
	if err == nil {
		err = planErr
	}
	if err != nil {
		if !isOverlayError(err) {
			return nil, err
		}
		return &target{container: c, workspace: ws, provider: p, cleanup: cleanup,
			overrideErr: err}, nil
	}
	return &target{container: c, workspace: ws, provider: p, cleanup: cleanup, gitGuard: plan}, nil
}

// materialise gives a container a config path a child process can open. A
// generated document is written to a temporary .devcontainer/devcontainer.json
// for one invocation: the CLI reads the layout around the file and rejects any
// other name. overrides is false for k8s, which builds its own spec.
func materialise(c model.Container, overrides bool) (model.Container, func(), error) {
	if c.GeneratedConfig == "" {
		return overlayProjectConfig(c, overrides)
	}

	dir, err := os.MkdirTemp("", "dev-config-")
	if err != nil {
		return c, func() {}, fmt.Errorf("preparing the generated config: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	// 0700 directory and 0600 file: the configuration describes the operator's
	// build.
	nested := filepath.Join(dir, ".devcontainer")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		cleanup()
		return c, func() {}, fmt.Errorf("preparing the generated config: %w", err)
	}
	path := filepath.Join(nested, "devcontainer.json")
	doc := []byte(c.GeneratedConfig)
	// Added per invocation, not stored, so rewriteGeneratedTools need not
	// re-derive them.
	if len(c.GitGuardMounts) > 0 {
		var err error
		if doc, err = dcgen.AppendMounts(doc, c.GitGuardMounts); err != nil {
			cleanup()
			return c, func() {}, fmt.Errorf("adding the git guard's mounts: %w", err)
		}
	}
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		cleanup()
		return c, func() {}, fmt.Errorf("writing the generated config: %w", err)
	}

	// A folderless container has no host folder, but the CLI needs
	// --workspace-folder. The temporary directory serves; the document names
	// workspaceFolder explicitly so the name does not matter.
	if c.SourceKind == model.SourceNone {
		c.Source = dir
	}

	c.ConfigPath = path
	return c, cleanup, nil
}

// overlayProjectConfig merges dev's state mount, and a worktree's bind mounts,
// into a project's devcontainer.json for one invocation. Nothing is stored: a
// stored copy would freeze the project's edits, and GeneratedConfig is
// re-rendered over by rewriteGeneratedTools. Writes nothing when neither
// applies.
func overlayProjectConfig(c model.Container, overrides bool) (model.Container, func(), error) {
	needsWorktree := c.WorktreeRepo != ""
	needsGuard := len(c.GitGuardMounts) > 0
	isolated := c.Mode == model.ModeIsolated
	if !overrides || c.ConfigPath == "" || (!c.PersistState && !needsWorktree && !needsGuard && !isolated) {
		return c, func() {}, nil
	}

	merged, err := os.ReadFile(c.ConfigPath)
	if err != nil {
		return c, func() {}, &overlayError{fmt.Errorf("reading %s: %w", c.ConfigPath, err)}
	}

	// An isolated workspace is a volume, whatever the project binds, so a
	// ${localWorkspaceFolder} bind cannot expose the operator's folder.
	if isolated {
		merged, err = dcgen.OverlayIsolatedWorkspace(merged, dcgen.Mount{
			Volume: local.VolumeName(c.WorkspaceName, c.Name),
			Folder: local.WorkspaceFolder(c.Name),
		})
		if err != nil {
			return c, func() {}, &overlayError{fmt.Errorf("merging %s: %w", c.ConfigPath, err)}
		}
	}
	// Before the state mount, so both land in one document: each merge keeps
	// the previous output.
	if needsWorktree {
		merged, err = dcgen.OverlayWorktree(merged, c.WorktreeRepo, c.Source)
		if err != nil {
			return c, func() {}, &overlayError{fmt.Errorf("merging %s: %w", c.ConfigPath, err)}
		}
	}
	if c.PersistState {
		// The same helper as the generated document, so the volume has one
		// spelling.
		merged, err = dcgen.Overlay(merged, dcgen.State{
			Volume: local.StateVolumeName(c.WorkspaceName, c.Name),
		})
		if err != nil {
			return c, func() {}, &overlayError{fmt.Errorf("merging %s: %w", c.ConfigPath, err)}
		}
	}
	// Last: the guard's mounts sit inside the .git the worktree binds bring in,
	// and docker applies mounts in order.
	if needsGuard {
		merged, err = dcgen.OverlayMounts(merged, c.GitGuardMounts)
		if err != nil {
			return c, func() {}, &overlayError{fmt.Errorf("merging %s: %w", c.ConfigPath, err)}
		}
	}

	dir, err := os.MkdirTemp("", "dev-override-")
	if err != nil {
		return c, func() {}, fmt.Errorf("preparing the merged config: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	// Flat, since --override-config takes a document and --config keeps the
	// project file's path anchoring. 0600 as for the generated one.
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

// containerEnv resolves the workspace's settings and the state variables into
// environment variables. Per invocation, so a rotated secret needs no restart.
// Taken from the container, so no command can omit the state variables.
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

	// First, so an explicit workspace setting of the same name still wins.
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

// requireRunning reports a stopped container in terms the operator can act on,
// without starting it.
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
