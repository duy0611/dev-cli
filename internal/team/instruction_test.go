package team

import (
	"strings"
	"testing"
)

const instructionTestDispatch = "/usr/local/bin/dev-team"

func instructionTestConfig() Config {
	return Config{
		Lead:    Tool{ID: "claude", Model: "opus"},
		Workers: "claude",
		Models: map[Role]string{
			RoleCoder:      "claude-sonnet-5",
			RoleReviewer:   "claude-opus-5",
			RoleResearcher: "claude-haiku-5",
		},
		MaxTurns: DefaultMaxTurns,
		Timeout:  DefaultTimeout,
	}
}

func TestInstructionNamesOnlyPresentRoles(t *testing.T) {
	tests := []struct {
		name     string
		models   map[Role]string
		want     []string
		unwanted []string
	}{
		{
			name:     "coder only",
			models:   map[Role]string{RoleCoder: "claude-sonnet-5"},
			want:     []string{"coder", "claude-sonnet-5"},
			unwanted: []string{"reviewer", "researcher"},
		},
		{
			name:     "reviewer only",
			models:   map[Role]string{RoleReviewer: "claude-opus-5"},
			want:     []string{"reviewer", "claude-opus-5"},
			unwanted: []string{"coder", "researcher"},
		},
		{
			name:     "researcher only",
			models:   map[Role]string{RoleResearcher: "claude-haiku-5"},
			want:     []string{"researcher", "claude-haiku-5"},
			unwanted: []string{"coder", "reviewer"},
		},
		{
			name: "all three",
			models: map[Role]string{
				RoleCoder:      "claude-sonnet-5",
				RoleReviewer:   "claude-opus-5",
				RoleResearcher: "claude-haiku-5",
			},
			want: []string{
				"coder", "claude-sonnet-5",
				"reviewer", "claude-opus-5",
				"researcher", "claude-haiku-5",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := instructionTestConfig()
			c.Models = tt.models
			got := Instruction(c, instructionTestDispatch)

			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("instruction does not mention %q:\n%s", want, got)
				}
			}
			for _, unwanted := range tt.unwanted {
				if strings.Contains(got, unwanted) {
					t.Errorf("instruction mentions absent role %q:\n%s", unwanted, got)
				}
			}
		})
	}
}

func TestInstructionCarriesTheRules(t *testing.T) {
	got := Instruction(instructionTestConfig(), instructionTestDispatch)

	for _, want := range []string{
		"do not implement it yourself",
		"not your built-in subagents",
		"send the change to the reviewer",
		"At most two retries",
		"implement it yourself and record the takeover",
		instructionTestDispatch + " start ROLE",
		instructionTestDispatch + " wait ID",
		instructionTestDispatch + " result ID",
		instructionTestDispatch + " takeover ID --reason",
		instructionTestDispatch + " list",
		instructionTestDispatch + " start ROLE --retry ID",
		"\n" + instructionTestDispatch + " start coder <<'EOF'\n",
		"\nEOF\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("instruction missing %q:\n%s", want, got)
		}
	}
}

func TestInstructionTellsLeadToLoopOnWait(t *testing.T) {
	got := Instruction(instructionTestConfig(), instructionTestDispatch)

	for _, want := range []string{
		"STATUS: running",
		"call wait again",
		"never give up",
		"tool call timed out",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("instruction missing %q:\n%s", want, got)
		}
	}
}
