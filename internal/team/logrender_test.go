package team

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestRenderLogClaudeFixture(t *testing.T) {
	f, err := os.Open("testdata/claude-stream.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	defer f.Close()

	var out bytes.Buffer
	if err := RenderLog("claude", f, &out); err != nil {
		t.Fatalf("RenderLog: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"tool: Read /tmp/claudefix/seed.txt",
		"edit: Write /tmp/claudefix/note.txt",
		"text: DONE",
		"result: DONE",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered log lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `{"type"`) {
		t.Errorf("rendered log still contains raw JSON:\n%s", got)
	}
	if lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n"); len(lines) != 4 {
		t.Errorf("rendered %d lines, want one per event (4):\n%s", len(lines), got)
	}
}

func TestRenderLogOpencodeFixture(t *testing.T) {
	f, err := os.Open("testdata/opencode-stream.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	defer f.Close()

	var out bytes.Buffer
	if err := RenderLog("opencode", f, &out); err != nil {
		t.Fatalf("RenderLog: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"text: I'll write the file now.",
		"edit: write /tmp/ocfix/note2.txt",
		"text: DONE",
		"result: DONE",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered log lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `{"type"`) {
		t.Errorf("rendered log still contains raw JSON:\n%s", got)
	}
}

func TestRenderLogPartialLine(t *testing.T) {
	in := `{"type":"assistant","message":{"id":"msg_1","content":[{"type":"text","text":"partial"}]}}`

	var out bytes.Buffer
	if err := RenderLog("claude", strings.NewReader(in), &out); err != nil {
		t.Fatalf("RenderLog: %v", err)
	}
	if got, want := out.String(), "text: partial\n"; got != want {
		t.Errorf("output = %q, want %q: a last line without a newline is still fed", got, want)
	}
}

func TestRenderLogLongLine(t *testing.T) {
	text := strings.Repeat("x", 300<<10)
	in := `{"type":"assistant","message":{"id":"msg_long","content":[{"type":"text","text":"` + text + `"}]}}` + "\n"

	var out bytes.Buffer
	if err := RenderLog("claude", strings.NewReader(in), &out); err != nil {
		t.Fatalf("RenderLog: %v", err)
	}
	if out.Len() < len(text) {
		t.Errorf("rendered %d bytes, want at least the %d-byte text", out.Len(), len(text))
	}
	if !strings.HasPrefix(out.String(), "text: xxx") {
		t.Errorf("output does not start with the text event: %.80q", out.String())
	}
}

func TestRenderLogUnknownTool(t *testing.T) {
	err := RenderLog("hermes", strings.NewReader(""), io.Discard)
	if err == nil {
		t.Fatal("accepted an unknown worker tool")
	}
	if !strings.Contains(err.Error(), "hermes") {
		t.Errorf("error = %q, want it to name the tool", err)
	}
}
