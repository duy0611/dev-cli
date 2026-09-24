package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/agentcfg"
)

func TestClaudeSteps(t *testing.T) {
	v := agentcfg.View{
		Marketplaces: map[string]string{"official": "anthropics/claude-plugins-official"},
		Plugins:      []string{"superpowers@official"},
		MCP: map[string]agentcfg.MCPServer{
			"ctx": {Command: "npx", Args: []string{"-y", "ctx"}, Env: map[string]string{"K": "${K}"}},
			"web": {URL: "https://x/mcp", Headers: map[string]string{"Authorization": "Bearer ${T}"}},
		},
		Skills: []agentcfg.Skill{{Name: "ours", Archive: []byte("t")}},
	}
	steps, err := configureClaude(v)
	if err != nil {
		t.Fatal(err)
	}
	var descs []string
	for _, s := range steps {
		descs = append(descs, s.Desc)
	}
	want := []string{
		"claude: marketplace official",
		"claude: plugin superpowers@official",
		"claude: mcp ctx (clear any old definition)",
		"claude: mcp ctx",
		"claude: mcp web (clear any old definition)",
		"claude: mcp web",
		"claude: skill ours",
	}
	if !slices.Equal(descs, want) {
		t.Fatalf("steps =\n%s\nwant\n%s", strings.Join(descs, "\n"), strings.Join(want, "\n"))
	}

	mkt := steps[0]
	if !slices.Equal(mkt.Cmd, []string{"claude", "plugin", "marketplace", "add", "anthropics/claude-plugins-official"}) {
		t.Errorf("marketplace cmd = %v", mkt.Cmd)
	}
	listed := `[{"name":"official","source":"github"}]`
	if !mkt.Done([]byte(listed)) || mkt.Done([]byte(`[]`)) || mkt.Done([]byte(`not json`)) {
		t.Error("marketplace Done misreads the list")
	}

	if !slices.Equal(steps[1].Cmd, []string{"claude", "plugin", "install", "--scope", "user", "superpowers@official"}) {
		t.Errorf("install cmd = %v", steps[1].Cmd)
	}

	// Removal is allowed to fail — there is nothing to remove the first time.
	if !strings.Contains(steps[2].Cmd[2], "|| true") || steps[2].Cmd[4] != "ctx" {
		t.Errorf("remove cmd = %v", steps[2].Cmd)
	}

	add := steps[3].Cmd
	if !slices.Equal(add[:6], []string{"claude", "mcp", "add-json", "--scope", "user", "ctx"}) {
		t.Errorf("add cmd = %v", add)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(add[6]), &doc); err != nil {
		t.Fatal(err)
	}
	// References pass through: Claude resolves ${K} itself, from the
	// environment dev launches it with.
	if doc["type"] != "stdio" || doc["command"] != "npx" || doc["env"].(map[string]any)["K"] != "${K}" {
		t.Errorf("stdio doc = %v", doc)
	}

	if err := json.Unmarshal([]byte(steps[5].Cmd[6]), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["type"] != "http" || doc["url"] != "https://x/mcp" {
		t.Errorf("http doc = %v", doc)
	}
}
