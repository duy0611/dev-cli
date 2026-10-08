package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/duy0611/dev-cli/internal/guard"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/xpath"
)

// checkEscape refuses a container whose configuration asks its engine for the
// host, unless the operator allowed it with --allow-privileged.
//
// Run at create, before the row exists, and at rebuild, before the old
// container is touched — so a refusal costs nothing and leaves nothing behind.
//
// Only on a provider that hands the document to an engine (ConfigReader): the
// Kubernetes provider builds its own pod spec, and privileged mode, mounts and
// runArgs in a devcontainer.json never reach a pod.
//
// A generated configuration is dev's own, rendered from a catalog with nothing
// privileged in it, so it is not read: the CLI round trip would cost seconds
// and a registry fetch for a document whose answer is known.
func (a *app) checkEscape(ctx context.Context, p provider.Provider, c model.Container) error {
	if c.AllowPrivileged || c.GeneratedConfig != "" {
		return nil
	}
	reader, ok := p.(provider.ConfigReader)
	if !ok {
		return nil
	}

	merged, composeFiles, err := reader.MergedConfig(ctx, c)
	if err != nil {
		return err
	}
	home, err := operatorHome()
	if err != nil {
		return err
	}
	findings, err := guard.Escape(merged, home)
	if err != nil {
		return err
	}
	for _, file := range composeFiles {
		content, err := os.ReadFile(file)
		if err != nil {
			// Refused rather than skipped: a compose file the check cannot read
			// is a configuration it has not checked.
			return fmt.Errorf("reading %s to check it: %w", file, err)
		}
		more, err := guard.Compose(xpath.Shorten(file), content, home)
		if err != nil {
			return err
		}
		findings = append(findings, more...)
	}
	if len(findings) == 0 {
		return nil
	}

	descr := make([]string, len(findings))
	for i, f := range findings {
		descr[i] = f.String()
	}
	a.record("refused", c.WorkspaceName, c.Name, map[string]any{
		"reason":   "escape",
		"findings": descr,
	})
	return usageErrorf("container %s asks for access to the host:\n  %s\npass --allow-privileged at create to allow it",
		c.Name, strings.Join(descr, "\n  "))
}

// guardNewContainer runs the create-time guards on a container whose row is
// about to be written. worktreeRepo is git's common directory for a worktree
// container, so the configuration checked carries the same mounts the
// container will start with; omitted for any other.
//
// The container is materialised here exactly as start will materialise it —
// generated document to a file, or the project's document merged with dev's
// own mounts — because what is checked has to be what runs.
func (a *app) guardNewContainer(ctx context.Context, workspace string, c model.Container, worktreeRepo ...string) error {
	if c.AllowPrivileged || c.GeneratedConfig != "" {
		return nil
	}
	st, err := a.store()
	if err != nil {
		return err
	}
	ws, err := st.GetWorkspace(workspace)
	if err != nil {
		return err
	}
	p, err := a.providerFor(ws)
	if err != nil {
		return err
	}
	if len(worktreeRepo) > 0 {
		c.WorktreeRepo = worktreeRepo[0]
	}
	c, cleanup, err := materialise(c, overridesConfig(p))
	if err != nil {
		return err
	}
	defer cleanup()
	return a.checkEscape(ctx, p, c)
}

// operatorHome is the home directory as the engine would see it: resolved, so
// a mount naming it through a symlink is still recognised (invariant 2).
func operatorHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating the home directory: %w", err)
	}
	if resolved, err := xpath.Resolve(home); err == nil {
		return resolved, nil
	}
	return home, nil
}

// hostAccessOf is what container list shows under HOST: whether the container
// may ask its engine for the host. "allowed" for a row created with
// --allow-privileged — and for every row created before the guard existed,
// which the migration left as it was — "refused" otherwise.
func hostAccessOf(c model.Container) string {
	if c.AllowPrivileged {
		return "allowed"
	}
	return "refused"
}
