package agent

import (
	"encoding/json"
	"slices"

	"github.com/duy0611/dev-cli/internal/agentcfg"
)

// claudeDir is where Claude Code keeps its configuration: on the state volume
// when the container persists state, its own default otherwise.
const claudeDir = "${CLAUDE_CONFIG_DIR:-$HOME/.claude}"

// configureClaude drives Claude's own CLI. enabledPlugins in settings.json
// only enables a plugin that is already installed, so writing settings would
// declare plugins without ever fetching them.
func configureClaude(v agentcfg.View) ([]Step, error) {
	var steps []Step
	for _, name := range sortedKeys(v.Marketplaces) {
		steps = append(steps, Step{
			Desc: "claude: marketplace " + name,
			Cmd:  []string{"claude", "plugin", "marketplace", "add", v.Marketplaces[name]},
			// What `marketplace add` does with one it already knows is
			// undocumented, so it is asked first. Checked again afterwards,
			// which is what catches a key that is not the name the source
			// gives itself — otherwise every rebuild would add it again.
			Check: []string{"claude", "plugin", "marketplace", "list", "--json"},
			Done:  func(out []byte) bool { return listsMarketplace(out, name) },
		})
	}
	for _, p := range v.Plugins {
		// Safe to repeat: installing an installed plugin exits 0. No -y: a
		// plugin whose marketplace declares a command to run at install is
		// refused unattended rather than run without anyone seeing it.
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
			// add-json refuses a name that exists, so a changed definition
			// needs the old one gone first. Failure is expected the first
			// time, when there is nothing to remove.
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

// listsMarketplace reports whether the list names this marketplace. Output that
// does not parse reads as "not there", so the step runs and the check after it
// is the one that fails, with the step named.
func listsMarketplace(out []byte, name string) bool {
	var list []marketplace
	if json.Unmarshal(out, &list) != nil {
		return false
	}
	return slices.ContainsFunc(list, func(m marketplace) bool { return m.Name == name })
}

// claudeMCP renders one server as add-json takes it. ${NAME} references pass
// through unchanged: that is Claude's own syntax, resolved when it starts.
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
