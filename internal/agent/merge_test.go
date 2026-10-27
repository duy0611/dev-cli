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
	out, err := mergeOpencode(current, nil, []string{"opencode-wakatime", "mine"}, testServers)
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
	out, err := mergeOpencode(current, nil, []string{"p"}, nil)
	if err != nil {
		t.Fatalf("JSONC refused: %v", err)
	}
	if !bytes.Contains(out, []byte(`"theme": "dark"`)) {
		t.Errorf("theme lost:\n%s", out)
	}
}

// A config_file is authoritative: it replaces the container's document, so a
// key deleted from the file is gone after the next apply.
func TestMergeOpencodeAuthoritative(t *testing.T) {
	current := []byte(`{"theme":"dark","model":"old","plugin":["mine"],"mcp":{"hand":{"type":"local","command":["x"]}}}`)
	base := []byte(`{"model":"new","providers":{"litellm":{"models":{"a":{}}}}}`)
	out, err := mergeOpencode(current, base, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	for _, gone := range []string{"theme", "plugin", "mcp"} {
		if _, ok := doc[gone]; ok {
			t.Errorf("%s survived an authoritative apply:\n%s", gone, out)
		}
	}
	if string(doc["model"]) != `"new"` {
		t.Errorf("model = %s", doc["model"])
	}
	if _, ok := doc["providers"]; !ok {
		t.Errorf("providers lost:\n%s", out)
	}
}

// The container's copy is replaced, not parsed, so a corrupt one is no
// obstacle.
func TestMergeOpencodeAuthoritativeIgnoresABrokenCurrent(t *testing.T) {
	out, err := mergeOpencode([]byte("{"), []byte(`{"model":"m"}`), nil, nil)
	if err != nil {
		t.Fatalf("a broken current file was read: %v", err)
	}
	if !bytes.Contains(out, []byte(`"model": "m"`)) {
		t.Errorf("out = %s", out)
	}
}

// Fresh files get the V2 key; a file already using a key keeps it.
func TestMergeOpencodePluginShape(t *testing.T) {
	cases := []struct {
		name       string
		base       []byte
		wantKey    string
		notWantKey string
	}{
		{"fresh", nil, "plugins", "plugin"},
		{"v2", []byte(`{"plugins":["a"]}`), "plugins", "plugin"},
		{"legacy", []byte(`{"plugin":["a"]}`), "plugin", "plugins"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := mergeOpencode(nil, c.base, []string{"a", "b"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(out, &doc); err != nil {
				t.Fatalf("output is not JSON: %v\n%s", err, out)
			}
			raw, ok := doc[c.wantKey]
			if !ok {
				t.Fatalf("no %q key:\n%s", c.wantKey, out)
			}
			var got []string
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, ",") != "a,b" {
				t.Errorf("%s = %v, want a,b", c.wantKey, got)
			}
			if _, ok := doc[c.notWantKey]; ok {
				t.Errorf("wrote %q as well:\n%s", c.notWantKey, out)
			}
		})
	}
}

// MCP follows the shape of the document it is added to.
func TestMergeOpencodeMCPShape(t *testing.T) {
	cases := []struct {
		name        string
		base        []byte
		wantServers bool
	}{
		{"fresh", nil, true},
		{"v2", []byte(`{"mcp":{"servers":{"hand":{"type":"local","command":["x"]}}}}`), true},
		{"v2 timeout only", []byte(`{"mcp":{"timeout":{"catalog":1000}}}`), true},
		{"legacy", []byte(`{"mcp":{"hand":{"type":"local","command":["x"]}}}`), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := mergeOpencode(nil, c.base, nil, testServers)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(out, &doc); err != nil {
				t.Fatalf("output is not JSON: %v\n%s", err, out)
			}
			var mcp map[string]json.RawMessage
			if err := json.Unmarshal(doc["mcp"], &mcp); err != nil {
				t.Fatal(err)
			}
			if c.wantServers {
				if _, ok := mcp["servers"]; !ok {
					t.Fatalf("no mcp.servers:\n%s", out)
				}
				var servers map[string]json.RawMessage
				if err := json.Unmarshal(mcp["servers"], &servers); err != nil {
					t.Fatal(err)
				}
				if _, ok := servers["ctx"]; !ok {
					t.Errorf("ctx is not under mcp.servers:\n%s", out)
				}
				if _, ok := mcp["ctx"]; ok {
					t.Errorf("ctx was also written as a legacy direct server:\n%s", out)
				}
				return
			}
			if _, ok := mcp["servers"]; ok {
				t.Errorf("added mcp.servers to a legacy file:\n%s", out)
			}
			if _, ok := mcp["ctx"]; !ok {
				t.Errorf("ctx is not a legacy direct server:\n%s", out)
			}
		})
	}
}

// A V2 object entry and a legacy [package, options] pair are not ours to
// rewrite; declared strings are added beside them and deduped by package.
func TestMergeOpencodeKeepsRicherPluginEntries(t *testing.T) {
	cases := []struct {
		name string
		base string
		key  string
	}{
		{"v2 objects", `{"plugins":[{"package":"x","options":{"a":1}},"y"]}`, "plugins"},
		{"legacy tuples", `{"plugin":[["x",{"a":1}],"y"]}`, "plugin"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := mergeOpencode(nil, []byte(c.base), []string{"y", "z"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(out, &doc); err != nil {
				t.Fatalf("output is not JSON: %v\n%s", err, out)
			}
			var entries []json.RawMessage
			if err := json.Unmarshal(doc[c.key], &entries); err != nil {
				t.Fatalf("%s is not a list: %v", c.key, err)
			}
			if len(entries) != 3 {
				t.Fatalf("%s has %d entries, want the two existing plus z:\n%s", c.key, len(entries), out)
			}
			if string(entries[2]) != `"z"` {
				t.Errorf("z was not appended as a string: %s", entries[2])
			}
			if !bytes.Contains(entries[0], []byte("x")) {
				t.Errorf("the richer entry was rewritten: %s", entries[0])
			}
		})
	}
}

func TestMergeOpencodeFromNothing(t *testing.T) {
	out, err := mergeOpencode(nil, nil, []string{"p"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"plugins"`)) {
		t.Errorf("out = %s", out)
	}
}

func TestMergeOpencodeRejectsAWrongShape(t *testing.T) {
	for _, current := range []string{`[1]`, `{"plugin": "x"}`, `{"mcp": []}`, `{`} {
		if _, err := mergeOpencode([]byte(current), nil, []string{"p"}, testServers); err == nil {
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
	// ${NAME} passes through for hermes to expand.
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

// A rebuild re-applies over a file the last apply wrote.
func TestMergesAreIdempotent(t *testing.T) {
	once, err := mergeOpencode([]byte(`{"plugin":["a"]}`), nil, []string{"a", "b"}, testServers)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := mergeOpencode(once, nil, []string{"a", "b"}, testServers)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(once, twice) {
		t.Errorf("opencode drifted:\n%s\n---\n%s", once, twice)
	}

	base := []byte(`{"model":"m"}`)
	auth1, err := mergeOpencode(nil, base, []string{"a"}, testServers)
	if err != nil {
		t.Fatal(err)
	}
	auth2, err := mergeOpencode(auth1, base, []string{"a"}, testServers)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(auth1, auth2) {
		t.Errorf("an authoritative opencode apply drifted:\n%s\n---\n%s", auth1, auth2)
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

	// No declared content means no file is touched.
	o, _ = configureOpencode(agentcfg.View{})
	h, _ = configureHermes(agentcfg.View{})
	if len(o) != 0 || len(h) != 0 {
		t.Errorf("empty views produced steps: %+v %+v", o, h)
	}

	// A config document alone still writes the file.
	o, _ = configureOpencode(agentcfg.View{Config: []byte(`{"model":"m"}`)})
	if len(o) != 1 || o[0].File != "${OPENCODE_CONFIG_DIR:-$HOME/.config/opencode}/opencode.json" {
		t.Errorf("config-only steps = %+v", o)
	}
}

// JSON null must not leave the map nil, or a write to it panics.
func TestMergeOpencodeTreatsNullAsEmpty(t *testing.T) {
	for _, current := range []string{`null`, `{"mcp": null}`, `{"plugin": null}`} {
		out, err := mergeOpencode([]byte(current), nil, []string{"p"}, testServers)
		if err != nil {
			t.Errorf("%s: %v", current, err)
			continue
		}
		if !bytes.Contains(out, []byte(`"ctx"`)) || !bytes.Contains(out, []byte(`"p"`)) {
			t.Errorf("%s: out = %s", current, out)
		}
	}
}
