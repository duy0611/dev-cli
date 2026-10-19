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
	// Prompt renders the arguments, after Binary, that open the agent with text
	// as its first message, continuing its most recent session when resume is
	// set. Nil when the agent cannot start with a message.
	Prompt func(text string, resume bool) []string
	// Resumes says Prompt honours resume.
	Resumes bool
}

// PromptCommand is the full argv that starts the agent with text as its first
// message. resumed reports whether it continues a session; an agent without
// Prompt is an error naming it.
func (a Agent) PromptCommand(text string, resume bool) (argv []string, resumed bool, err error) {
	if a.Prompt == nil {
		return nil, false, fmt.Errorf("%s cannot start with a message", a.ID)
	}
	resumed = resume && a.Resumes
	return append([]string{a.Binary}, a.Prompt(text, resumed)...), resumed, nil
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
	"claude": {
		ID: "claude", Binary: "claude", Configure: configureClaude,
		Prompt: claudePrompt, Resumes: true,
	},
	"opencode": {
		ID: "opencode", Binary: "opencode", Configure: configureOpencode,
		Prompt: opencodePrompt, Resumes: true,
	},
	"codex": {ID: "codex", Binary: "codex"},
	"hermes": {
		ID: "hermes", Binary: "hermes", Configure: configureHermes,
		Prompt: hermesPrompt, Resumes: true,
	},
}

func claudePrompt(text string, resume bool) []string {
	if resume {
		return []string{"--continue", text}
	}
	return []string{text}
}

func opencodePrompt(text string, resume bool) []string {
	if resume {
		return []string{"--continue", "--prompt", text}
	}
	return []string{"--prompt", text}
}

func hermesPrompt(text string, resume bool) []string {
	if resume {
		return []string{"chat", "--continue", "--query", text}
	}
	return []string{"chat", "--query", text}
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
