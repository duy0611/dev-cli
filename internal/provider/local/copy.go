package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/tailscale/hujson"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

var _ provider.Copier = (*Provider)(nil)

// SourceImage returns the image ID of src's container.
func (p *Provider) SourceImage(ctx context.Context, src model.Container) (string, error) {
	id, err := p.containerID(ctx, src)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("container %s: %w", src.Name, provider.ErrNoContainer)
	}
	out, err := output(ctx, dockerBin, "inspect", "--type", "container", "--format", "{{.Image}}", id)
	if err != nil {
		return "", err
	}
	image := first(out)
	if image == "" {
		return "", fmt.Errorf("container %s: the engine reported no image", src.Name)
	}
	// The container keeps its image ID even after the image is untagged or
	// removed, so ask the engine whether it is still there.
	if exec.CommandContext(ctx, dockerBin, "image", "inspect", "--format", "{{.Id}}", image).Run() != nil {
		return "", fmt.Errorf("container %s: %w", src.Name, provider.ErrNoImage)
	}
	return image, nil
}

// SnapshotImage tags imageID for dst.
func (p *Provider) SnapshotImage(ctx context.Context, dst model.Container, imageID string) error {
	if err := requireBinary(dockerBin); err != nil {
		return err
	}
	return runDocker(ctx, "tag", imageID, SnapshotTag(dst.WorkspaceName, dst.Name))
}

// CopyState fills dst's new state volume from src's. cp runs as the helper's
// entrypoint, so the image needs neither a shell nor a cooperative USER.
// --pull=never is spelled the same by docker and podman: the image is the
// source's, already local, and a pull would turn a missing one into a slow
// failure.
func (p *Provider) CopyState(ctx context.Context, src, dst model.Container, helperImage string) error {
	if err := requireBinary(dockerBin); err != nil {
		return err
	}
	from := StateVolumeName(src.WorkspaceName, src.Name)
	to := StateVolumeName(dst.WorkspaceName, dst.Name)
	if exec.CommandContext(ctx, dockerBin, "volume", "inspect", from).Run() != nil {
		return fmt.Errorf("volume %s: %w", from, provider.ErrNoStateVolume)
	}
	if err := runDocker(ctx, "volume", "create", to); err != nil {
		return err
	}
	return runDocker(ctx, "run", "--rm", "--pull=never", "--user", "0",
		"--entrypoint", "cp",
		"-v", from+":/from:ro", "-v", to+":/to",
		helperImage, "-a", "/from/.", "/to/")
}

// DiscardCopy removes dst's snapshot tag and state volume.
func (p *Provider) DiscardCopy(ctx context.Context, dst model.Container) error {
	if err := requireBinary(dockerBin); err != nil {
		return err
	}
	if p.hasSnapshotTag(ctx, dst) {
		if err := runDocker(ctx, "rmi", SnapshotTag(dst.WorkspaceName, dst.Name)); err != nil {
			return err
		}
	}
	return runDocker(ctx, "volume", "rm", "--force", StateVolumeName(dst.WorkspaceName, dst.Name))
}

// hasSnapshotTag asks for the image ID rather than trusting the exit status, so
// an engine that answers nothing counts as having no tag.
func (p *Provider) hasSnapshotTag(ctx context.Context, c model.Container) bool {
	if _, err := exec.LookPath(dockerBin); err != nil {
		return false
	}
	out, err := output(ctx, dockerBin, "image", "inspect", "--format", "{{.Id}}",
		SnapshotTag(c.WorkspaceName, c.Name))
	return err == nil && first(out) != ""
}

// dropSnapshotTag untags the snapshot. A failure is only reported: docker
// refuses to remove the last tag of an image a container uses, and by now the
// container is up and the tag is a leftover.
func (p *Provider) dropSnapshotTag(ctx context.Context, c model.Container) {
	if !p.hasSnapshotTag(ctx, c) {
		return
	}
	if err := runDocker(ctx, "rmi", SnapshotTag(c.WorkspaceName, c.Name)); err != nil {
		fmt.Fprintf(os.Stderr, "dev: removing the snapshot tag of container %s: %v\n", c.Name, err)
	}
}

// snapshotPaths are the config files up passes when it starts from a snapshot.
type snapshotPaths struct{ config, override string }

// snapshotConfig rewrites the configuration for a first start from a snapshot:
// a copied image is used as it is, so everything that builds one is dropped.
// ok is false when the normal path applies, which is when there is no snapshot
// or the engine already has a container. The document the CLI would read is the
// override when there is one; otherwise a project's own file goes in as an
// override, and a generated one replaces --config.
func (p *Provider) snapshotConfig(ctx context.Context, c model.Container) (paths snapshotPaths, cleanup func(), ok bool, err error) {
	if !p.hasSnapshotTag(ctx, c) {
		return paths, nil, false, nil
	}
	id, err := p.containerID(ctx, c)
	if err != nil || id != "" {
		return paths, nil, false, err
	}

	asOverride := c.OverrideConfigPath != "" || c.GeneratedConfig == ""
	src := c.ConfigPath
	if c.OverrideConfigPath != "" {
		src = c.OverrideConfigPath
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return paths, nil, false, fmt.Errorf("reading %s: %w", src, err)
	}
	doc, err := rewriteForSnapshot(raw, SnapshotTag(c.WorkspaceName, c.Name))
	if err != nil {
		return paths, nil, false, fmt.Errorf("rewriting %s for the copied image: %w", src, err)
	}

	dir, err := os.MkdirTemp("", "dev-snapshot-")
	if err != nil {
		return paths, nil, false, fmt.Errorf("preparing the copied-image config: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "devcontainer.json")
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		cleanup()
		return paths, nil, false, fmt.Errorf("writing the copied-image config: %w", err)
	}

	paths = snapshotPaths{config: c.ConfigPath, override: c.OverrideConfigPath}
	if asOverride {
		paths.override = path
	} else {
		paths.config = path
	}
	return paths, cleanup, true, nil
}

// rewriteForSnapshot points a devcontainer.json at image and removes every
// field that would build one.
func rewriteForSnapshot(raw []byte, image string) ([]byte, error) {
	std, err := hujson.Standardize(raw)
	if err != nil {
		return nil, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(std, &doc); err != nil {
		return nil, err
	}
	if _, ok := doc["dockerComposeFile"]; ok {
		return nil, errors.New("dockerComposeFile has one image per service")
	}
	for _, k := range []string{"features", "build", "dockerFile", "dockerfile", "context"} {
		delete(doc, k)
	}
	doc["image"], _ = json.Marshal(image)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
