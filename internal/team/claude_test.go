package team

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestClaudeArgv(t *testing.T) {
	// The task goes on stdin; no argv element may ever carry it.
	const task = "task text that must never reach argv"

	base := func(model string) []string {
		return []string{
			"claude", "-p",
			"--output-format", "stream-json",
			"--verbose",
			"--model", model,
			"--permission-mode", "auto",
		}
	}
	locked := func(argv []string) []string {
		return append(argv, "--disallowedTools", "Edit", "Write", "MultiEdit", "NotebookEdit")
	}

	tests := []struct {
		name string
		spec WorkerSpec
		want []string
	}{
		{
			name: "coder",
			spec: WorkerSpec{Role: RoleCoder, Model: "M"},
			want: base("M"),
		},
		{
			name: "coder without a model",
			spec: WorkerSpec{Role: RoleCoder},
			want: []string{
				"claude", "-p",
				"--output-format", "stream-json",
				"--verbose",
				"--permission-mode", "auto",
			},
		},
		{
			name: "reviewer locks editing",
			spec: WorkerSpec{Role: RoleReviewer, Model: "M"},
			want: locked(base("M")),
		},
		{
			name: "researcher locks editing",
			spec: WorkerSpec{Role: RoleResearcher, Model: "M"},
			want: locked(base("M")),
		},
		{
			name: "resume",
			spec: WorkerSpec{Role: RoleCoder, Model: "M", SessionID: "s1"},
			want: append(base("M"), "--resume", "s1"),
		},
		{
			name: "resume with a lock keeps the lock last",
			spec: WorkerSpec{Role: RoleReviewer, Model: "M", SessionID: "s1"},
			want: locked(append(base("M"), "--resume", "s1")),
		},
		{
			name: "max turns is dispatch's job",
			spec: WorkerSpec{Role: RoleCoder, Model: "M", MaxTurns: 7},
			want: base("M"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, parser, err := Worker("claude", tt.spec)
			if err != nil {
				t.Fatalf("Worker: %v", err)
			}
			if parser == nil {
				t.Fatal("Worker returned no parser")
			}
			if !cmd.Stdin {
				t.Error("Stdin = false, want the task fed on stdin")
			}
			if !slices.Equal(cmd.Argv, tt.want) {
				t.Errorf("argv = %q, want %q", cmd.Argv, tt.want)
			}
			for _, arg := range cmd.Argv {
				if strings.Contains(arg, task) {
					t.Errorf("argv element %q carries the task text", arg)
				}
				if strings.Contains(arg, "max-turns") {
					t.Errorf("argv element %q sets a turn cap; dispatch counts turns", arg)
				}
			}
		})
	}
}

func TestClaudeParserFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/claude-stream.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	p := newClaudeParser()
	var events []Event
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		events = append(events, p.Feed(line)...)
	}

	got := p.Outcome()
	if got.Final != "DONE" {
		t.Errorf("Final = %q, want %q", got.Final, "DONE")
	}
	if got.SessionID != "f7771d53-d8d6-4c81-969a-39ae4219bbb9" {
		t.Errorf("SessionID = %q, want the fixture's session", got.SessionID)
	}
	if got.CostUSD == nil || *got.CostUSD != 0.006312285 {
		t.Errorf("CostUSD = %v, want 0.006312285", got.CostUSD)
	}
	if got.Turns != 3 {
		t.Errorf("Turns = %d, want 3 (4 assistant lines, 3 message ids)", got.Turns)
	}
	if want := []string{"/tmp/claudefix/note.txt"}; !slices.Equal(got.Edited, want) {
		t.Errorf("Edited = %q, want %q", got.Edited, want)
	}
	if got.Failed {
		t.Error("Failed = true, want false for a success result")
	}

	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	if want := []string{"tool", "edit", "text", "result"}; !slices.Equal(kinds, want) {
		t.Errorf("event kinds = %q, want %q", kinds, want)
	}
	for _, e := range events {
		switch e.Kind {
		case "tool":
			if e.Tool != "Read" || e.Text != "/tmp/claudefix/seed.txt" {
				t.Errorf("tool event = %+v, want Read of the seed file", e)
			}
		case "edit":
			if e.Tool != "Write" || e.Text != "/tmp/claudefix/note.txt" {
				t.Errorf("edit event = %+v, want Write of note.txt", e)
			}
		case "text":
			if e.Text != "DONE" {
				t.Errorf("text event = %+v, want DONE", e)
			}
		case "result":
			if e.Text != "DONE" {
				t.Errorf("result event = %+v, want DONE", e)
			}
		}
	}

	t.Run("running count and subagent lines", func(t *testing.T) {
		p := newClaudeParser()
		p.Feed([]byte(`{"type":"assistant","message":{"id":"msg_1","content":[{"type":"thinking"}]},"parent_tool_use_id":null}`))
		p.Feed([]byte(`{"type":"assistant","message":{"id":"msg_1","content":[{"type":"tool_use","name":"Read"}]},"parent_tool_use_id":null}`))
		if got := p.Outcome().Turns; got != 1 {
			t.Errorf("running Turns = %d, want 1: two lines of one message are one turn", got)
		}

		p.Feed([]byte(`{"type":"assistant","message":{"id":"msg_sub","content":[{"type":"text","text":"subagent"}]},"parent_tool_use_id":"toolu_1"}`))
		if got := p.Outcome().Turns; got != 1 {
			t.Errorf("running Turns = %d, want 1: a subagent line is not the worker's turn", got)
		}

		p.Feed([]byte(`{"type":"assistant","message":{"id":"msg_2","content":[{"type":"text","text":"working"}]}}`))
		if got := p.Outcome().Turns; got != 2 {
			t.Errorf("running Turns = %d, want 2", got)
		}
		if got := p.Outcome().Final; got != "" {
			t.Errorf("Final = %q before the result line, want empty", got)
		}

		p.Feed([]byte(`{"type":"result","session_id":"s","total_cost_usd":0.25,"num_turns":5,"is_error":false,"subtype":"success","result":"DONE"}`))
		if got := p.Outcome().Turns; got != 5 {
			t.Errorf("Turns = %d after the result line, want the reported 5", got)
		}
	})
}

func TestClaudeParserSurvivesGarbage(t *testing.T) {
	p := newClaudeParser()

	garbage := [][]byte{
		[]byte(`{"type":"assistant","message":`),
		[]byte("not json at all"),
		bytes.Repeat([]byte("x"), 200<<10),
	}
	for _, line := range garbage {
		events := p.Feed(line)
		if len(events) != 1 || events[0].Kind != "other" {
			t.Errorf("Feed(%.40q) = %+v, want one other event", line, events)
		}
	}

	// The caller uses ReadBytes, so the last line may arrive without a newline.
	events := p.Feed([]byte(`{"type":"result","session_id":"s1","total_cost_usd":0.5,"num_turns":2,"is_error":false,"subtype":"success","result":"DONE"}`))
	if len(events) != 1 || events[0].Kind != "result" || events[0].Text != "DONE" {
		t.Fatalf("Feed(result) = %+v, want one result event", events)
	}

	got := p.Outcome()
	if got.Final != "DONE" || got.SessionID != "s1" || got.Turns != 2 {
		t.Errorf("Outcome = %+v, want the later result line", got)
	}
	if got.CostUSD == nil || *got.CostUSD != 0.5 {
		t.Errorf("CostUSD = %v, want 0.5", got.CostUSD)
	}
}

func TestClaudeParserNotebookEdit(t *testing.T) {
	p := newClaudeParser()

	notebook := []byte(`{"type":"assistant","message":{"id":"msg_1","content":[{"type":"tool_use","name":"NotebookEdit","input":{"notebook_path":"/tmp/claudefix/nb.ipynb"}}]},"parent_tool_use_id":null}`)
	events := p.Feed(notebook)
	if len(events) != 1 || events[0].Kind != "edit" || events[0].Tool != "NotebookEdit" || events[0].Text != "/tmp/claudefix/nb.ipynb" {
		t.Fatalf("NotebookEdit events = %+v, want one edit carrying the notebook path", events)
	}

	p.Feed([]byte(`{"type":"assistant","message":{"id":"msg_2","content":[{"type":"tool_use","name":"Write","input":{"file_path":"/tmp/claudefix/note.txt"}}]}}`))
	p.Feed([]byte(`{"type":"assistant","message":{"id":"msg_3","content":[{"type":"tool_use","name":"NotebookEdit","input":{"notebook_path":"/tmp/claudefix/nb.ipynb"}}]}}`))

	want := []string{"/tmp/claudefix/nb.ipynb", "/tmp/claudefix/note.txt"}
	if got := p.Outcome().Edited; !slices.Equal(got, want) {
		t.Errorf("Edited = %q, want %q (notebook first, deduped, then the file edit)", got, want)
	}
}

func TestClaudeParserTolerant(t *testing.T) {
	t.Run("user content that is not an array", func(t *testing.T) {
		p := newClaudeParser()
		if events := p.Feed([]byte(`{"type":"user","message":{"content":"not blocks"}}`)); len(events) != 0 {
			t.Fatalf("Feed(user) = %+v, want no events", events)
		}

		events := p.Feed([]byte(`{"type":"assistant","message":{"id":"msg_1","content":[{"type":"text","text":"working"}]}}`))
		if len(events) != 1 || events[0].Kind != "text" {
			t.Fatalf("later assistant line = %+v, want one text event", events)
		}
		if got := p.Outcome().Turns; got != 1 {
			t.Errorf("Turns = %d, want 1", got)
		}
	})

	t.Run("tool input with an unexpected shape", func(t *testing.T) {
		p := newClaudeParser()
		line := []byte(`{"type":"assistant","message":{"id":"msg_1","content":[` +
			`{"type":"tool_use","name":"Bash","input":{"command":42}},` +
			`{"type":"tool_use","name":"Read","input":"not an object"},` +
			`{"type":"tool_use","name":"Write","input":{"file_path":"/tmp/claudefix/note.txt"}}` +
			`]}}`)
		events := p.Feed(line)

		var kinds []string
		for _, e := range events {
			kinds = append(kinds, e.Kind)
		}
		if want := []string{"tool", "tool", "edit"}; !slices.Equal(kinds, want) {
			t.Fatalf("events = %+v, want kinds %q", events, want)
		}
		if got := p.Outcome().Turns; got != 1 {
			t.Errorf("Turns = %d, want 1 even with a malformed tool input", got)
		}
		if want := []string{"/tmp/claudefix/note.txt"}; !slices.Equal(p.Outcome().Edited, want) {
			t.Errorf("Edited = %q, want %q", p.Outcome().Edited, want)
		}
	})

	t.Run("a field the parser does not need", func(t *testing.T) {
		p := newClaudeParser()
		line := []byte(`{"type":"assistant","message":{"id":"msg_1","content":[{"type":"text","text":"working"}]},"usage":{"input_tokens":"lots"}}`)
		events := p.Feed(line)
		if len(events) != 1 || events[0].Kind != "text" {
			t.Fatalf("events = %+v, want one text event", events)
		}
		if got := p.Outcome().Turns; got != 1 {
			t.Errorf("Turns = %d, want 1", got)
		}
	})
}

func TestRender(t *testing.T) {
	tests := []struct {
		name  string
		event Event
		want  string
	}{
		{
			name:  "tool call",
			event: Event{Kind: "tool", Tool: "Read", Text: "/tmp/claudefix/seed.txt"},
			want:  "tool: Read /tmp/claudefix/seed.txt",
		},
		{
			name:  "edit",
			event: Event{Kind: "edit", Tool: "Write", Text: "/tmp/claudefix/note.txt"},
			want:  "edit: Write /tmp/claudefix/note.txt",
		},
		{
			name:  "text",
			event: Event{Kind: "text", Text: "DONE"},
			want:  "text: DONE",
		},
		{
			name:  "text stays one line",
			event: Event{Kind: "text", Text: "line one\nline two"},
			want:  "text: line one line two",
		},
		{
			name:  "result",
			event: Event{Kind: "result", Text: "DONE"},
			want:  "result: DONE",
		},
		{
			name:  "noise",
			event: Event{Kind: "other"},
			want:  "",
		},
		{
			name:  "unknown kind",
			event: Event{Kind: "weird"},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Render(tt.event); got != tt.want {
				t.Errorf("Render(%+v) = %q, want %q", tt.event, got, tt.want)
			}
		})
	}
}
