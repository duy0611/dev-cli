package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/duy0611/dev-cli/internal/agentcfg"
	"github.com/tailscale/hujson"
)

// opencodeDir is opencode's configuration directory: on the state volume when
// the container persists state, its own default otherwise.
const opencodeDir = "${OPENCODE_CONFIG_DIR:-$HOME/.config/opencode}"

// configureOpencode merges into opencode.json rather than running a CLI:
// opencode has none for plugins or MCP. Listing a package under plugin is the
// whole job, since opencode installs what that list names when it starts.
func configureOpencode(v agentcfg.View) ([]Step, error) {
	var steps []Step
	if len(v.Plugins) > 0 || len(v.MCP) > 0 {
		steps = append(steps, Step{
			Desc: "opencode: plugins and mcp in opencode.json",
			File: opencodeDir + "/opencode.json",
			Edit: func(cur []byte) ([]byte, error) { return mergeOpencode(cur, v.Plugins, v.MCP) },
		})
	}
	return append(steps, skillSteps("opencode", opencodeDir+"/skills", v.Skills)...), nil
}

// mergeOpencode adds declared plugins and sets declared servers by name,
// leaving every other plugin, server and key where it was — the same per-entry
// promise `claude mcp add-json` makes. Unowned keys are held as raw JSON so
// they round-trip without dev having to understand them.
func mergeOpencode(current []byte, plugins []string, servers map[string]agentcfg.MCPServer) ([]byte, error) {
	doc := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(current)) > 0 {
		// opencode reads JSONC, so the file may carry comments and trailing
		// commas. Standardize strips them; they do not survive the rewrite,
		// which is the price of a program merging a file whose formatting it
		// does not own. Cloned because Standardize works in place.
		std, err := hujson.Standardize(bytes.Clone(current))
		if err != nil {
			return nil, fmt.Errorf("opencode.json does not parse: %w", err)
		}
		if err := json.Unmarshal(std, &doc); err != nil {
			return nil, fmt.Errorf("opencode.json is not an object: %w", err)
		}
		// null decodes without error and leaves the map nil; it means the
		// same as an empty file.
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
	}

	if len(plugins) > 0 {
		var have []string
		if raw, ok := doc["plugin"]; ok {
			if err := json.Unmarshal(raw, &have); err != nil {
				return nil, fmt.Errorf("opencode.json: plugin is not a list of strings: %w", err)
			}
		}
		for _, p := range plugins {
			if !slices.Contains(have, p) {
				have = append(have, p)
			}
		}
		raw, err := json.Marshal(have)
		if err != nil {
			return nil, err
		}
		doc["plugin"] = raw
	}

	if len(servers) > 0 {
		mcp := map[string]json.RawMessage{}
		if raw, ok := doc["mcp"]; ok {
			if err := json.Unmarshal(raw, &mcp); err != nil {
				return nil, fmt.Errorf("opencode.json: mcp is not an object: %w", err)
			}
			// "mcp": null, for the reason doc can be nil above.
			if mcp == nil {
				mcp = map[string]json.RawMessage{}
			}
		}
		for name, m := range servers {
			raw, err := json.Marshal(opencodeMCP(m))
			if err != nil {
				return nil, err
			}
			mcp[name] = raw
		}
		raw, err := json.Marshal(mcp)
		if err != nil {
			return nil, err
		}
		doc["mcp"] = raw
	}

	// A map marshals with sorted keys, which is what makes a second apply
	// write the same bytes as the first.
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// opencodeMCP renders one server in opencode's shape, with ${NAME} rewritten
// to its {env:NAME}. The command itself is not rewritten; the spec recognises
// references only in env, headers, args and url.
func opencodeMCP(m agentcfg.MCPServer) any {
	ref := func(s string) string {
		return agentcfg.RewriteRefs(s, func(n string) string { return "{env:" + n + "}" })
	}
	refs := func(in map[string]string) map[string]string {
		if len(in) == 0 {
			return nil
		}
		out := make(map[string]string, len(in))
		for k, v := range in {
			out[k] = ref(v)
		}
		return out
	}
	if m.Remote() {
		return struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers,omitempty"`
		}{"remote", ref(m.URL), refs(m.Headers)}
	}
	// opencode takes the whole argv as one list.
	cmd := []string{m.Command}
	for _, a := range m.Args {
		cmd = append(cmd, ref(a))
	}
	return struct {
		Type        string            `json:"type"`
		Command     []string          `json:"command"`
		Environment map[string]string `json:"environment,omitempty"`
	}{"local", cmd, refs(m.Env)}
}
