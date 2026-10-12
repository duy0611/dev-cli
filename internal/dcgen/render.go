package dcgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Resolve validates a selection, adds what the chosen tools require, and
// returns it sorted and deduplicated. It is separate from Render so the CLI can
// report the additions.
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
	// Sorted: the result is compared byte-for-byte against the stored copy.
	slices.Sort(out)
	return out, nil
}

// Mount says where a generated container keeps its work. The zero value is the
// CLI's default, a bind of the workspace folder.
type Mount struct {
	// Volume is the name of a volume to mount. Empty for a folder container.
	Volume string
	// Folder is where the volume appears inside the container.
	Folder string
	// Host is a host directory mounted as the workspace at its own path, for a
	// worktree checkout. Mounted elsewhere, `git gc --auto` would prune the
	// worktree. Mutually exclusive with Volume.
	Host string
	// Bind is a host directory mounted at the identical path, for git's common
	// directory of a worktree. The .git file's gitdir line is an absolute host
	// path that must resolve.
	Bind string
}

// State says whether a container keeps its agents' configuration on a volume.
// The zero value does not. StateVolume is read by the local provider only.
type State struct {
	Volume string
}

func Render(name string, toolIDs []string, mount Mount, state State) (string, error) {
	ids, err := Resolve(toolIDs)
	if err != nil {
		return "", err
	}

	doc := map[string]any{
		"name":  name,
		"image": BaseImage,
		// The base image's non-root user, so features install into the
		// operator's home.
		"remoteUser": "vscode",
	}
	// Collected, not assigned: a container needing two of either would keep
	// only the last.
	var (
		postCreate []string
		mounts     []string
	)

	switch {
	case mount.Volume != "":
		// Both, never one: workspaceMount alone makes the CLI derive the
		// in-container path from the host basename, a temporary name here.
		doc["workspaceFolder"] = mount.Folder
		doc["workspaceMount"] = fmt.Sprintf("source=%s,target=%s,type=volume", mount.Volume, mount.Folder)
		// A volume is created root-owned and gets no UID remapping. The chown
		// below makes the remote user's first write succeed.
		postCreate = append(postCreate, chown(mount.Folder))
	case mount.Host != "":
		// The same path on both sides. The repository's backlink to this
		// checkout is an absolute host path, and `git worktree prune` (run by
		// `git gc --auto`) deletes a registration whose backlink does not
		// resolve.
		doc["workspaceFolder"] = mount.Host
		doc["workspaceMount"] = fmt.Sprintf("source=%s,target=%s,type=bind", mount.Host, mount.Host)
	}

	if mount.Bind != "" {
		// The checkout's .git file points here with an absolute host path, so
		// the same path is needed.
		mounts = append(mounts, fmt.Sprintf("source=%s,target=%s,type=bind", mount.Bind, mount.Bind))
	}

	if state.Volume != "" {
		mounts = append(mounts, fmt.Sprintf("source=%s,target=%s,type=volume", state.Volume, StateDir))
		// A map: encoding/json sorts the keys, keeping the output stable.
		doc["containerEnv"] = StateEnv()
		postCreate = append(postCreate, chown(StateDir))
	}

	// Appended in a fixed order: the byte-stability test depends on it.
	if len(mounts) > 0 {
		doc["mounts"] = mounts
	}
	if len(postCreate) > 0 {
		// && so a failure stops the chain rather than being hidden.
		doc["postCreateCommand"] = strings.Join(postCreate, " && ")
	}
	if features := featuresFor(ids); len(features) > 0 {
		doc["features"] = features
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	// encoding/json sorts map keys, keeping the output stable enough to
	// compare.
	if err := enc.Encode(doc); err != nil {
		return "", fmt.Errorf("rendering the configuration: %w", err)
	}
	return buf.String(), nil
}

// featuresFor builds the features object for a resolved selection.
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
			// Handled once below: several tools share this reference and a JSON
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
		// Every tool this feature offers is installed by default, so
		// unrequested ones are switched off by name. "none" is the value its
		// install script tests for.
		features[kubeFeature] = map[string]any{
			"version":  noneUnless(slices.Contains(ids, "kubectl")),
			"helm":     noneUnless(slices.Contains(ids, "helm")),
			"minikube": "none", // nothing in the catalog offers it
		}
	}

	if len(apt) > 0 {
		// A comma-separated string, as the feature's option schema declares.
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

// ToolsOf reports which catalog tools a stored configuration installs. It is
// derived, so a hand-edited configuration can still be reasoned about. Unknown
// features are ignored.
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

// chown hands a directory to the remote user. Docker creates a named volume
// owned by root, and the CLI does not UID-remap it. The base image's vscode
// user has passwordless sudo, and ownership persists on the volume, so once
// after create is enough. A no-op on Kubernetes, where fsGroup already makes
// the volume writable.
func chown(dir string) string {
	return fmt.Sprintf("sudo chown vscode:vscode %s", dir)
}
