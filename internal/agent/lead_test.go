package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestClaudeLead(t *testing.T) {
	a, err := Lookup("claude")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	// Quotes and newlines must survive as one argument, not be re-split.
	instruction := "You plan and review.\nSay \"got it\" before you start."
	argv, env, err := a.LeadCommand(LeadInput{Instruction: instruction, Model: "opus"},
		[]string{"--resume", "abc"})
	if err != nil {
		t.Fatalf("LeadCommand: %v", err)
	}
	want := []string{"claude", "--model", "opus", "--append-system-prompt", instruction, "--resume", "abc"}
	if !slices.Equal(argv, want) {
		t.Errorf("argv =\n%q\nwant\n%q", argv, want)
	}
	if len(env) != 0 {
		t.Errorf("env = %v, want none", env)
	}

	argv, _, err = a.LeadCommand(LeadInput{Instruction: instruction}, nil)
	if err != nil {
		t.Fatalf("LeadCommand without a model: %v", err)
	}
	want = []string{"claude", "--append-system-prompt", instruction}
	if !slices.Equal(argv, want) {
		t.Errorf("argv =\n%q\nwant\n%q", argv, want)
	}
}

func TestOpencodeLead(t *testing.T) {
	a, err := Lookup("opencode")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	const (
		instruction = "You plan and review.\nSay \"got it\" before you start."
		file        = "/var/dev-state/dev-team/runs/api-20261026T101500Z/lead-instruction.md"
		model       = "litellm/deepseek-v4.1-flash"
	)
	argv, env, err := a.LeadCommand(LeadInput{
		Instruction:     instruction,
		InstructionFile: file,
		Model:           model,
	}, []string{"--continue"})
	if err != nil {
		t.Fatalf("LeadCommand: %v", err)
	}
	want := []string{"opencode", "--standalone", "--continue"}
	if !slices.Equal(argv, want) {
		t.Errorf("argv =\n%q\nwant\n%q", argv, want)
	}

	if len(env) != 1 || env[0].Key != "OPENCODE_CONFIG_CONTENT" {
		t.Fatalf("env = %v, want only OPENCODE_CONFIG_CONTENT", env)
	}
	var cfg struct {
		Instructions []string `json:"instructions"`
		Model        string   `json:"model"`
	}
	if err := json.Unmarshal([]byte(env[0].Value), &cfg); err != nil {
		t.Fatalf("OPENCODE_CONFIG_CONTENT is not JSON: %v: %s", err, env[0].Value)
	}
	if !slices.Equal(cfg.Instructions, []string{file}) {
		t.Errorf("instructions = %q, want only %q", cfg.Instructions, file)
	}
	if cfg.Model != model {
		t.Errorf("model = %q, want %q", cfg.Model, model)
	}

	// The instruction travels by file: neither the text nor its file may
	// reach argv, where a tool would parse or log it.
	for _, arg := range argv {
		if strings.Contains(arg, instruction) {
			t.Errorf("instruction leaked into argv: %q", arg)
		}
	}
	if strings.Contains(env[0].Value, "got it") {
		t.Errorf("instruction text leaked into the environment: %s", env[0].Value)
	}

	_, env, err = a.LeadCommand(LeadInput{Instruction: instruction, InstructionFile: file}, nil)
	if err != nil {
		t.Fatalf("LeadCommand without a model: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(env[0].Value), &raw); err != nil {
		t.Fatalf("OPENCODE_CONFIG_CONTENT is not JSON: %v", err)
	}
	if _, ok := raw["model"]; ok {
		t.Errorf("model set with no --lead-model: %s", env[0].Value)
	}
}

func TestLeadCommandRefusesAgentWithoutLead(t *testing.T) {
	for _, id := range []string{"codex", "hermes"} {
		a, err := Lookup(id)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", id, err)
		}
		argv, env, err := a.LeadCommand(LeadInput{Instruction: "plan and review"}, nil)
		if err == nil {
			t.Errorf("%s led a team: %v", id, argv)
			continue
		}
		if argv != nil || env != nil {
			t.Errorf("%s returned argv %v, env %v alongside the error", id, argv, env)
		}
		if !strings.Contains(err.Error(), id) || !strings.Contains(err.Error(), "cannot lead") {
			t.Errorf("%s error %q does not name the agent and the refusal", id, err)
		}
	}
}
