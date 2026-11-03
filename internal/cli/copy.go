package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/duy0611/dev-cli/internal/dcgen"
	"github.com/duy0611/dev-cli/internal/guard"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

// copyKinds is what --copy asked to carry over from the source.
type copyKinds struct{ image, state bool }

func (k copyKinds) any() bool { return k.image || k.state }

// list is the kinds in a fixed order, for the audit record.
func (k copyKinds) list() []string {
	var out []string
	if k.image {
		out = append(out, "image")
	}
	if k.state {
		out = append(out, "state")
	}
	return out
}

func parseCopy(entries []string, from string) (copyKinds, error) {
	var k copyKinds
	if len(entries) == 0 {
		return k, nil
	}
	if from == "" {
		return k, usageErrorf("--copy needs --from: it copies from another container")
	}
	for _, e := range entries {
		switch strings.TrimSpace(e) {
		case "image":
			k.image = true
		case "state":
			k.state = true
		default:
			return k, usageErrorf("--copy: %q is not image or state", e)
		}
	}
	return k, nil
}

// copyPlan is a validated --copy, ready to run once the row is written.
type copyPlan struct {
	kinds   copyKinds
	src     model.Container
	imageID string
	p       provider.Provider
	copier  provider.Copier
}

// planCopy runs every check and engine lookup --copy needs, so a refusal
// happens before anything is written. configPath is the new container's own
// project config, empty when dev generates it.
func (a *app) planCopy(ctx context.Context, wsName string, kinds copyKinds, src model.Container,
	opts createOpts, mode model.Mode, configPath string) (*copyPlan, error) {
	// Before the provider check, so the refusal on k8s names isolation.
	if src.Mode == model.ModeIsolated || mode == model.ModeIsolated {
		who := "the new container"
		if src.Mode == model.ModeIsolated {
			who = "container " + src.Name
		}
		return nil, usageErrorf("--copy: %s is isolated; isolated containers cannot share an image or state, "+
			"since an agent in one could have planted anything in them", who)
	}
	kind, err := a.providerKind(wsName)
	if err != nil {
		return nil, err
	}
	if kind != model.KindLocal {
		return nil, usageErrorf("--copy needs the local provider; workspace %s runs on a %s provider", wsName, kind)
	}

	if kinds.state {
		switch {
		case !src.PersistState:
			return nil, usageErrorf("--copy state: container %s keeps no state volume (it was created with --no-persist-state)", src.Name)
		case opts.noPersistState:
			return nil, usageErrorf("--copy state contradicts --no-persist-state: the copy needs a state volume to fill")
		}
	}
	for _, f := range []struct{ who, path string }{{"container " + src.Name, src.ConfigPath}, {"the new container", configPath}} {
		if usesCompose(f.path) {
			return nil, usageErrorf("--copy: %s uses docker compose, which has one image per service and an unreliable state volume; drop --copy", f.who)
		}
	}
	if kinds.image && opts.toolsGiven {
		return nil, usageErrorf("--copy image cannot be combined with --tools: %s's tools are already baked into its image", src.Name)
	}

	p, err := a.providerForWorkspace(wsName)
	if err != nil {
		return nil, err
	}
	copier, ok := p.(provider.Copier)
	if !ok {
		return nil, usageErrorf("--copy is not supported by this workspace's provider")
	}
	// State alone still needs the image: its helper container runs from it.
	imageID, err := copier.SourceImage(ctx, src)
	switch {
	case errors.Is(err, provider.ErrNoContainer):
		return nil, usageErrorf("--copy: the engine has no container for %s; start %s once (dev container start %s), or drop --copy",
			src.Name, src.Name, src.Name)
	case errors.Is(err, provider.ErrNoImage):
		return nil, usageErrorf("--copy: %s's image no longer exists locally; rebuild %s (dev container rebuild %s), or drop --copy image",
			src.Name, src.Name, src.Name)
	case err != nil:
		return nil, err
	}
	if kinds.state {
		if err := copier.CheckState(ctx, src); errors.Is(err, provider.ErrNoStateVolume) {
			return nil, usageErrorf("--copy state: %s has no state volume in the engine; drop --copy state", src.Name)
		} else if err != nil {
			return nil, err
		}
	}
	return &copyPlan{kinds: kinds, src: src, imageID: imageID, p: p, copier: copier}, nil
}

// usesCompose is false for an unreadable file: the commands that need the file
// report it.
func usesCompose(path string) bool {
	if path == "" {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	compose, err := dcgen.UsesCompose(raw)
	return err == nil && compose
}

// matchImage refuses a new project-owned container whose configuration would
// not have produced the source's image. probe carries the new container's
// live digest.
func (cp *copyPlan) matchImage(probe model.Container) error {
	const advice = "; drop --copy image and build"
	if cp.src.ConfigDigest == "" {
		return usageErrorf("--copy image: container %s has no recorded configuration to compare with "+
			"(it was never started, or predates the check)%s", cp.src.Name, advice)
	}
	diff := guard.ImageDiff(
		guard.ParseRecord(cp.src.ConfigDigest, cp.src.ConfigDigestFields),
		guard.ParseRecord(probe.ConfigDigest, probe.ConfigDigestFields))
	if len(diff) > 0 {
		return usageErrorf("--copy image: this folder's configuration differs from the one %s was built from (%s)%s",
			cp.src.Name, strings.Join(diff, ", "), advice)
	}
	return nil
}

// run tags the image and fills the state volume.
func (cp *copyPlan) run(ctx context.Context, a *app, c model.Container) error {
	if cp.kinds.image {
		if err := cp.copier.SnapshotImage(ctx, c, cp.imageID); err != nil {
			return fmt.Errorf("tagging %s's image for %s: %w", cp.src.Name, c.Name, err)
		}
	}
	if cp.kinds.state {
		// Only a snapshot when the source is up; a failed lookup just means no
		// warning.
		if status, err := cp.p.Status(ctx, cp.src); err == nil && status == model.StatusRunning {
			warnf(a, "%s is running, so its state is copied as it is changing and a file may come out torn; "+
				"dev container stop %s first for a clean copy", cp.src.Name, cp.src.Name)
		}
		if err := cp.copier.CopyState(ctx, cp.src, c, cp.imageID); err != nil {
			return fmt.Errorf("copying %s's state to %s: %w", cp.src.Name, c.Name, err)
		}
	}
	return nil
}

// undo removes everything a failed create made, and never touches the source.
// cause is returned, after the cleanup's own failures are reported.
func (cp *copyPlan) undo(ctx context.Context, a *app, c model.Container, cause error) error {
	if err := cp.copier.DiscardCopy(ctx, c); err != nil {
		warnf(a, "removing the copy made for %s: %v", c.Name, err)
	}
	if err := cp.p.Remove(ctx, c); err != nil {
		warnf(a, "removing container %s: %v", c.Name, err)
	}
	if st, err := a.store(); err == nil {
		if err := st.DeleteContainer(c.WorkspaceName, c.Name); err != nil {
			warnf(a, "deleting the record of container %s: %v", c.Name, err)
		}
	}
	return cause
}

func (a *app) providerForWorkspace(wsName string) (provider.Provider, error) {
	st, err := a.store()
	if err != nil {
		return nil, err
	}
	ws, err := st.GetWorkspace(wsName)
	if err != nil {
		return nil, err
	}
	return a.providerFor(ws)
}
