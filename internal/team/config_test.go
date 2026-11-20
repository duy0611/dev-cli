package team

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseWorkers(t *testing.T) {
	tests := []struct {
		name    string
		specs   []string
		want    map[Role]string
		wantErr string
	}{
		{
			name:  "one worker",
			specs: []string{"coder=a/b"},
			want:  map[Role]string{RoleCoder: "a/b"},
		},
		{
			name:  "all three roles",
			specs: []string{"coder=a/b", "reviewer=c/d", "researcher=e/f"},
			want: map[Role]string{
				RoleCoder:      "a/b",
				RoleReviewer:   "c/d",
				RoleResearcher: "e/f",
			},
		},
		{
			name:  "architect and tester",
			specs: []string{"architect=x", "tester=y"},
			want:  map[Role]string{RoleArchitect: "x", RoleTester: "y"},
		},
		{
			name:    "empty model",
			specs:   []string{"coder="},
			wantErr: "coder",
		},
		{
			name:    "empty role",
			specs:   []string{"=x"},
			wantErr: "=x",
		},
		{
			name:    "no equals",
			specs:   []string{"coder"},
			wantErr: "coder",
		},
		{
			name:    "unknown role",
			specs:   []string{"qa=x"},
			wantErr: "qa",
		},
		{
			name:    "repeated role",
			specs:   []string{"coder=a/b", "coder=c/d"},
			wantErr: "coder",
		},
		{
			name:    "four specs",
			specs:   []string{"coder=a", "reviewer=b", "researcher=c", "coder=d"},
			wantErr: "coder",
		},
		{
			name:  "model keeps everything after the first equals",
			specs: []string{"coder=x=y/z"},
			want:  map[Role]string{RoleCoder: "x=y/z"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseWorkers(tt.specs)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ParseWorkers(%q) = %v, want error naming %q", tt.specs, got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want it to name %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseWorkers(%q): %v", tt.specs, err)
			}
			if !maps.Equal(got, tt.want) {
				t.Errorf("ParseWorkers(%q) = %v, want %v", tt.specs, got, tt.want)
			}
		})
	}
}

func TestValidateRefusesHermesAndCodex(t *testing.T) {
	base := func() Config {
		return Config{
			Lead:     Tool{ID: "claude"},
			Workers:  "opencode",
			Models:   map[Role]string{RoleCoder: "a/b"},
			MaxTurns: DefaultMaxTurns,
			Timeout:  DefaultTimeout,
		}
	}

	t.Run("claude lead and opencode workers", func(t *testing.T) {
		if err := base().Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})

	for _, tool := range []string{"hermes", "codex", "nope"} {
		t.Run(tool+" as lead", func(t *testing.T) {
			c := base()
			c.Lead.ID = tool
			assertRefused(t, c.Validate(), tool)
		})
		t.Run(tool+" as worker", func(t *testing.T) {
			c := base()
			c.Workers = tool
			assertRefused(t, c.Validate(), tool)
		})
	}
}

func assertRefused(t *testing.T, err error, tool string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Validate accepted %q", tool)
	}
	if !strings.Contains(err.Error(), tool) {
		t.Errorf("error = %q, want it to name %q", err, tool)
	}
	if !strings.Contains(err.Error(), "only claude and opencode") {
		t.Errorf("error = %q, want it to say why", err)
	}
}

func TestRolesOrder(t *testing.T) {
	tests := []struct {
		name   string
		models map[Role]string
		want   []Role
	}{
		{
			name: "declared out of order",
			models: map[Role]string{
				RoleResearcher: "e/f",
				RoleCoder:      "a/b",
				RoleReviewer:   "c/d",
			},
			want: []Role{RoleResearcher, RoleCoder, RoleReviewer},
		},
		{
			name: "all five",
			models: map[Role]string{
				RoleReviewer:   "a",
				RoleTester:     "b",
				RoleCoder:      "c",
				RoleArchitect:  "d",
				RoleResearcher: "e",
			},
			want: []Role{RoleResearcher, RoleArchitect, RoleCoder, RoleTester, RoleReviewer},
		},
		{
			name:   "one role",
			models: map[Role]string{RoleReviewer: "c/d"},
			want:   []Role{RoleReviewer},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{Models: tt.models}
			if got := c.Roles(); !slices.Equal(got, tt.want) {
				t.Errorf("Roles() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateWorkerCount(t *testing.T) {
	base := Config{Lead: Tool{ID: "claude"}, Workers: "claude", MaxTurns: 1, Timeout: time.Minute}
	all := map[Role]string{}
	for _, r := range AllRoles() {
		all[r] = "m"
	}
	c := base
	c.Models = all
	if err := c.Validate(); err != nil {
		t.Errorf("five workers: %v", err)
	}
	c.Models = map[Role]string{}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "team has 0 workers, want 1 to 5") {
		t.Errorf("zero workers error = %v, want 'team has 0 workers, want 1 to 5'", err)
	}
}

func TestRoleLocks(t *testing.T) {
	readOnly := []Role{RoleResearcher, RoleArchitect, RoleReviewer}
	edits := []Role{RoleCoder, RoleTester}
	if got := AllRoles(); !slices.Equal(got, []Role{RoleResearcher, RoleArchitect, RoleCoder, RoleTester, RoleReviewer}) {
		t.Fatalf("AllRoles() = %v", got)
	}
	for _, r := range AllRoles() {
		if !r.Valid() {
			t.Errorf("%s.Valid() = false", r)
		}
		if got, want := r.ReadOnly(), slices.Contains(readOnly, r); got != want {
			t.Errorf("%s.ReadOnly() = %v, want %v", r, got, want)
		}
		if got, want := r.Edits(), slices.Contains(edits, r); got != want {
			t.Errorf("%s.Edits() = %v, want %v", r, got, want)
		}
	}
	if Role("qa").Valid() {
		t.Error(`Role("qa").Valid() = true`)
	}
}
