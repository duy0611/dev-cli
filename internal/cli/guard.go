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

// configCheck is a project's configuration as the devcontainer CLI merges it.
// It is read once per command, since each read is a CLI round trip.
type configCheck struct {
	c           model.Container
	merged      []byte
	compose     []string
	treeTargets []string
}

// readConfig reads c's merged configuration, or returns nil when there is
// nothing to check: generated configurations are dev's own, and the Kubernetes
// provider builds its own spec.
func readConfig(ctx context.Context, p provider.Provider, c model.Container, plan gitGuardPlan) (*configCheck, error) {
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
	return &configCheck{c: c, merged: merged, compose: compose, treeTargets: plan.ownTargets()}, nil
}

// escape refuses a configuration that reaches the host unless
// --allow-privileged was given.
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
			// Refused rather than skipped: a compose file that cannot be read
			// was not checked.
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
// nothing to digest.
func (cc *configCheck) digest() (guard.Digest, error) {
	if cc == nil {
		return guard.Digest{}, nil
	}
	merged, err := guard.WithoutMounts(cc.merged, cc.treeTargets)
	if err != nil {
		return guard.Digest{}, err
	}
	return guard.DigestOf(merged, cc.c.ConfigPath, cc.compose)
}

// guardNewContainer runs the create-time guards and fills in the digest the row
// records. The configuration is materialised exactly as start will materialise
// it, so what is checked is what runs.
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
	// With the git guard's mounts, so the digest matches what the first start
	// materialises.
	plan, err := a.planGitGuard(ctx, probe, probe.WorktreeRepo, false, overridesConfig(p))
	if err != nil {
		return a.recordRefusal(*c, err)
	}
	if overridesConfig(p) {
		probe.GitGuardMounts = plan.mounts
	}
	probe, cleanup, err := materialise(probe, overridesConfig(p))
	if err != nil {
		return err
	}
	defer cleanup()

	cc, err := readConfig(ctx, p, probe, plan)
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

// checkDrift refuses a rebuild whose configuration differs from the one last
// built, unless accept is set. Returns the digest to record afterwards.
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

// recordFirstDigest stores the digest of the configuration a container first
// starts from.
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

// operatorHome is the home directory as the engine sees it, resolved through
// symlinks.
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

// hostAccessOf is the HOST column: whether the container may ask its engine for
// the host.
func hostAccessOf(c model.Container) string {
	if c.AllowPrivileged {
		return "allowed"
	}
	return "refused"
}
