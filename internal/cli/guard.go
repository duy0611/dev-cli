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

// configCheck is a project's configuration as the devcontainer CLI merges it,
// read once per command and asked two questions: does it reach the host
// (escape), and is it the configuration the container was built from (drift).
// One read for both, because each read is a CLI round trip that may fetch
// feature metadata from a registry.
type configCheck struct {
	c       model.Container
	merged  []byte
	compose []string
}

// readConfig reads c's merged configuration, or returns nil when there is
// nothing to check: a generated configuration is dev's own, rendered from a
// catalog with nothing privileged in it; and a provider that is not a
// ConfigReader builds its own spec — the Kubernetes provider ignores a
// document's privileged mode, mounts and runArgs entirely.
func readConfig(ctx context.Context, p provider.Provider, c model.Container) (*configCheck, error) {
	if c.GeneratedConfig != "" {
		return nil, nil
	}
	reader, ok := p.(provider.ConfigReader)
	if !ok {
		return nil, nil
	}
	merged, compose, err := reader.MergedConfig(ctx, c)
	if err != nil {
		return nil, err
	}
	return &configCheck{c: c, merged: merged, compose: compose}, nil
}

// escape refuses a configuration that asks its engine for the host, unless the
// operator allowed it with --allow-privileged.
//
// Called at create before the row exists, at start, and at rebuild before the
// old container is touched — so a refusal costs nothing and leaves nothing
// behind.
func (a *app) escape(cc *configCheck) error {
	if cc == nil || cc.c.AllowPrivileged {
		return nil
	}
	home, err := operatorHome()
	if err != nil {
		return err
	}
	findings, err := guard.Escape(cc.merged, home)
	if err != nil {
		return err
	}
	for _, file := range cc.compose {
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
	a.record("refused", cc.c.WorkspaceName, cc.c.Name, map[string]any{
		"reason":   "escape",
		"findings": descr,
	})
	return usageErrorf("container %s asks for access to the host:\n  %s\npass --allow-privileged at create to allow it",
		cc.c.Name, strings.Join(descr, "\n  "))
}

// digest is the configuration's digest, or the zero Digest when there is
// nothing to digest (see readConfig).
func (cc *configCheck) digest() (guard.Digest, error) {
	if cc == nil {
		return guard.Digest{}, nil
	}
	return guard.DigestOf(cc.merged, cc.c.ConfigPath, cc.compose)
}

// guardNewContainer runs the create-time guards on a container whose row is
// about to be written, and fills in the digest the row records. worktreeRepo
// is git's common directory for a worktree container, so the configuration
// checked carries the same mounts the container will start with; omitted for
// any other.
//
// The container is materialised here exactly as start will materialise it —
// generated document to a file, or the project's document merged with dev's
// own mounts — because what is checked has to be what runs.
func (a *app) guardNewContainer(ctx context.Context, workspace string, c *model.Container, worktreeRepo ...string) error {
	if c.GeneratedConfig != "" {
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
	probe := *c
	if len(worktreeRepo) > 0 {
		probe.WorktreeRepo = worktreeRepo[0]
	}
	// With the git guard's mounts, so the digest recorded here matches what
	// the first start materialises. Not seeded: start does that, once the row
	// exists and the container is about to be created.
	plan, err := a.planGitGuard(probe, probe.WorktreeRepo, false)
	if err != nil {
		return err
	}
	if overridesConfig(p) {
		probe.GitGuardMounts = plan.mounts
	}
	probe, cleanup, err := materialise(probe, overridesConfig(p))
	if err != nil {
		return err
	}
	defer cleanup()

	cc, err := readConfig(ctx, p, probe)
	if err != nil {
		return err
	}
	if err := a.escape(cc); err != nil {
		return err
	}
	d, err := cc.digest()
	if err != nil {
		return err
	}
	c.ConfigDigest, c.ConfigDigestFields = d.Sum, d.Record()
	return nil
}

// checkDrift refuses a rebuild whose configuration is not the one the
// container was last built from, unless accept says to take it — and returns
// the digest to record once the rebuild has happened.
//
// A row with no digest yet (created before this check, or with --no-start and
// never started) records one without refusing: a container is guarded from its
// first digest on, never retroactively.
func (a *app) checkDrift(cc *configCheck, accept bool) (guard.Digest, error) {
	live, err := cc.digest()
	if err != nil || cc == nil {
		return live, err
	}
	if cc.c.ConfigDigest == "" || live.Sum == cc.c.ConfigDigest {
		return live, nil
	}

	stored := guard.ParseRecord(cc.c.ConfigDigest, cc.c.ConfigDigestFields)
	changed := guard.Diff(stored, live)
	if accept {
		a.record("accept-config", cc.c.WorkspaceName, cc.c.Name, map[string]any{
			"previous_digest": cc.c.ConfigDigest,
			"config_digest":   live.Sum,
			"changed":         changed,
		})
		return live, nil
	}
	a.record("refused", cc.c.WorkspaceName, cc.c.Name, map[string]any{
		"reason":   "drift",
		"findings": changed,
	})
	what := "the configuration changed"
	if len(changed) > 0 {
		what = "changed: " + strings.Join(changed, ", ")
	}
	return guard.Digest{}, usageErrorf(
		"container %s's configuration is not the one it was built from (%s)\n"+
			"review it, then pass --accept-config to rebuild with it", cc.c.Name, what)
}

// recordFirstDigest stores the digest of a configuration a container with none
// recorded has just started from.
func (a *app) recordFirstDigest(cc *configCheck) error {
	d, err := cc.digest()
	if err != nil {
		return err
	}
	st, err := a.store()
	if err != nil {
		return err
	}
	return st.SetConfigDigest(cc.c.WorkspaceName, cc.c.Name, d.Sum, d.Record())
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
