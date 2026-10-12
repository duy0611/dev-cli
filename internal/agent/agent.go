// Package agent knows which coding agents a container can run and how to invoke
// them.
package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/duy0611/dev-cli/internal/agentcfg"
)

// Agent describes one coding agent.
type Agent struct {
	// ID is what --agent takes.
	ID string
	// Binary is the executable to look for and run inside the container.
	Binary string
	// Args are prepended to whatever the operator passes after --.
	Args []string
	// Configure returns the steps that apply this agent's section of an
	// agents.yaml. Nil when the agent cannot be configured.
	Configure func(v agentcfg.View) ([]Step, error)
}

// Command renders the argv to run inside the container.
func (a Agent) Command(extra []string) []string {
	cmd := make([]string, 0, 1+len(a.Args)+len(extra))
	cmd = append(cmd, a.Binary)
	cmd = append(cmd, a.Args...)
	return append(cmd, extra...)
}

// registry is the set of agents this tool can start.
var registry = map[string]Agent{
	"claude":   {ID: "claude", Binary: "claude", Configure: configureClaude},
	"opencode": {ID: "opencode", Binary: "opencode", Configure: configureOpencode},
	"codex":    {ID: "codex", Binary: "codex"},
	"hermes":   {ID: "hermes", Binary: "hermes", Configure: configureHermes},
}

// Lookup returns the agent with the given id.
func Lookup(id string) (Agent, error) {
	a, ok := registry[strings.ToLower(strings.TrimSpace(id))]
	if !ok {
		return Agent{}, fmt.Errorf("unknown agent %q (one of: %s)", id, strings.Join(IDs(), ", "))
	}
	return a, nil
}

// IDs lists the known agents, sorted, for help text and error messages.
func IDs() []string {
	out := make([]string, 0, len(registry))
	for id := range registry {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Configurable lists the agents an agents.yaml can configure, in sorted order.
func Configurable() []Agent {
	var out []Agent
	for _, id := range IDs() {
		if a := registry[id]; a.Configure != nil {
			out = append(out, a)
		}
	}
	return out
}
