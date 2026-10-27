package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/duy0611/dev-cli/internal/agentcfg"
	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/tailscale/hujson"
)

// opencodeDir is opencode's configuration directory.
const opencodeDir = "${OPENCODE_CONFIG_DIR:-$HOME/.config/opencode}"

// configureOpencode writes opencode.json, where opencode reads plugins and MCP
// servers. A declared config_file becomes the whole document; without one the
// file already in the container is the base and declared entries are added.
func configureOpencode(v agentcfg.View) ([]Step, error) {
	var steps []Step
	if len(v.Plugins) > 0 || len(v.MCP) > 0 || len(v.Config) > 0 {
		steps = append(steps, Step{
			Desc: "opencode: config, plugins and mcp in opencode.json",
			File: opencodeDir + "/opencode.json",
			Edit: func(cur []byte) ([]byte, error) { return mergeOpencode(cur, v.Config, v.Plugins, v.MCP) },
		})
	}
	return append(steps, skillSteps("opencode", opencodeDir+"/skills", v.Skills)...), nil
}

// opencodeLead points opencode's merged config at the instruction file, which
// lives outside the working tree. The interactive command has no --model flag,
// so the model travels in the same config rather than argv.
func opencodeLead(in LeadInput) ([]string, []provider.EnvVar, error) {
	config, err := json.Marshal(struct {
		Instructions []string `json:"instructions"`
		Model        string   `json:"model,omitempty"`
	}{Instructions: []string{in.InstructionFile}, Model: in.Model})
	if err != nil {
		return nil, nil, fmt.Errorf("encoding opencode's lead config: %w", err)
	}
	// Without --standalone the background service ignores this env var.
	return []string{"--standalone"}, []provider.EnvVar{{
		Key: "OPENCODE_CONFIG_CONTENT", Value: string(config),
	}}, nil
}

// pluginNames extracts the package each entry names, for dedupe. An entry in a
// shape that names none is left alone rather than rejected.
func pluginNames(entries []json.RawMessage) []string {
	var names []string
	for _, e := range entries {
		var s string
		if json.Unmarshal(e, &s) == nil {
			names = append(names, s)
			continue
		}
		var obj struct {
			Package string `json:"package"`
		}
		if json.Unmarshal(e, &obj) == nil && obj.Package != "" {
			names = append(names, obj.Package)
			continue
		}
		var tuple []json.RawMessage
		if json.Unmarshal(e, &tuple) == nil && len(tuple) == 2 {
			if json.Unmarshal(tuple[0], &s) == nil {
				names = append(names, s)
			}
		}
	}
	return names
}

// mergeOpencode builds the document written to opencode.json. When base is
// non-nil it is the whole document: a config_file is authoritative, so a key
// removed from it is gone after the next apply, and the container's copy is
// replaced rather than read. Without one, the existing file is the base and
// unowned keys round-trip.
func mergeOpencode(current, base []byte, plugins []string, servers map[string]agentcfg.MCPServer) ([]byte, error) {
	doc := map[string]json.RawMessage{}
	src := current
	if base != nil {
		src = base
	}
	if len(bytes.TrimSpace(src)) > 0 {
		// opencode reads JSONC; comments and trailing commas do not survive
		// the rewrite. Cloned because Standardize works in place.
		std, err := hujson.Standardize(bytes.Clone(src))
		if err != nil {
			return nil, fmt.Errorf("opencode.json does not parse: %w", err)
		}
		if err := json.Unmarshal(std, &doc); err != nil {
			return nil, fmt.Errorf("opencode.json is not an object: %w", err)
		}
		// null leaves the map nil, which would panic on write.
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
	}

	if len(plugins) > 0 {
		// A fresh file gets V2's plural key; a file already using one keeps it,
		// so an image still on opencode 1 sees the key it reads.
		key := "plugins"
		if _, ok := doc["plugins"]; !ok {
			if _, ok := doc["plugin"]; ok {
				key = "plugin"
			}
		}
		// Entries may be strings, V2 {package, options} objects, or legacy
		// [package, options] pairs; only strings are ours to add.
		var entries []json.RawMessage
		if raw, ok := doc[key]; ok {
			if err := json.Unmarshal(raw, &entries); err != nil {
				return nil, fmt.Errorf("opencode.json: %s is not a list: %w", key, err)
			}
			if entries == nil {
				entries = []json.RawMessage{}
			}
		}
		have := pluginNames(entries)
		if key == "plugins" {
			// A legacy array may sit beside it; do not add the same package
			// twice across the two keys.
			if raw, ok := doc["plugin"]; ok {
				var legacy []json.RawMessage
				if err := json.Unmarshal(raw, &legacy); err != nil {
					return nil, fmt.Errorf("opencode.json: plugin is not a list: %w", err)
				}
				have = append(have, pluginNames(legacy)...)
			}
		}
		for _, p := range plugins {
			if slices.Contains(have, p) {
				continue
			}
			b, err := json.Marshal(p)
			if err != nil {
				return nil, err
			}
			entries = append(entries, b)
			have = append(have, p)
		}
		raw, err := json.Marshal(entries)
		if err != nil {
			return nil, err
		}
		doc[key] = raw
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
		// V2 keeps servers under mcp.servers and may also hold mcp.timeout;
		// anything else is a legacy direct server.
		v2 := true
		for k := range mcp {
			if k != "servers" && k != "timeout" {
				v2 = false
				break
			}
		}
		target := mcp
		if v2 {
			target = map[string]json.RawMessage{}
			if raw, ok := mcp["servers"]; ok {
				if err := json.Unmarshal(raw, &target); err != nil {
					return nil, fmt.Errorf("opencode.json: mcp.servers is not an object: %w", err)
				}
				if target == nil {
					target = map[string]json.RawMessage{}
				}
			}
		}
		for name, m := range servers {
			raw, err := json.Marshal(opencodeMCP(m))
			if err != nil {
				return nil, err
			}
			target[name] = raw
		}
		if v2 {
			raw, err := json.Marshal(target)
			if err != nil {
				return nil, err
			}
			mcp["servers"] = raw
		}
		raw, err := json.Marshal(mcp)
		if err != nil {
			return nil, err
		}
		doc["mcp"] = raw
	}

	// Sorted keys make a second apply write identical bytes.
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// opencodeMCP renders one server in opencode's shape, rewriting ${NAME} to
// {env:NAME}. The command is not rewritten: references are recognised only in
// env, headers, args and url.
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
