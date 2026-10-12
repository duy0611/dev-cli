package agent

import (
	"encoding/json"
	"slices"

	"github.com/duy0611/dev-cli/internal/agentcfg"
)

// claudeDir is Claude Code's configuration directory.
const claudeDir = "${CLAUDE_CONFIG_DIR:-$HOME/.claude}"

// configureClaude drives Claude's own CLI. enabledPlugins in settings.json only
// enables plugins already installed, so writing settings would never fetch
// them.
func configureClaude(v agentcfg.View) ([]Step, error) {
	var steps []Step
	for _, name := range sortedKeys(v.Marketplaces) {
		steps = append(steps, Step{
			Desc: "claude: marketplace " + name,
			Cmd:  []string{"claude", "plugin", "marketplace", "add", v.Marketplaces[name]},
			// Re-adding a known marketplace is undocumented, so check first.
			// The re-check catches a key that is not the source's own name,
			// which would otherwise be re-added on every rebuild.
			Check: []string{"claude", "plugin", "marketplace", "list", "--json"},
			Done:  func(out []byte) bool { return listsMarketplace(out, name) },
		})
	}
	for _, p := range v.Plugins {
		// No -y: a plugin that declares an install command is refused
		// unattended rather than run unseen.
		steps = append(steps, Step{
			Desc: "claude: plugin " + p,
			Cmd:  []string{"claude", "plugin", "install", "--scope", "user", p},
		})
	}
	for _, name := range sortedKeys(v.MCP) {
		doc, err := claudeMCP(v.MCP[name])
		if err != nil {
			return nil, err
		}
		steps = append(steps,
			// add-json refuses an existing name. Failure here is expected when
			// there is no old definition.
			Step{
				Desc: "claude: mcp " + name + " (clear any old definition)",
				Cmd:  []string{"sh", "-c", `claude mcp remove --scope user "$1" >/dev/null 2>&1 || true`, "sh", name},
			},
			Step{
				Desc: "claude: mcp " + name,
				Cmd:  []string{"claude", "mcp", "add-json", "--scope", "user", name, string(doc)},
			},
		)
	}
	return append(steps, skillSteps("claude", claudeDir+"/skills", v.Skills)...), nil
}

// marketplace is the one field dev reads from `marketplace list --json`.
type marketplace struct {
	Name string `json:"name"`
}

// listsMarketplace reports whether the list names this marketplace. Unparseable
// output reads as absent, so the step runs and the check after it fails by
// name.
func listsMarketplace(out []byte, name string) bool {
	var list []marketplace
	if json.Unmarshal(out, &list) != nil {
		return false
	}
	return slices.ContainsFunc(list, func(m marketplace) bool { return m.Name == name })
}

// claudeMCP renders one server as add-json takes it. ${NAME} passes through for
// Claude to resolve at start.
func claudeMCP(m agentcfg.MCPServer) ([]byte, error) {
	if m.Remote() {
		return json.Marshal(struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers,omitempty"`
		}{"http", m.URL, m.Headers})
	}
	return json.Marshal(struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args,omitempty"`
		Env     map[string]string `json:"env,omitempty"`
	}{"stdio", m.Command, m.Args, m.Env})
}
