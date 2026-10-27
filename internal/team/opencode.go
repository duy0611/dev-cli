package team

import (
	"encoding/json"
	"slices"
)

// opencodeConfigEnv is the per-run config opencode reads. --standalone is what
// makes it read this instead of the background service's settings.
const opencodeConfigEnv = "OPENCODE_CONFIG_CONTENT"

// readOnlyDeniedActions are v2 permission actions, which are tool names: bash is
// not one, and denying edit alone still leaves the shell hole open.
var readOnlyDeniedActions = []string{"edit", "write", "shell", "subagent", "execute"}

type opencodePermission struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   string `json:"effect"`
}

type opencodeAgentConfig struct {
	Mode        string               `json:"mode"`
	Steps       *int                 `json:"steps,omitempty"`
	Permissions []opencodePermission `json:"permissions,omitempty"`
}

type opencodeConfig struct {
	Agents map[string]opencodeAgentConfig `json:"agents"`
}

// opencodeWorker gives the worker a named primary agent so its permissions and
// step backstop apply. The task stays on stdin: opencode 2.0.32 reads a piped
// message when no positional one is given, which keeps large tasks off argv.
func opencodeWorker(w WorkerSpec) (WorkerCmd, Parser) {
	argv := []string{"opencode", "run", "--standalone", "--auto", "--format", "json"}
	if w.Model != "" {
		argv = append(argv, "-m", w.Model)
	}
	argv = append(argv, "--agent", string(w.Role))
	if w.SessionID != "" {
		argv = append(argv, "-s", w.SessionID)
	}

	agent := opencodeAgentConfig{Mode: "primary"}
	if w.MaxTurns > 0 {
		// Dispatch counts step_start and stops first; steps only bounds a run
		// whose supervisor is gone.
		steps := w.MaxTurns + 1
		agent.Steps = &steps
	}
	if w.Role == RoleReviewer || w.Role == RoleResearcher {
		for _, action := range readOnlyDeniedActions {
			agent.Permissions = append(agent.Permissions, opencodePermission{
				Action: action, Resource: "*", Effect: "deny",
			})
		}
	}

	// The value is plain strings, an int and a map, so Marshal cannot fail.
	config, _ := json.Marshal(opencodeConfig{Agents: map[string]opencodeAgentConfig{string(w.Role): agent}})
	return WorkerCmd{
		Argv:  argv,
		Env:   []string{opencodeConfigEnv + "=" + string(config)},
		Stdin: true,
	}, newOpencodeParser()
}

// opencodeParser decodes lazily, field by field: one mistyped field must not
// cost the line its turn, session or text.
type opencodeParser struct {
	sessionID  string
	turns      int
	final      string
	inStep     bool
	costSum    float64
	costSteps  int
	failed     bool
	failReason string
	edited     []string
	editedSeen map[string]struct{}
}

func newOpencodeParser() *opencodeParser {
	return &opencodeParser{editedSeen: make(map[string]struct{})}
}

func (p *opencodeParser) Feed(line []byte) []Event {
	fields, ok := jsonFields(line)
	if !ok {
		return []Event{{Kind: "other"}}
	}
	if session := fieldString(fields, "sessionID"); session != "" {
		p.sessionID = session
	}

	switch fieldString(fields, "type") {
	case "step_start":
		p.turns++
		p.inStep = true
		// A step starts a fresh answer: a run that ends on a tool call must not
		// report the previous step's narration as its final text.
		p.final = ""
		return nil
	case "text":
		part, ok := fieldObject(fields, "part")
		if !ok {
			return []Event{{Kind: "other"}}
		}
		text := fieldString(part, "text")
		if text == "" {
			return []Event{{Kind: "other"}}
		}
		if p.inStep {
			p.final = text
		}
		return []Event{{Kind: "text", Text: text}}
	case "tool_use":
		return p.toolEvents(fields)
	case "step_finish":
		return p.stepFinishEvents(fields)
	case "error":
		p.failed = true
		if detail, ok := fieldObject(fields, "error"); ok {
			p.failReason = fieldString(detail, "message")
		}
		return []Event{{Kind: "other"}}
	}
	return []Event{{Kind: "other"}}
}

func (p *opencodeParser) toolEvents(line map[string]json.RawMessage) []Event {
	part, ok := fieldObject(line, "part")
	if !ok {
		return []Event{{Kind: "other"}}
	}
	tool := fieldString(part, "tool")
	if tool == "" {
		return []Event{{Kind: "other"}}
	}
	state, _ := fieldObject(part, "state")
	input, _ := fieldObject(state, "input")
	path := fieldString(input, "path")

	if path != "" && (tool == "write" || tool == "edit") {
		if _, seen := p.editedSeen[path]; !seen {
			p.editedSeen[path] = struct{}{}
			p.edited = append(p.edited, path)
		}
		return []Event{{Kind: "edit", Tool: tool, Text: path}}
	}
	detail := path
	if detail == "" {
		detail = fieldString(input, "command")
	}
	return []Event{{Kind: "tool", Tool: tool, Text: detail}}
}

func (p *opencodeParser) stepFinishEvents(line map[string]json.RawMessage) []Event {
	part, ok := fieldObject(line, "part")
	if !ok {
		return []Event{{Kind: "other"}}
	}
	if cost, ok := fieldFloat(part, "cost"); ok {
		p.costSum += cost
		p.costSteps++
	}
	if fieldString(part, "reason") == "stop" {
		return []Event{{Kind: "result", Text: p.final}}
	}
	return nil
}

func (p *opencodeParser) Outcome() Outcome {
	var cost *float64
	if p.costSteps > 0 {
		sum := p.costSum
		cost = &sum
	}
	return Outcome{
		Final:      p.final,
		SessionID:  p.sessionID,
		CostUSD:    cost,
		Turns:      p.turns,
		Edited:     slices.Clone(p.edited),
		Failed:     p.failed,
		FailReason: p.failReason,
	}
}

// jsonFields decodes one line as an object, leaving each field raw so a
// mistyped field costs only itself.
func jsonFields(line []byte) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil || fields == nil {
		return nil, false
	}
	return fields, true
}

func fieldObject(fields map[string]json.RawMessage, key string) (map[string]json.RawMessage, bool) {
	raw, ok := fields[key]
	if !ok {
		return nil, false
	}
	return jsonFields(raw)
}

func fieldString(fields map[string]json.RawMessage, key string) string {
	var value string
	if raw, ok := fields[key]; ok && json.Unmarshal(raw, &value) == nil {
		return value
	}
	return ""
}

func fieldFloat(fields map[string]json.RawMessage, key string) (float64, bool) {
	var value *float64
	if raw, ok := fields[key]; ok && json.Unmarshal(raw, &value) == nil && value != nil {
		return *value, true
	}
	return 0, false
}
