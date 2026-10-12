package dcgen

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tailscale/hujson"
)

// Overlay merges dev's state mount into a project's own devcontainer.json. The
// --override-config flag replaces the document rather than merging it, so every
// field dev does not touch is passed through unchanged. Only mounts is
// rewritten; environment already reaches the container through --remote-env.
//
// dev owns StateDir while it drives: an existing mount there is replaced,
// because docker refuses a duplicate mount destination.
func Overlay(projectConfig []byte, state State) ([]byte, error) {
	doc, err := parseConfig(projectConfig)
	if err != nil {
		return nil, err
	}
	// The string spelling, matching what Render writes.
	own := ownMount{
		target: StateDir,
		entry:  fmt.Sprintf("source=%s,target=%s,type=volume", state.Volume, StateDir),
	}
	if err := mergeMounts(doc, own); err != nil {
		return nil, err
	}
	return marshalConfig(doc)
}

// OverlayWorktree merges a worktree's two bind mounts, the common directory and
// the checkout, into a project's devcontainer.json. dev owns both targets while
// it drives, as with Overlay.
func OverlayWorktree(projectConfig []byte, repo, path string) ([]byte, error) {
	doc, err := parseConfig(projectConfig)
	if err != nil {
		return nil, err
	}
	// The identical path on both sides: git's gitdir line and the repository's
	// backlink are both absolute host paths (invariant 11).
	if err := mergeMounts(doc,
		ownMount{target: repo, entry: fmt.Sprintf("source=%s,target=%s,type=bind", repo, repo)},
		ownMount{target: path, entry: fmt.Sprintf("source=%s,target=%s,type=bind", path, path)},
	); err != nil {
		return nil, err
	}
	return marshalConfig(doc)
}

// OverlayIsolatedWorkspace points a project's devcontainer.json at a volume for
// its workspace, for an isolated container. Both workspaceMount and
// workspaceFolder are replaced, so the CLI does not derive the in-container
// path from the host folder.
func OverlayIsolatedWorkspace(projectConfig []byte, mount Mount) ([]byte, error) {
	doc, err := parseConfig(projectConfig)
	if err != nil {
		return nil, err
	}
	for key, value := range map[string]string{
		"workspaceMount":  fmt.Sprintf("source=%s,target=%s,type=volume", mount.Volume, mount.Folder),
		"workspaceFolder": mount.Folder,
	} {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("rendering %s: %w", key, err)
		}
		doc[key] = raw
	}
	return marshalConfig(doc)
}

// OverlayMounts merges mounts entries dev owns into a project's
// devcontainer.json, in string form. A project mount at the same target is
// replaced.
func OverlayMounts(projectConfig []byte, entries []string) ([]byte, error) {
	doc, err := parseConfig(projectConfig)
	if err != nil {
		return nil, err
	}
	if err := mergeMounts(doc, ownMounts(entries)...); err != nil {
		return nil, err
	}
	return marshalConfig(doc)
}

// AppendMounts adds mounts entries to a generated document. They are added per
// invocation, because the git guard's mounts depend on where the project's .git
// is today.
func AppendMounts(generated []byte, entries []string) ([]byte, error) {
	return OverlayMounts(generated, entries)
}

// NamesWorkspaceMount reports whether a project's document sets its workspace
// location, which replaces the CLI's default layout, so dev cannot know where
// .git lands.
func NamesWorkspaceMount(projectConfig []byte) (bool, error) {
	doc, err := parseConfig(projectConfig)
	if err != nil {
		return false, err
	}
	_, mount := doc["workspaceMount"]
	_, folder := doc["workspaceFolder"]
	return mount || folder, nil
}

func ownMounts(entries []string) []ownMount {
	out := make([]ownMount, 0, len(entries))
	for _, e := range entries {
		target, _ := mountTarget(json.RawMessage(strconvQuote(e)))
		out = append(out, ownMount{target: target, entry: e})
	}
	return out
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ownMount is one mounts entry dev owns for one invocation.
type ownMount struct {
	target string
	entry  string
}

// mergeMounts replaces any project mount whose target matches one of own's, and
// appends the rest, so the result is deterministic.
func mergeMounts(doc map[string]json.RawMessage, own ...ownMount) error {
	var existing []json.RawMessage
	if raw, ok := doc["mounts"]; ok {
		if err := json.Unmarshal(raw, &existing); err != nil {
			return fmt.Errorf("reading the project's mounts: %w", err)
		}
	}

	ownTargets := make(map[string]bool, len(own))
	for _, m := range own {
		ownTargets[m.target] = true
	}

	kept := make([]json.RawMessage, 0, len(existing)+len(own))
	for _, entry := range existing {
		target, err := mountTarget(entry)
		if err != nil {
			return err
		}
		if ownTargets[target] {
			continue // replaced below
		}
		kept = append(kept, entry)
	}
	for _, m := range own {
		raw, err := json.Marshal(m.entry)
		if err != nil {
			return fmt.Errorf("rendering the mount at %s: %w", m.target, err)
		}
		kept = append(kept, raw)
	}

	mounts, err := json.Marshal(kept)
	if err != nil {
		return fmt.Errorf("rendering the merged mounts: %w", err)
	}
	doc["mounts"] = mounts
	return nil
}

// marshalConfig renders a merged document back to JSON, indented for a human
// reading it.
func marshalConfig(doc map[string]json.RawMessage) ([]byte, error) {
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("rendering the merged configuration: %w", err)
	}
	return out, nil
}

// UsesCompose reports whether a project's document names a compose file. A
// mounts entry is not reliably applied for a compose project
// (devcontainers/spec#106), so the caller warns.
func UsesCompose(projectConfig []byte) (bool, error) {
	doc, err := parseConfig(projectConfig)
	if err != nil {
		return false, err
	}
	_, ok := doc["dockerComposeFile"]
	return ok, nil
}

// parseConfig decodes a devcontainer.json into its top-level fields. It is
// JSONC: comments and trailing commas are legal, and hujson.Standardize handles
// them without touching string literals.
func parseConfig(projectConfig []byte) (map[string]json.RawMessage, error) {
	std, err := hujson.Standardize(projectConfig)
	if err != nil {
		return nil, fmt.Errorf("reading the project's devcontainer.json: %w", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(std, &doc); err != nil {
		return nil, fmt.Errorf("reading the project's devcontainer.json: %w", err)
	}
	// A top-level null unmarshals into a nil map without error, which would
	// panic in Overlay.
	if doc == nil {
		return nil, fmt.Errorf("reading the project's devcontainer.json: expected an object, got null")
	}
	return doc, nil
}

// mountTarget returns where a mounts entry lands inside the container, in
// either spelling.
func mountTarget(entry json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(entry, &s); err == nil {
		for _, field := range strings.Split(s, ",") {
			// docker's --mount also accepts destination= and dst= for the same
			// thing.
			for _, key := range []string{"target=", "destination=", "dst="} {
				if strings.HasPrefix(field, key) {
					return strings.TrimPrefix(field, key), nil
				}
			}
		}
		return "", nil // a mount with no target is the CLI's problem, not dev's
	}

	var obj struct {
		Target      string `json:"target"`
		Destination string `json:"destination"`
	}
	if err := json.Unmarshal(entry, &obj); err != nil {
		return "", fmt.Errorf("reading a mounts entry: %w", err)
	}
	if obj.Target != "" {
		return obj.Target, nil
	}
	return obj.Destination, nil
}
