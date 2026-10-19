package agent

import (
	"reflect"
	"strings"
	"testing"
)

func TestPromptCommand(t *testing.T) {
	const text = "review this"
	tests := []struct {
		id     string
		resume bool
		want   []string
	}{
		{"claude", true, []string{"claude", "--continue", text}},
		{"claude", false, []string{"claude", text}},
		{"opencode", true, []string{"opencode", "--continue", "--prompt", text}},
		{"opencode", false, []string{"opencode", "--prompt", text}},
		{"hermes", true, []string{"hermes", "chat", "--continue", "--query", text}},
		{"hermes", false, []string{"hermes", "chat", "--query", text}},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			a, err := Lookup(tc.id)
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			argv, resumed, err := a.PromptCommand(text, tc.resume)
			if err != nil {
				t.Fatalf("PromptCommand: %v", err)
			}
			if !reflect.DeepEqual(argv, tc.want) {
				t.Errorf("argv = %v, want %v", argv, tc.want)
			}
			if resumed != tc.resume {
				t.Errorf("resumed = %v, want %v", resumed, tc.resume)
			}
		})
	}
}

func TestPromptCommandCodexRefused(t *testing.T) {
	a, err := Lookup("codex")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	_, _, err = a.PromptCommand("review this", false)
	if err == nil {
		t.Fatal("codex accepted a first message, want an error")
	}
	if !strings.Contains(err.Error(), "codex") {
		t.Errorf("error = %q, want it to name codex", err)
	}
}

func TestPromptCommandFallsBackWithoutResume(t *testing.T) {
	a := Agent{
		ID: "test", Binary: "test",
		Prompt: func(text string, resume bool) []string {
			if resume {
				return []string{"--resume", text}
			}
			return []string{text}
		},
		Resumes: false,
	}

	argv, resumed, err := a.PromptCommand("review this", true)
	if err != nil {
		t.Fatalf("PromptCommand: %v", err)
	}
	if resumed {
		t.Error("resumed is true for an agent that cannot resume")
	}
	if !reflect.DeepEqual(argv, []string{"test", "review this"}) {
		t.Errorf("argv = %v, want the new-session form", argv)
	}
}
