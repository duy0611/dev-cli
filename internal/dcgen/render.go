package dcgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Resolve validates a selection, adds anything the chosen tools require, and
// returns it sorted and deduplicated.
//
// Separate from Render because the CLI wants to tell the operator what it added
// on their behalf, which it cannot do if the addition happens inside the
// renderer.
func Resolve(toolIDs []string) ([]string, error) {
	seen := map[string]bool{}
	var add func(id string) error
	add = func(id string) error {
		if seen[id] {
			return nil
		}
		tool, ok := Lookup(id)
		if !ok {
			return fmt.Errorf("unknown tool %q", id)
		}
		seen[id] = true
		for _, req := range tool.Requires {
			if err := add(req); err != nil {
				return err
			}
		}
		return nil
	}

	for _, id := range toolIDs {
		if err := add(strings.TrimSpace(id)); err != nil {
			return nil, err
		}
	}

	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	// Sorted, because the map above has no order and the result reaches a
	// rendered document that gets compared against its stored predecessor.
	slices.Sort(out)
	return out, nil
}

// Mount says where a generated container keeps its work.
//
// The zero value means the devcontainer CLI's own default: a bind mount of the
// workspace folder, which is what a folder-backed container wants. A
// folderless one has no host directory to bind, so it names a volume instead.
type Mount struct {
	// Volume is the name of a volume to mount. Empty for a folder container.
	Volume string
	// Folder is where the volume appears inside the container.
	Folder string
}

func Render(name string, toolIDs []string, mount Mount) (string, error) {
	ids, err := Resolve(toolIDs)
	if err != nil {
		return "", err
	}

	doc := map[string]any{
		"name":  name,
		"image": BaseImage,
		// The base image's non-root user. Named explicitly so a feature that
		// installs into a home directory installs into the one the operator
		// will be sitting in.
		"remoteUser": "vscode",
	}
	if mount.Volume != "" {
		// Both, never one. workspaceMount alone leaves the CLI deriving the
		// in-container path from the host directory's basename, which for a
		// folderless container is a temporary directory with a different name
		// every invocation.
		doc["workspaceFolder"] = mount.Folder
		doc["workspaceMount"] = fmt.Sprintf("source=%s,target=%s,type=volume", mount.Volume, mount.Folder)
		// Docker creates a named volume owned by root. A bind mount gets
		// UID-remapped to the host user by the devcontainer CLI itself
		// (updateRemoteUserUID), but a volume gets no such fixup, so the
		// remote user's first write fails with permission denied. The base
		// image's vscode user already has passwordless sudo, so one chown
		// after create is enough — the volume's ownership then persists
		// across every later start.
		doc["postCreateCommand"] = fmt.Sprintf("sudo chown vscode:vscode %s", mount.Folder)
	}
	if features := featuresFor(ids); len(features) > 0 {
		doc["features"] = features
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	// encoding/json sorts map keys, which is what makes the output stable
	// enough to compare against the stored copy and to assert on in a test.
	if err := enc.Encode(doc); err != nil {
		return "", fmt.Errorf("rendering the configuration: %w", err)
	}
	return buf.String(), nil
}

// featuresFor builds the features object for an already-resolved selection.
func featuresFor(ids []string) map[string]any {
	features := map[string]any{}
	var apt []string

	for _, id := range ids {
		tool, ok := Lookup(id)
		if !ok {
			continue // Resolve already rejected these
		}
		switch {
		case tool.Apt != "":
			apt = append(apt, tool.Apt)
		case tool.Feature == kubeFeature:
			// Handled once, below: three tools share this reference and a JSON
			// object cannot hold it twice.
		default:
			opts := map[string]any{}
			for k, v := range tool.Options {
				opts[k] = v
			}
			features[tool.Feature] = opts
		}
	}

	if slices.Contains(ids, "kubectl") || slices.Contains(ids, "helm") {
		// Every tool this feature offers defaults to being installed, so the
		// ones not asked for are switched off by name. "none" is the value its
		// install script tests for.
		features[kubeFeature] = map[string]any{
			"version":  noneUnless(slices.Contains(ids, "kubectl")),
			"helm":     noneUnless(slices.Contains(ids, "helm")),
			"minikube": "none", // nothing in the catalog offers it
		}
	}

	if len(apt) > 0 {
		// A comma-separated string, not an array: that is what this feature's
		// published option schema declares.
		features[aptFeature] = map[string]any{"packages": strings.Join(apt, ",")}
	}
	return features
}

func noneUnless(wanted bool) string {
	if wanted {
		return "latest"
	}
	return "none"
}

// ToolsOf reports which catalog tools a stored configuration installs.
//
// Derived rather than stored beside the document, so that a configuration
// edited by hand is still something `rebuild --tools` can reason about. A
// feature reference the catalog does not know is ignored: it belongs to
// whoever added it, and dropping it would be destroying their edit.
func ToolsOf(config string) ([]string, error) {
	var doc struct {
		Features map[string]json.RawMessage `json:"features"`
	}
	if err := json.Unmarshal([]byte(config), &doc); err != nil {
		return nil, fmt.Errorf("reading the stored configuration: %w", err)
	}

	var out []string
	for ref, raw := range doc.Features {
		switch ref {
		case kubeFeature:
			var opts struct {
				Version string `json:"version"`
				Helm    string `json:"helm"`
			}
			// Absent means the feature's own default, which installs it.
			opts.Version, opts.Helm = "latest", "latest"
			if err := json.Unmarshal(raw, &opts); err != nil {
				return nil, fmt.Errorf("reading the stored configuration: %w", err)
			}
			if opts.Version != "none" {
				out = append(out, "kubectl")
			}
			if opts.Helm != "none" {
				out = append(out, "helm")
			}
		case aptFeature:
			var opts struct {
				Packages string `json:"packages"`
			}
			if err := json.Unmarshal(raw, &opts); err != nil {
				return nil, fmt.Errorf("reading the stored configuration: %w", err)
			}
			for _, pkg := range strings.Split(opts.Packages, ",") {
				pkg = strings.TrimSpace(pkg)
				for _, tool := range catalog {
					if tool.Apt == pkg && pkg != "" {
						out = append(out, tool.ID)
					}
				}
			}
		default:
			for _, tool := range catalog {
				if tool.Feature == ref && tool.Feature != "" {
					out = append(out, tool.ID)
				}
			}
		}
	}

	slices.Sort(out)
	return slices.Compact(out), nil
}
