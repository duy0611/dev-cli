package team

import "strings"

// Event is one parsed line of a worker's output stream.
type Event struct {
	Kind string // "text", "tool", "edit", "result" or "other"
	Tool string // tool name for "tool" and "edit" events
	Text string // message text, path, or command detail
}

// Outcome is what a worker's stream says when the task ends.
type Outcome struct {
	Final      string   // the worker's final answer
	SessionID  string   // the session to resume on a retry
	CostUSD    *float64 // nil when the tool did not report it
	Turns      int      // claude's distinct assistant messages or opencode's step_start events
	Edited     []string // paths the worker edited, in stream order
	Failed     bool     // the tool reported an error result
	FailReason string   // the tool's own error text, when it reported one
}

// Parser turns a worker's output into events, one line at a time. It never
// errors: unknown or malformed input is reported as an "other" event.
type Parser interface {
	Feed(line []byte) []Event
	Outcome() Outcome
}

// WorkerSpec is the per-task input to a worker adapter.
type WorkerSpec struct {
	Role      Role
	Model     string
	SessionID string
	MaxTurns  int
}

// WorkerCmd is how a worker tool is launched for one task: argv, the
// environment entries it needs, and whether the task text has to reach it on
// stdin rather than in argv.
type WorkerCmd struct {
	Argv  []string
	Env   []string
	Stdin bool
}

// Render returns one readable log line for an event, or "" for noise.
func Render(e Event) string {
	var detail string
	switch e.Kind {
	case "tool", "edit":
		detail = strings.TrimSpace(e.Tool + " " + e.Text)
	case "text", "result":
		detail = e.Text
	default:
		return ""
	}
	detail = strings.Join(strings.Fields(detail), " ")
	if detail == "" {
		return e.Kind
	}
	return e.Kind + ": " + detail
}
