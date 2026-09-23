package dcgen

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tailscale/hujson"
)

// Overlay merges dev's state mount into a project's own devcontainer.json.
//
// The devcontainer CLI's --override-config *replaces* the document rather than
// deep-merging it, so everything the project declared has to come back out
// again. That is why the document is decoded into json.RawMessage values:
// every field dev does not touch round-trips byte for byte, and dev stays out
// of the business of understanding a spec it does not implement.
//
// Exactly one field is rewritten. Environment already reaches both up and exec
// through --remote-env, so merging containerEnv would be a second route to a
// destination that already has one — and two mechanisms for one outcome is how
// a mount gets dropped.
//
// dev owns StateDir whenever dev is driving: an existing mount at that target is
// replaced, not kept beside. Leaving both would make docker refuse the whole run
// with "duplicate mount destination", and standing down instead would leave
// `container remove` unable to delete a volume dev did not name.
func Overlay(projectConfig []byte, state State) ([]byte, error) {
	doc, err := parseConfig(projectConfig)
	if err != nil {
		return nil, err
	}
	// The string spelling, matching what Render writes, so the two places dev
	// names this volume name it the same way.
	own := ownMount{
		target: StateDir,
		entry:  fmt.Sprintf("source=%s,target=%s,type=volume", state.Volume, StateDir),
	}
	if err := mergeMounts(doc, own); err != nil {
		return nil, err
	}
	return marshalConfig(doc)
}

// OverlayWorktree merges a worktree's two bind mounts into a project's own
// devcontainer.json: git's common directory and the checkout, each at its own
// host path.
//
// A project-owned container cannot get these from a generated document, since
// dev never writes into a project's own devcontainer.json (invariant 9), so
// they arrive the same way the state mount does — merged in at invocation
// time and passed through --override-config. Both mounts are required for git
// to work at all inside the container: see CLAUDE.md invariant 11.
//
// dev owns both targets whenever dev is driving, for the reason Overlay owns
// StateDir: an existing entry at either path is replaced, not kept beside,
// since docker refuses the whole run with "duplicate mount destination" when a
// target arrives twice.
func OverlayWorktree(projectConfig []byte, repo, path string) ([]byte, error) {
	doc, err := parseConfig(projectConfig)
	if err != nil {
		return nil, err
	}
	// The identical path on both sides of each mount — see gitwt and invariant
	// 11 for why. Repo resolves the checkout's gitdir: line; path satisfies the
	// repository's own backlink to the checkout.
	if err := mergeMounts(doc,
		ownMount{target: repo, entry: fmt.Sprintf("source=%s,target=%s,type=bind", repo, repo)},
		ownMount{target: path, entry: fmt.Sprintf("source=%s,target=%s,type=bind", path, path)},
	); err != nil {
		return nil, err
	}
	return marshalConfig(doc)
}

// ownMount is one mounts entry dev owns for the length of one invocation:
// where it lands in the container, and the full devcontainer.json spelling.
type ownMount struct {
	target string
	entry  string
}

// mergeMounts replaces any project mount whose target matches one of own's,
// and appends the rest of own after the project's surviving entries — kept in
// the project's own order, with dev's appended last, so the result is
// deterministic and a diff between two invocations of the same document is
// empty.
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

// marshalConfig renders a merged document back to JSON.
//
// Indented because this file is what an operator is pointed at when a
// container starts wrong, and two spaces cost nothing in a temporary file.
func marshalConfig(doc map[string]json.RawMessage) ([]byte, error) {
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("rendering the merged configuration: %w", err)
	}
	return out, nil
}

// UsesCompose reports whether a project's document names a compose file.
//
// A `mounts` entry is not reliably applied for a compose project: the spec
// lists the property as cross-orchestrator, but mounts there belong in the
// compose file and implementations differ on whether they inject it
// (devcontainers/spec#106). The container still runs; its state volume is
// simply absent, with nothing reporting an error. The caller warns.
func UsesCompose(projectConfig []byte) (bool, error) {
	doc, err := parseConfig(projectConfig)
	if err != nil {
		return false, err
	}
	_, ok := doc["dockerComposeFile"]
	return ok, nil
}

// parseConfig decodes a devcontainer.json into its top-level fields.
//
// devcontainer.json is JSONC: comments and trailing commas are legal, the CLI
// accepts them and VS Code's own templates ship them, while encoding/json
// rejects both. hujson.Standardize normalises the syntax and nothing else — it
// is correct on a // or /* inside a string literal, where a hand-rolled
// stripper would silently corrupt the line.
func parseConfig(projectConfig []byte) (map[string]json.RawMessage, error) {
	std, err := hujson.Standardize(projectConfig)
	if err != nil {
		return nil, fmt.Errorf("reading the project's devcontainer.json: %w", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(std, &doc); err != nil {
		return nil, fmt.Errorf("reading the project's devcontainer.json: %w", err)
	}
	// json.Unmarshal unmarshals null into a nil map with err == nil. A top-level
	// null is not a valid devcontainer document; reject it before returning,
	// since a nil map causes a panic in Overlay when it tries to assign to it.
	if doc == nil {
		return nil, fmt.Errorf("reading the project's devcontainer.json: expected an object, got null")
	}
	return doc, nil
}

// mountTarget returns where a mounts entry lands inside the container.
//
// Both spellings, because the CLI accepts both: the string form
// "source=x,target=/y,type=volume" and the object form
// {"source":"x","target":"/y"}. Reading only one would leave an object-form
// project with dev's mount added beside its own rather than replacing it, which
// is the duplicate-destination failure again.
func mountTarget(entry json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(entry, &s); err == nil {
		for _, field := range strings.Split(s, ",") {
			// target= is the documented spelling; docker's own --mount also
			// accepts destination= and dst= for the same thing.
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
