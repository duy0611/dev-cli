// Package team holds an agent team's config, worker adapters and run folder.
package team

import (
	"fmt"
	"strings"
	"time"
)

// Role is one of the built-in worker roles.
type Role string

const (
	RoleCoder      Role = "coder"
	RoleReviewer   Role = "reviewer"
	RoleResearcher Role = "researcher"
)

// Tool is an agent CLI, optionally pinned to a model.
type Tool struct {
	ID    string `json:"id"`
	Model string `json:"model,omitempty"`
}

// Config is the team a launch was asked for, as written to <run>/config.json.
type Config struct {
	Lead     Tool            `json:"lead"`
	Workers  string          `json:"workers"`
	Models   map[Role]string `json:"models"`
	MaxTurns int             `json:"max_turns"`
	Timeout  time.Duration   `json:"timeout"`
}

const (
	DefaultMaxTurns = 30
	DefaultTimeout  = 30 * time.Minute
)

var roleOrder = []Role{RoleCoder, RoleReviewer, RoleResearcher}

func (r Role) valid() bool {
	return r == RoleCoder || r == RoleReviewer || r == RoleResearcher
}

// ParseWorkers parses repeated --worker ROLE=MODEL values. The model is passed
// through untouched, so a value may itself contain '='.
func ParseWorkers(specs []string) (map[Role]string, error) {
	models := make(map[Role]string, len(specs))
	for _, spec := range specs {
		name, model, ok := strings.Cut(spec, "=")
		if !ok {
			return nil, fmt.Errorf("invalid --worker %q: want ROLE=MODEL", spec)
		}
		if name == "" {
			return nil, fmt.Errorf("invalid --worker %q: role is empty", spec)
		}
		if model == "" {
			return nil, fmt.Errorf("invalid --worker %q: model is empty", spec)
		}
		role := Role(name)
		if !role.valid() {
			return nil, fmt.Errorf("invalid --worker %q: unknown role %q", spec, name)
		}
		if _, repeated := models[role]; repeated {
			return nil, fmt.Errorf("invalid --worker %q: role %q given more than once", spec, name)
		}
		models[role] = model
	}
	return models, nil
}

// Validate rejects a config the dispatch engine cannot run. Zero limits are
// rejected rather than defaulted here: the CLI fills in defaults first, so
// config.json records the limits the run actually used.
func (c Config) Validate() error {
	if err := validateTool("lead", c.Lead.ID); err != nil {
		return err
	}
	if err := validateTool("worker", c.Workers); err != nil {
		return err
	}
	if n := len(c.Models); n < 1 || n > 3 {
		return fmt.Errorf("team has %d workers, want 1 to 3", n)
	}
	for role := range c.Models {
		if !role.valid() {
			return fmt.Errorf("unknown worker role %q", role)
		}
	}
	if c.MaxTurns <= 0 {
		return fmt.Errorf("worker max turns must be positive, got %d", c.MaxTurns)
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("worker timeout must be positive, got %s", c.Timeout)
	}
	return nil
}

func validateTool(kind, id string) error {
	switch id {
	case "claude", "opencode":
		return nil
	}
	return fmt.Errorf("%s tool %q cannot join a team: only claude and opencode run unattended workers", kind, id)
}

// Roles returns the roles present in the team, in the fixed coder, reviewer,
// researcher order.
func (c Config) Roles() []Role {
	roles := make([]Role, 0, len(c.Models))
	for _, role := range roleOrder {
		if _, present := c.Models[role]; present {
			roles = append(roles, role)
		}
	}
	return roles
}
