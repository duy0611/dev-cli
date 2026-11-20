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
		"Chief of Staff",
		"only approval point",
		"never do leaf work yourself",
		"bounded tasks with acceptance criteria",
		"do design yourself",
		"Never take the work over on your own",
		"only if the operator tells you",
		"not your built-in subagents",
		"At most two retries",
		instructionTestDispatch + " start ROLE",
		instructionTestDispatch + " wait ID",
		instructionTestDispatch + " result ID",
		instructionTestDispatch + " takeover ID --reason",
		instructionTestDispatch + " list",
		instructionTestDispatch + " start ROLE --retry ID",
		"\n" + instructionTestDispatch + " start researcher --title \"short title\" <<'EOF'\n",
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

func teamOf(roles ...Role) Config {
	c := instructionTestConfig()
	c.Models = map[Role]string{}
	for _, r := range roles {
		c.Models[r] = "m-" + string(r)
	}
	return c
}

func TestInstructionFlowNamesPresentRolesInOrder(t *testing.T) {
	tests := []struct {
		roles []Role
		want  string
	}{
		{[]Role{RoleCoder, RoleTester}, "coder → tester"},
		{AllRoles(), "researcher → architect → coder → tester → reviewer"},
		{[]Role{RoleReviewer}, "reviewer"},
		{[]Role{RoleTester, RoleCoder, RoleReviewer}, "coder → tester → reviewer"},
	}
	for _, tt := range tests {
		got := Instruction(teamOf(tt.roles...), instructionTestDispatch)
		if !strings.Contains(got, "\nThe flow: "+tt.want+".") {
			t.Errorf("%v: flow line %q missing:\n%s", tt.roles, tt.want, got)
		}
	}
}

func TestInstructionMissingRoleRules(t *testing.T) {
	tests := []struct {
		roles    []Role
		want     []string
		unwanted []string
	}{
		{[]Role{RoleCoder},
			[]string{"do research, design and review yourself", "your own subagents may help", "the implementer's own tests stand"},
			[]string{"Never implement yourself", "go to the operator"}},
		{[]Role{RoleReviewer},
			[]string{"do research and design yourself", "Never implement yourself: with no worker to implement, stop and ask the operator.", "Reviewer blockers go to the operator, since no worker can implement the fix."},
			[]string{"the implementer's own tests", "Tester failures"}},
		{[]Role{RoleTester, RoleReviewer},
			[]string{"Tester failures and reviewer blockers go to the operator, since no worker can implement the fix."},
			nil},
		{[]Role{RoleResearcher},
			[]string{"do design and review yourself", "stop and ask the operator"},
			[]string{"go to the operator"}},
		{[]Role{RoleCoder, RoleTester, RoleReviewer, RoleArchitect},
			[]string{"do research yourself"},
			[]string{"Never implement yourself", "own tests stand", "design and", "go to the operator"}},
		{AllRoles(), nil,
			[]string{"Work no worker", "Never implement yourself", "own tests stand", "go to the operator"}},
	}
	for _, tt := range tests {
		got := Instruction(teamOf(tt.roles...), instructionTestDispatch)
		for _, want := range tt.want {
			if !strings.Contains(got, want) {
				t.Errorf("%v: missing %q:\n%s", tt.roles, want, got)
			}
		}
		for _, unwanted := range tt.unwanted {
			if strings.Contains(got, unwanted) {
				t.Errorf("%v: has %q:\n%s", tt.roles, unwanted, got)
			}
		}
		if !strings.Contains(got, "stop and ask the operator what to do") {
			t.Errorf("%v: retry escalation missing", tt.roles)
		}
	}
}

func TestInstructionDutiesForNewRoles(t *testing.T) {
	got := Instruction(teamOf(RoleArchitect, RoleTester), instructionTestDispatch)
	for _, want := range []string{
		"writes the spec from the approved plan, never edits.",
		"tries to break a change with tests against the spec, never fixes source.",
		"design, testing",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

func TestInstructionFullTeamRules(t *testing.T) {
	got := Instruction(teamOf(AllRoles()...), instructionTestDispatch)
	for _, want := range []string{
		"unless it would change what was approved",
		"the spec and acceptance criteria, not the coder's account",
		"start coder --retry ID",
		"Reviewer nits",
		"do not trigger a retry",
		"`blocked`",
		"through the architect",
		"counts as an attempt",
		"retried on the same worker",
		"open questions",
		"rather than invent a requirement",
		"JSON block",
		"is never a pass",
		"At most two retries",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "run the acceptance checks yourself") {
		t.Errorf("tester present, lead must not run the checks:\n%s", got)
	}
	if strings.Contains(got, "implement it yourself and record the takeover") {
		t.Errorf("old takeover phrase present:\n%s", got)
	}
}

func TestInstructionAcceptanceChecksWithoutTester(t *testing.T) {
	got := Instruction(teamOf(RoleCoder, RoleReviewer), instructionTestDispatch)
	if !strings.Contains(got, "run the acceptance checks yourself") {
		t.Errorf("coder without tester: lead must run the checks:\n%s", got)
	}
	if strings.Contains(got, "not the coder's account") || strings.Contains(got, "is never a pass") && !strings.Contains(got, "reviewer") {
		t.Errorf("tester rules leaked into a team with no tester:\n%s", got)
	}
	if got := Instruction(teamOf(RoleReviewer), instructionTestDispatch); strings.Contains(got, "run the acceptance checks yourself") {
		t.Errorf("no coder: nothing to check:\n%s", got)
	}
}

func TestInstructionAsksForTitles(t *testing.T) {
	got := Instruction(testConfig(map[Role]string{RoleResearcher: "haiku", RoleCoder: "sonnet"}), "/x/dev-team")
	for _, want := range []string{
		"/x/dev-team start researcher --title \"short title\" <<'EOF'\n",
		"start ROLE --title \"...\"",
		"start ROLE --retry ID",
		"short title naming the plan task",
		"Task 3: saved tiers",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("instruction lacks %q:\n%s", want, got)
		}
	}
}
