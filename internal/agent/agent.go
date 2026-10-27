// Package agent knows which coding agents a container can run and how to invoke
// them.
package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/duy0611/dev-cli/internal/agentcfg"
	"github.com/duy0611/dev-cli/internal/provider"
)

// LeadInput is what a lead agent needs for its standing instruction. Claude
// takes the text; opencode reads InstructionFile, which the launcher writes into
// the run folder so the text never reaches argv.
type LeadInput struct {
	Instruction     string
	InstructionFile string
	Model           string
}

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
	// Lead renders the arguments and environment that give the agent its
	// standing instruction as a team lead. Nil when the agent has no channel
	// for one.
	Lead func(in LeadInput) ([]string, []provider.EnvVar, error)
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

// LeadCommand is the full argv that runs the agent as a team lead, extra
// following the lead's own arguments. An agent without Lead is an error
// naming it.
func (a Agent) LeadCommand(in LeadInput, extra []string) (argv []string, env []provider.EnvVar, err error) {
	if a.Lead == nil {
		return nil, nil, fmt.Errorf("agent %q cannot lead a team: no way to give it a standing instruction", a.ID)
	}
	lead, env, err := a.Lead(in)
	if err != nil {
		return nil, nil, err
	}
	cmd := a.Command(nil)
	cmd = append(cmd, lead...)
	return append(cmd, extra...), env, nil
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
		Prompt: claudePrompt, Resumes: true, Lead: claudeLead,
	},
	"opencode": {
		ID: "opencode", Binary: "opencode", Configure: configureOpencode,
		Prompt: opencodePrompt, Resumes: true, Lead: opencodeLead,
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

// claudeLead gives Claude the instruction as an appended system prompt, which
// leaves the working tree untouched.
func claudeLead(in LeadInput) ([]string, []provider.EnvVar, error) {
	var args []string
	if in.Model != "" {
		args = append(args, "--model", in.Model)
	}
	return append(args, "--append-system-prompt", in.Instruction), nil, nil
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
