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
	"slices"
	"strings"
	"time"

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

// SnapshotImage makes imageID available as dst's snapshot tag. The CLI appends
// the source's own materialised config (its mounts, lifecycle commands and
// privileges) as the last entry of the metadata label, and would merge it into
// dst's; when the label ends that way the snapshot is a new image without that
// entry. Any other image is tagged as it is.
func (p *Provider) SnapshotImage(ctx context.Context, dst model.Container, imageID string) (err error) {
	if err := requireBinary(dockerBin); err != nil {
		return err
	}
	tag := SnapshotTag(dst.WorkspaceName, dst.Name)
	meta, ok, err := imageMetadata(ctx, imageID)
	if err != nil {
		return err
	}
	if !ok {
		return runDocker(ctx, "tag", imageID, tag)
	}
	filtered, dropped, err := featureMetadata(meta)
	if err != nil {
		return fmt.Errorf("reading %s of %s: %w", metadataLabel, imageID, err)
	}
	if !dropped {
		return runDocker(ctx, "tag", imageID, tag)
	}

	// A fixed name lets a leftover from a killed run be found and removed.
	tmp := "dev-copy-" + snapshotHash(dst.WorkspaceName, dst.Name) + "-tmp"
	_ = exec.CommandContext(ctx, dockerBin, "rm", "-f", tmp).Run()
	// create leaves the filesystem as the image has it; commit keeps the image's
	// config and replaces the one label.
	if err := runDocker(ctx, "create", "--pull=never", "--name", tmp, imageID); err != nil {
		return err
	}
	defer func() {
		rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if rmErr := runDocker(rmCtx, "rm", "-f", tmp); rmErr != nil && err == nil {
			err = rmErr
		}
	}()
	return runDocker(ctx, "commit", "--change", "LABEL "+metadataLabel+"="+dockerfileString(filtered), tmp, tag)
}

// imageMetadata returns the devcontainer.metadata label of an image, and
// whether it has one.
func imageMetadata(ctx context.Context, imageID string) (string, bool, error) {
	out, err := output(ctx, dockerBin, "image", "inspect", "--format", "{{json .Config.Labels}}", imageID)
	if err != nil {
		return "", false, err
	}
	var labels map[string]string
	if err := json.Unmarshal([]byte(out), &labels); err != nil {
		return "", false, fmt.Errorf("reading the labels of %s: %w", imageID, err)
	}
	meta, ok := labels[metadataLabel]
	return meta, ok, nil
}

// ImageFeatureIDs returns the id of each metadata entry of imageID, in order
// and without repeats. An image with no label has none.
func (p *Provider) ImageFeatureIDs(ctx context.Context, imageID string) ([]string, error) {
	if err := requireBinary(dockerBin); err != nil {
		return nil, err
	}
	meta, ok, err := imageMetadata(ctx, imageID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	entries, err := metadataEntries(meta)
	if err != nil {
		return nil, fmt.Errorf("reading %s of %s: %w", metadataLabel, imageID, err)
	}
	var ids []string
	for _, e := range entries {
		m, err := entryFields(e)
		if err != nil {
			return nil, fmt.Errorf("reading %s of %s: %w", metadataLabel, imageID, err)
		}
		var id string
		if raw, ok := m["id"]; ok && json.Unmarshal(raw, &id) == nil && id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// metadataEntries parses a devcontainer.metadata value. The CLI writes an
// array; an older one wrote a single object.
func metadataEntries(raw string) ([]json.RawMessage, error) {
	if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		raw = "[" + raw + "]"
	}
	var entries []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func entryFields(e json.RawMessage) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	err := json.Unmarshal(e, &m)
	return m, err
}

// configEntryKeys are what the CLI's own config entry carries when the source
// had any of it, and dev always adds a state mount to a source's config.
var configEntryKeys = []string{"mounts", "privileged", "capAdd", "securityOpt",
	"initializeCommand", "onCreateCommand", "updateContentCommand",
	"postCreateCommand", "postStartCommand", "postAttachCommand"}

// featureMetadata drops the source's own config entry from a metadata value.
// Nothing in the image says whether the CLI built it, and base images carry
// entries without an id too (mcr's remoteUser), so the entry is recognised by
// position and content: last, no id, and holding what a dev config always
// does. dropped is false when nothing was removed.
func featureMetadata(raw string) (filtered string, dropped bool, err error) {
	entries, err := metadataEntries(raw)
	if err != nil {
		return "", false, err
	}
	if n := len(entries); n > 0 {
		last, err := entryFields(entries[n-1])
		if err != nil {
			return "", false, err
		}
		_, hasID := last["id"]
		carries := false
		for _, k := range configEntryKeys {
			if _, ok := last[k]; ok {
				carries = true
			}
		}
		if !hasID && carries {
			entries, dropped = entries[:n-1], true
		}
	}
	if entries == nil {
		entries = []json.RawMessage{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(entries); err != nil {
		return "", false, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, buf.Bytes()); err != nil {
		return "", false, err
	}
	return compact.String(), dropped, nil
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

// CheckTarget reports whether dst already has a state volume or a snapshot tag
// of either kind.
func (p *Provider) CheckTarget(ctx context.Context, dst model.Container) error {
	if err := requireBinary(dockerBin); err != nil {
		return err
	}
	to := StateVolumeName(dst.WorkspaceName, dst.Name)
	if exec.CommandContext(ctx, dockerBin, "volume", "inspect", to).Run() == nil {
		return fmt.Errorf("volume %s: %w", to, provider.ErrTargetExists)
	}
	for _, tag := range snapshotTags(dst) {
		if hasImage(ctx, tag) {
			return fmt.Errorf("image %s: %w", tag, provider.ErrTargetExists)
		}
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

// DiscardCopy removes dst's snapshot tags and state volume.
func (p *Provider) DiscardCopy(ctx context.Context, dst model.Container) error {
	if err := requireBinary(dockerBin); err != nil {
		return err
	}
	for _, tag := range snapshotTags(dst) {
		if hasImage(ctx, tag) {
			if err := runDocker(ctx, "rmi", tag); err != nil {
				return err
			}
		}
	}
	return runDocker(ctx, "volume", "rm", "--force", StateVolumeName(dst.WorkspaceName, dst.Name))
}

func snapshotTags(c model.Container) []string {
	return []string{SnapshotTag(c.WorkspaceName, c.Name), SnapshotKeptTag(c.WorkspaceName, c.Name)}
}

// hasImage asks for the image ID rather than trusting the exit status, so an
// engine that answers nothing counts as having no such image.
func hasImage(ctx context.Context, ref string) bool {
	if _, err := exec.LookPath(dockerBin); err != nil {
		return false
	}
	out, err := output(ctx, dockerBin, "image", "inspect", "--format", "{{.Id}}", ref)
	return err == nil && first(out) != ""
}

// dropSnapshotTag deletes both snapshot tags once the container is gone or has
// been rebuilt from a normal config. A failure is only reported: the image may
// still be a parent of an image the CLI layered on top.
func (p *Provider) dropSnapshotTag(ctx context.Context, c model.Container) {
	for _, tag := range snapshotTags(c) {
		if !hasImage(ctx, tag) {
			continue
		}
		if err := runDocker(ctx, "rmi", tag); err != nil {
			fmt.Fprintf(os.Stderr, "dev: removing the snapshot tag of container %s: %v\n", c.Name, err)
		}
	}
}

// keepSnapshot swaps the snapshot's tag for the kept one after the first
// successful up, so the rewrite cannot apply a second time. Docker untags
// rather than deletes the image, since the kept tag remains. Failures are only
// reported: the container is already up.
func (p *Provider) keepSnapshot(ctx context.Context, c model.Container) {
	from, kept := SnapshotTag(c.WorkspaceName, c.Name), SnapshotKeptTag(c.WorkspaceName, c.Name)
	if err := runDocker(ctx, "tag", from, kept); err != nil {
		fmt.Fprintf(os.Stderr, "dev: keeping the copied image of container %s: %v\n", c.Name, err)
		return
	}
	if err := runDocker(ctx, "rmi", from); err != nil {
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
	if !hasImage(ctx, SnapshotTag(c.WorkspaceName, c.Name)) {
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
