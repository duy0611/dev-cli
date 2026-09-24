package agent

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/agentcfg"
	"go.yaml.in/yaml/v3"
)

var testServers = map[string]agentcfg.MCPServer{
	"ctx": {Command: "npx", Args: []string{"-y", "ctx", "--key=${K}"}, Env: map[string]string{"K": "${K}", "H": "$HOME"}},
	"web": {URL: "https://x/${REGION}/mcp", Headers: map[string]string{"Authorization": "Bearer ${T}"}},
}

func TestMergeOpencode(t *testing.T) {
	current := []byte(`{"theme": "dark", "plugin": ["mine"], "mcp": {"hand": {"type": "local", "command": ["x"]}}}`)
	out, err := mergeOpencode(current, []string{"opencode-wakatime", "mine"}, testServers)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Theme  string                    `json:"theme"`
		Plugin []string                  `json:"plugin"`
		MCP    map[string]map[string]any `json:"mcp"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if doc.Theme != "dark" {
		t.Error("an unowned key did not survive")
	}
	if strings.Join(doc.Plugin, ",") != "mine,opencode-wakatime" {
		t.Errorf("plugin = %v, want the existing one kept and the new one added once", doc.Plugin)
	}
	if doc.MCP["hand"] == nil {
		t.Error("a server added by hand was removed")
	}

	ctx := doc.MCP["ctx"]
	cmd, _ := json.Marshal(ctx["command"])
	if ctx["type"] != "local" || string(cmd) != `["npx","-y","ctx","--key={env:K}"]` {
		t.Errorf("ctx = %v", ctx)
	}
	env := ctx["environment"].(map[string]any)
	if env["K"] != "{env:K}" || env["H"] != "$HOME" {
		t.Errorf("environment = %v", env)
	}

	web := doc.MCP["web"]
	if web["type"] != "remote" || web["url"] != "https://x/{env:REGION}/mcp" ||
		web["headers"].(map[string]any)["Authorization"] != "Bearer {env:T}" {
		t.Errorf("web = %v", web)
	}
}

func TestMergeOpencodeAcceptsJSONC(t *testing.T) {
	current := []byte("{\n  // mine\n  \"theme\": \"dark\",\n}\n")
	out, err := mergeOpencode(current, []string{"p"}, nil)
	if err != nil {
		t.Fatalf("JSONC refused: %v", err)
	}
	if !bytes.Contains(out, []byte(`"theme": "dark"`)) {
		t.Errorf("theme lost:\n%s", out)
	}
}

func TestMergeOpencodeFromNothing(t *testing.T) {
	out, err := mergeOpencode(nil, []string{"p"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"plugin"`)) {
		t.Errorf("out = %s", out)
	}
}

func TestMergeOpencodeRejectsAWrongShape(t *testing.T) {
	for _, current := range []string{`[1]`, `{"plugin": "x"}`, `{"mcp": []}`, `{`} {
		if _, err := mergeOpencode([]byte(current), []string{"p"}, testServers); err == nil {
			t.Errorf("accepted %s", current)
		}
	}
}

func TestMergeHermesKeepsCommentsAndOtherKeys(t *testing.T) {
	current := []byte("# my settings\nmodel: hermes-4\nmcp_servers:\n  hand:\n    command: x  # keep me\n")
	out, err := mergeHermes(current, testServers)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# my settings", "model: hermes-4", "# keep me", "hand:"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("%q lost:\n%s", want, out)
		}
	}
	var doc struct {
		MCP map[string]struct {
			Command string            `yaml:"command"`
			Args    []string          `yaml:"args"`
			Env     map[string]string `yaml:"env"`
			URL     string            `yaml:"url"`
			Headers map[string]string `yaml:"headers"`
		} `yaml:"mcp_servers"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	// hermes expands ${NAME} itself when it loads config.yaml.
	if doc.MCP["ctx"].Env["K"] != "${K}" || doc.MCP["web"].Headers["Authorization"] != "Bearer ${T}" {
		t.Errorf("mcp_servers = %+v", doc.MCP)
	}
}

func TestMergeHermesFromNothingAndFromNull(t *testing.T) {
	for _, current := range []string{"", "mcp_servers:\n"} {
		out, err := mergeHermes([]byte(current), testServers)
		if err != nil {
			t.Fatalf("%q: %v", current, err)
		}
		if !bytes.Contains(out, []byte("ctx:")) {
			t.Errorf("%q: out = %s", current, out)
		}
	}
	if _, err := mergeHermes([]byte("mcp_servers: [1]\n"), testServers); err == nil {
		t.Error("accepted a list where a mapping belongs")
	}
}

// A rebuild re-applies over a volume that already holds the last apply.
func TestMergesAreIdempotent(t *testing.T) {
	once, err := mergeOpencode([]byte(`{"plugin":["a"]}`), []string{"a", "b"}, testServers)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := mergeOpencode(once, []string{"a", "b"}, testServers)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(once, twice) {
		t.Errorf("opencode drifted:\n%s\n---\n%s", once, twice)
	}

	h1, err := mergeHermes([]byte("model: x\n"), testServers)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := mergeHermes(h1, testServers)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(h1, h2) {
		t.Errorf("hermes drifted:\n%s\n---\n%s", h1, h2)
	}
}

func TestOpencodeAndHermesSteps(t *testing.T) {
	v := agentcfg.View{Plugins: []string{"p"}, MCP: testServers,
		Skills: []agentcfg.Skill{{Name: "s", Archive: []byte("t")}}}

	o, err := configureOpencode(v)
	if err != nil {
		t.Fatal(err)
	}
	if len(o) != 2 || o[0].File != "${OPENCODE_CONFIG_DIR:-$HOME/.config/opencode}/opencode.json" ||
		!strings.Contains(o[1].Cmd[2], "${OPENCODE_CONFIG_DIR:-$HOME/.config/opencode}/skills/s") {
		t.Errorf("opencode steps = %+v", o)
	}

	h, err := configureHermes(v)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 || h[0].File != "${HERMES_HOME:-$HOME/.hermes}/config.yaml" ||
		!strings.Contains(h[1].Cmd[2], "${HERMES_HOME:-$HOME/.hermes}/skills/s") {
		t.Errorf("hermes steps = %+v", h)
	}

	// Nothing to merge means no file is touched at all.
	o, _ = configureOpencode(agentcfg.View{})
	h, _ = configureHermes(agentcfg.View{})
	if len(o) != 0 || len(h) != 0 {
		t.Errorf("empty views produced steps: %+v %+v", o, h)
	}
}

// JSON null is valid where an object belongs; json.Unmarshal leaves the map
// nil and a write to it panics, which would exit 2 with a stack trace.
func TestMergeOpencodeTreatsNullAsEmpty(t *testing.T) {
	for _, current := range []string{`null`, `{"mcp": null}`, `{"plugin": null}`} {
		out, err := mergeOpencode([]byte(current), []string{"p"}, testServers)
		if err != nil {
			t.Errorf("%s: %v", current, err)
			continue
		}
		if !bytes.Contains(out, []byte(`"ctx"`)) || !bytes.Contains(out, []byte(`"p"`)) {
			t.Errorf("%s: out = %s", current, out)
		}
	}
}
