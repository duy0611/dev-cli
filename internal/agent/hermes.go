package agent

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/duy0611/dev-cli/internal/agentcfg"
	"go.yaml.in/yaml/v3"
)

// hermesDir is hermes's configuration directory.
const hermesDir = "${HERMES_HOME:-$HOME/.hermes}"

// configureHermes merges MCP servers into config.yaml, since hermes has no CLI
// for them.
func configureHermes(v agentcfg.View) ([]Step, error) {
	var steps []Step
	if len(v.MCP) > 0 {
		steps = append(steps, Step{
			Desc: "hermes: mcp_servers in config.yaml",
			File: hermesDir + "/config.yaml",
			Edit: func(cur []byte) ([]byte, error) { return mergeHermes(cur, v.MCP) },
		})
	}
	return append(steps, skillSteps("hermes", hermesDir+"/skills", v.Skills)...), nil
}

// hermesServer is agentcfg.MCPServer with omitempty, so a local server gets no
// empty url: and a remote one no empty command:.
type hermesServer struct {
	Command string            `yaml:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
	URL     string            `yaml:"url,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
}

// mergeHermes sets declared servers under mcp_servers by name. It edits the
// parsed node tree so the operator's comments and key order survive. ${NAME}
// passes through for hermes to expand.
func mergeHermes(current []byte, servers map[string]agentcfg.MCPServer) ([]byte, error) {
	var doc yaml.Node
	if len(bytes.TrimSpace(current)) > 0 {
		if err := yaml.Unmarshal(current, &doc); err != nil {
			return nil, fmt.Errorf("config.yaml does not parse: %w", err)
		}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("config.yaml is not a mapping")
	}
	mcp, err := mappingUnder(root, "mcp_servers")
	if err != nil {
		return nil, err
	}
	for _, name := range sortedKeys(servers) {
		var val yaml.Node
		if err := val.Encode(hermesServer(servers[name])); err != nil {
			return nil, err
		}
		setKey(mcp, name, &val)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// mappingUnder returns the mapping at key in m, creating it when absent. A key
// with no value parses as null, which is replaced with an empty mapping.
func mappingUnder(m *yaml.Node, key string) (*yaml.Node, error) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != key {
			continue
		}
		v := m.Content[i+1]
		if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
			*v = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		}
		if v.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("config.yaml: %s is not a mapping", key)
		}
		return v, nil
	}
	v := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
	return v, nil
}

// setKey replaces key's value in mapping m, or appends the pair.
func setKey(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val)
}
