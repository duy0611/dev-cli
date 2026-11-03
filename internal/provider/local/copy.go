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
	"strings"

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

// metadataLabel is where the devcontainer CLI records an image's Feature and
// config metadata.
const metadataLabel = "devcontainer.metadata"

// SnapshotImage makes imageID available as dst's snapshot tag. When the image
// carries the CLI's metadata label, the snapshot is a new image whose label
// keeps only the entries with an id (the base image and each Feature): the
// entry without one is the source's own materialised config, which the CLI
// would merge into dst's, carrying the source's mounts, privileges and
// lifecycle commands along.
func (p *Provider) SnapshotImage(ctx context.Context, dst model.Container, imageID string) (err error) {
	if err := requireBinary(dockerBin); err != nil {
		return err
	}
	tag := SnapshotTag(dst.WorkspaceName, dst.Name)
	out, err := output(ctx, dockerBin, "image", "inspect", "--format", "{{json .Config.Labels}}", imageID)
	if err != nil {
		return err
	}
	var labels map[string]string
	if err := json.Unmarshal([]byte(out), &labels); err != nil {
		return fmt.Errorf("reading the labels of %s: %w", imageID, err)
	}
	meta, ok := labels[metadataLabel]
	if !ok {
		return runDocker(ctx, "tag", imageID, tag)
	}
	filtered, err := featureMetadata(meta)
	if err != nil {
		return fmt.Errorf("reading %s of %s: %w", metadataLabel, imageID, err)
	}

	// create leaves the filesystem as the image has it; commit keeps the image's
	// config and replaces the one label.
	cid, err := output(ctx, dockerBin, "create", "--pull=never", imageID)
	if err != nil {
		return err
	}
	cid = first(cid)
	if cid == "" {
		return errors.New("docker create printed no container ID")
	}
	defer func() {
		if rmErr := runDocker(context.WithoutCancel(ctx), "rm", cid); rmErr != nil && err == nil {
			err = rmErr
		}
	}()
	return runDocker(ctx, "commit", "--change", "LABEL "+metadataLabel+"="+dockerfileString(filtered), cid, tag)
}

// featureMetadata keeps the entries of a devcontainer.metadata value that have
// an id. The CLI writes an array; an older one wrote a single object.
func featureMetadata(raw string) (string, error) {
	var entries []json.RawMessage
	if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		entries = []json.RawMessage{json.RawMessage(raw)}
	} else if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return "", err
	}
	kept := []json.RawMessage{}
	for _, e := range entries {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(e, &m); err != nil {
			return "", err
		}
		if _, ok := m["id"]; ok {
			kept = append(kept, e)
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(kept); err != nil {
		return "", err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, buf.Bytes()); err != nil {
		return "", err
	}
	return compact.String(), nil
}

// dockerfileString quotes s as one double-quoted Dockerfile word: LABEL values
// are variable-expanded, so a literal $ is escaped too.
func dockerfileString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`).Replace(s) + `"`
}

// CheckState reports whether src has a state volume to copy, so a caller can
// refuse before it writes anything.
func (p *Provider) CheckState(ctx context.Context, src model.Container) error {
	if err := requireBinary(dockerBin); err != nil {
		return err
	}
	from := StateVolumeName(src.WorkspaceName, src.Name)
	if exec.CommandContext(ctx, dockerBin, "volume", "inspect", from).Run() != nil {
		return fmt.Errorf("volume %s: %w", from, provider.ErrNoStateVolume)
	}
	return nil
}

// CheckTarget reports whether dst already has a state volume or snapshot tag.
func (p *Provider) CheckTarget(ctx context.Context, dst model.Container) error {
	if err := requireBinary(dockerBin); err != nil {
		return err
	}
	to := StateVolumeName(dst.WorkspaceName, dst.Name)
	if exec.CommandContext(ctx, dockerBin, "volume", "inspect", to).Run() == nil {
		return fmt.Errorf("volume %s: %w", to, provider.ErrTargetExists)
	}
	if p.hasSnapshotTag(ctx, dst) {
		return fmt.Errorf("image %s: %w", SnapshotTag(dst.WorkspaceName, dst.Name), provider.ErrTargetExists)
	}
	return nil
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
	// docker's volume create succeeds on an existing name and would merge into
	// it, and a rollback would then delete a volume that was never ours.
	if exec.CommandContext(ctx, dockerBin, "volume", "inspect", to).Run() == nil {
		return fmt.Errorf("volume %s: %w", to, provider.ErrTargetExists)
	}
	if err := runDocker(ctx, "volume", "create", to); err != nil {
		return err
	}
	return runDocker(ctx, "run", "--rm", "--pull=never", "--user", "0", "--network", "none",
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

// dropSnapshotTag deletes the snapshot image once its container is gone or has
// been rebuilt from a normal config. A failure is only reported: the image may
// still be a parent of an image the CLI layered on top.
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
