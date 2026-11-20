package team

import (
	"strings"
	"testing"
)

func TestRolePromptReportFormat(t *testing.T) {
	for _, role := range AllRoles() {
		prompt := RolePrompt(role)
		for _, want := range []string{`"status"`, `"summary"`, `"artifacts"`, `"blockers"`, "done | failed | blocked"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s prompt lacks %q", role, want)
			}
		}
		if got := strings.Contains(prompt, `"nits"`); got != (role == RoleReviewer) {
			t.Errorf("%s prompt mentions nits = %v", role, got)
		}
	}
}

func TestRolePromptBoundaries(t *testing.T) {
	tests := []struct {
		role Role
		want []string
	}{
		{RoleCoder, []string{"test-first", "never weaken or change a test you did not write"}},
		{RoleTester, []string{"reproduction", "never edit non-test source"}},
		{RoleArchitect, []string{"never write files"}},
		{RoleResearcher, []string{"never edit"}},
		{RoleReviewer, []string{"never edit", "blockers", "nits", "style"}},
	}
	for _, tt := range tests {
		for _, want := range tt.want {
			if !strings.Contains(RolePrompt(tt.role), want) {
				t.Errorf("%s prompt lacks %q", tt.role, want)
			}
		}
	}
}
