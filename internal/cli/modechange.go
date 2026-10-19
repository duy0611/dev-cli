package cli

import (
	"context"
	"slices"

	"github.com/duy0611/dev-cli/internal/dcgen"
	"github.com/duy0611/dev-cli/internal/model"
)

// checkModeChange refuses a change of mode rebuild cannot make. from == to is
// allowed.
func checkModeChange(name string, from, to model.Mode, kind model.ProviderKind) error {
	switch to {
	case model.ModeGuarded, model.ModeUnprotected, model.ModeIsolated:
	default:
		return usageErrorf("unknown mode %q; use guarded, unprotected or isolated", to)
	}
	if from == to {
		return nil
	}
	switch from {
	case model.ModeIsolated:
		return usageErrorf("container %s is isolated; it cannot leave isolated mode. "+
			"Bring its work home with dev container fetch %s, then create a new container", name, name)
	case model.ModeGuarded, model.ModeUnprotected:
	default:
		return usageErrorf("container %s is %s; only a guarded or unprotected container can change mode",
			name, displayMode(from))
	}
	if kind == model.KindK8s && to != model.ModeIsolated {
		return usageErrorf("--mode %s needs a local provider: a pod cannot mount your folder live; "+
			"use --mode isolated, the k8s default", to)
	}
	return nil
}

// withMode returns c as it would be in mode to: Mode, WorkspacePath
// (isolatedPath(c.Source) for isolated, "" otherwise), WorktreeRepo ("" for
// isolated, kept otherwise), every folder placed again with placeFolder, and a
// generated document re-rendered for the new mount. c itself is untouched.
func withMode(ctx context.Context, c model.Container, to model.Mode) (model.Container, error) {
	c.Mode = to
	c.WorkspacePath = ""
	if to == model.ModeIsolated {
		c.WorkspacePath = isolatedPath(c.Source)
		c.WorktreeRepo = ""
	}
	placed := make([]model.Folder, len(c.Folders))
	for i, f := range c.Folders {
		placed[i] = placeFolder(ctx, f.HostPath, to)
	}
	c.Folders = placed
	if c.GeneratedConfig != "" {
		tools, err := dcgen.ToolsOf(c.GeneratedConfig)
		if err != nil {
			return c, err
		}
		if c.GeneratedConfig, err = renderGenerated(c, tools); err != nil {
			return c, err
		}
	}
	return c, nil
}

// retarget replaces t with a target for the same container as it will be after
// rebuild: in mode to ("" keeps it), with its folders changed. It returns the
// row to store once the container has been recreated; nothing is stored here.
func (a *app) retarget(ctx context.Context, t *target, to model.Mode, add, remove []string) (*target, model.Container, error) {
	st, err := a.store()
	if err != nil {
		return t, model.Container{}, err
	}
	c, err := st.GetContainer(t.workspace.Name, t.container.Name)
	if err != nil {
		return t, c, err
	}
	if len(add) > 0 && (c.Mode == model.ModeGuarded || c.Mode == model.ModeUnprotected) {
		if err := a.allowExtraFolders(t.workspace.Name, c.Mode); err != nil {
			return t, c, err
		}
	}
	// In the old mode: an isolated container's folders are fixed, and the
	// folders added are placed again by withMode anyway.
	if len(add) > 0 || len(remove) > 0 {
		if err := changeFolders(ctx, &c, add, remove); err != nil {
			return t, c, err
		}
	}
	changeMode := to != "" && to != c.Mode
	if changeMode {
		if c, err = withMode(ctx, c, to); err != nil {
			return t, c, err
		}
		if to == model.ModeGuarded {
			if err := checkGuarded(a, c.Source, c.ConfigPath, c.WorktreeRepo, len(c.Folders) > 0); err != nil {
				return t, c, err
			}
		}
		if err := checkFolders(c); err != nil {
			return t, c, err
		}
	}
	if c.Mode == model.ModeGuarded {
		for _, f := range c.Folders {
			if changeMode || slices.ContainsFunc(add, func(arg string) bool { return storedPath(arg) == f.HostPath }) {
				warnGuardGaps(a, f.HostPath, f.WorktreeRepo != "", true)
			}
		}
	}
	next, err := a.target(ctx, c, t.workspace, t.provider)
	if err != nil {
		return t, c, err
	}
	t.release()
	return next, c, nil
}
