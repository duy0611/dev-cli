package team

import (
	"encoding/json"
	"slices"
)

// claudeEditTools are the assistant tool names that change files and carry the
// path in input.file_path. Read produces a tool result with a path too, so the
// tool name alone decides what was edited.
var claudeEditTools = map[string]bool{
	"Write":        true,
	"Edit":         true,
	"MultiEdit":    true,
	"NotebookEdit": true,
}

type claudeBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// inputString reads a tool input field without letting one field's unexpected
// type discard the block's tool name, or the whole line's turn count.
func (b claudeBlock) inputString(keys ...string) string {
	var input map[string]any
	if err := json.Unmarshal(b.Input, &input); err != nil {
		return ""
	}
	for _, key := range keys {
		if value, ok := input[key].(string); ok {
			return value
		}
	}
	return ""
}

type claudeMessage struct {
	ID      string          `json:"id"`
	Content json.RawMessage `json:"content"`
}

type claudeLine struct {
	Type            string          `json:"type"`
	Message         json.RawMessage `json:"message"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	SessionID       string          `json:"session_id"`
	TotalCostUSD    *float64        `json:"total_cost_usd"`
	NumTurns        *int            `json:"num_turns"`
	IsError         bool            `json:"is_error"`
	Result          string          `json:"result"`
}

type claudeParser struct {
	seenMessages map[string]struct{}
	turns        int
	resultTurns  *int
	sessionID    string
	final        string
	cost         *float64
	failed       bool
	edited       []string
	editedSeen   map[string]struct{}
}

func newClaudeParser() *claudeParser {
	return &claudeParser{
		seenMessages: make(map[string]struct{}),
		editedSeen:   make(map[string]struct{}),
	}
}

func (p *claudeParser) Feed(line []byte) []Event {
	var parsed claudeLine
	if err := json.Unmarshal(line, &parsed); err != nil {
		return []Event{{Kind: "other"}}
	}
	if parsed.SessionID != "" {
		p.sessionID = parsed.SessionID
	}
	switch parsed.Type {
	case "assistant":
		return p.assistantEvents(&parsed)
	case "result":
		p.final = parsed.Result
		p.cost = parsed.TotalCostUSD
		p.resultTurns = parsed.NumTurns
		p.failed = parsed.IsError
		return []Event{{Kind: "result", Text: parsed.Result}}
	case "system", "user":
		return nil
	}
	return []Event{{Kind: "other"}}
}

func (p *claudeParser) assistantEvents(line *claudeLine) []Event {
	var msg claudeMessage
	_ = json.Unmarshal(line.Message, &msg)

	// Thinking and tool_use arrive as separate assistant lines of one message,
	// so a turn is a distinct message id, and a subagent's lines (non-null
	// parent_tool_use_id) are not the worker's turns.
	if line.ParentToolUseID == nil && msg.ID != "" {
		if _, seen := p.seenMessages[msg.ID]; !seen {
			p.seenMessages[msg.ID] = struct{}{}
			p.turns++
		}
	}

	var rawBlocks []json.RawMessage
	if err := json.Unmarshal(msg.Content, &rawBlocks); err != nil {
		return nil
	}

	var events []Event
	for _, rawBlock := range rawBlocks {
		var block claudeBlock
		if err := json.Unmarshal(rawBlock, &block); err != nil {
			continue
		}
		switch block.Type {
		case "text":
			if block.Text != "" {
				events = append(events, Event{Kind: "text", Text: block.Text})
			}
		case "tool_use":
			if claudeEditTools[block.Name] {
				path := block.inputString("file_path", "notebook_path")
				if path == "" {
					events = append(events, Event{Kind: "tool", Tool: block.Name})
					continue
				}
				if _, seen := p.editedSeen[path]; !seen {
					p.editedSeen[path] = struct{}{}
					p.edited = append(p.edited, path)
				}
				events = append(events, Event{Kind: "edit", Tool: block.Name, Text: path})
				continue
			}
			detail := block.inputString("file_path", "notebook_path", "command")
			events = append(events, Event{Kind: "tool", Tool: block.Name, Text: detail})
		}
	}
	return events
}

func (p *claudeParser) Outcome() Outcome {
	turns := p.turns
	if p.resultTurns != nil {
		turns = *p.resultTurns
	}
	return Outcome{
		Final:     p.final,
		SessionID: p.sessionID,
		CostUSD:   p.cost,
		Turns:     turns,
		Edited:    slices.Clone(p.edited),
		Failed:    p.failed,
	}
}

// claudeWorker keeps the task off argv: Claude reads a piped prompt, and the
// OS single-argument limit then stops mattering.
func claudeWorker(w WorkerSpec) (WorkerCmd, Parser) {
	// Claude has no documented turn flag; dispatch enforces MaxTurns by
	// counting turns in the stream.
	argv := []string{
		"claude", "-p",
		"--output-format", "stream-json",
		"--verbose",
	}
	if w.Model != "" {
		argv = append(argv, "--model", w.Model)
	}
	argv = append(argv, "--permission-mode", "auto")
	if w.SessionID != "" {
		argv = append(argv, "--resume", w.SessionID)
	}
	if w.Role == RoleReviewer || w.Role == RoleResearcher {
		// --disallowedTools is variadic and would swallow a following
		// positional, so it stays last.
		argv = append(argv, "--disallowedTools", "Edit", "Write", "MultiEdit", "NotebookEdit")
	}
	return WorkerCmd{Argv: argv, Stdin: true}, newClaudeParser()
}
