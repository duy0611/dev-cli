// Package agent knows which coding agents can be run in a container and how to
// invoke each one.
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
	// Configure turns this agent's view of an agents.yaml into the steps
	// that apply it inside a container. Nil for an agent the file has no
	// section for, which is what keeps it out of Configurable.
	Configure func(v agentcfg.View) ([]Step, error)
}

// Command renders the argv to run inside the container.
func (a Agent) Command(extra []string) []string {
	cmd := make([]string, 0, 1+len(a.Args)+len(extra))
	cmd = append(cmd, a.Binary)
	cmd = append(cmd, a.Args...)
	return append(cmd, extra...)
}

// registry is the set of agents this tool can start. Adding one is a line here;
// nothing else in the codebase names an agent.
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

// Configurable lists the agents an agents.yaml can configure, sorted, so an
// apply runs in the same order every time and its output reads the same.
func Configurable() []Agent {
	var out []Agent
	for _, id := range IDs() {
		if a := registry[id]; a.Configure != nil {
			out = append(out, a)
		}
	}
	return out
}
