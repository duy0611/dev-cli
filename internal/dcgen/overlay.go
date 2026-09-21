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

	var existing []json.RawMessage
	if raw, ok := doc["mounts"]; ok {
		if err := json.Unmarshal(raw, &existing); err != nil {
			return nil, fmt.Errorf("reading the project's mounts: %w", err)
		}
	}

	// Kept in the project's own order, with dev's appended last, so the result
	// is deterministic and a diff between two invocations is empty.
	kept := make([]json.RawMessage, 0, len(existing)+1)
	for _, entry := range existing {
		target, err := mountTarget(entry)
		if err != nil {
			return nil, err
		}
		if target == StateDir {
			continue // replaced below
		}
		kept = append(kept, entry)
	}

	// The string spelling, matching what Render writes, so the two places dev
	// names this volume name it the same way.
	own, err := json.Marshal(fmt.Sprintf("source=%s,target=%s,type=volume", state.Volume, StateDir))
	if err != nil {
		return nil, fmt.Errorf("rendering the state mount: %w", err)
	}
	kept = append(kept, own)

	mounts, err := json.Marshal(kept)
	if err != nil {
		return nil, fmt.Errorf("rendering the merged mounts: %w", err)
	}
	doc["mounts"] = mounts

	// Indented because this file is what an operator is pointed at when a
	// container starts wrong, and two spaces cost nothing in a temporary file.
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
