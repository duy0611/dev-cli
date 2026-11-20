package team

import (
	"fmt"
	"strings"
)

// Tier is a model size a worker or lead can be asked for instead of a model name.
type Tier string

const (
	TierSmall  Tier = "small"
	TierMedium Tier = "medium"
	TierLarge  Tier = "large"
)

// AllTiers returns the tiers from smallest to largest.
func AllTiers() []Tier { return []Tier{TierSmall, TierMedium, TierLarge} }

// ParseTier reads a tier name, ignoring case.
func ParseTier(s string) (Tier, bool) {
	t := Tier(strings.ToLower(s))
	for _, known := range AllTiers() {
		if t == known {
			return t, true
		}
	}
	return "", false
}

// DefaultTier is the tier a role gets when --worker names it without a model.
func DefaultTier(r Role) Tier {
	switch r {
	case RoleResearcher:
		return TierSmall
	case RoleArchitect, RoleReviewer:
		return TierLarge
	}
	return TierMedium
}

// BuiltinModel is the model a tool ships for a tier; only claude has one.
func BuiltinModel(tool string, t Tier) (string, bool) {
	if tool != "claude" {
		return "", false
	}
	switch t {
	case TierSmall:
		return "haiku", true
	case TierMedium:
		return "sonnet", true
	case TierLarge:
		return "opus", true
	}
	return "", false
}

// ParseTierOverrides parses repeated --tier TIER=MODEL values.
func ParseTierOverrides(specs []string) (map[Tier]string, error) {
	out := make(map[Tier]string, len(specs))
	for _, spec := range specs {
		name, model, ok := strings.Cut(spec, "=")
		if !ok {
			return nil, fmt.Errorf("invalid --tier %q: want TIER=MODEL", spec)
		}
		tier, ok := ParseTier(name)
		if !ok {
			return nil, fmt.Errorf("invalid --tier %q: unknown tier %q", spec, name)
		}
		if model == "" {
			return nil, fmt.Errorf("invalid --tier %q: model is empty", spec)
		}
		if _, repeated := out[tier]; repeated {
			return nil, fmt.Errorf("invalid --tier %q: tier %q given more than once", spec, name)
		}
		out[tier] = model
	}
	return out, nil
}

// TierSource says where a tier's model came from.
type TierSource string

const (
	SourceFlag    TierSource = "flag"
	SourceSaved   TierSource = "saved"
	SourceBuiltin TierSource = "built-in"
)

// Resolver turns a --worker or --lead-model value into a model.
type Resolver struct {
	Tool      string
	Overrides map[Tier]string // from --tier; nil for the lead
	Saved     map[Tier]string
}

// Resolve returns the model for value. A value that is not a tier comes back
// unchanged with tier and source empty.
func (r Resolver) Resolve(value string) (model string, tier Tier, source TierSource, err error) {
	tier, ok := ParseTier(value)
	if !ok {
		return value, "", "", nil
	}
	if m := r.Overrides[tier]; m != "" {
		return m, tier, SourceFlag, nil
	}
	if m := r.Saved[tier]; m != "" {
		return m, tier, SourceSaved, nil
	}
	if m, ok := BuiltinModel(r.Tool, tier); ok {
		return m, tier, SourceBuiltin, nil
	}
	msg := fmt.Sprintf("tier %q has no model for %s: run dev container team tier set %s %s=MODEL", tier, r.Tool, r.Tool, tier)
	if r.Overrides != nil {
		msg += fmt.Sprintf(", or pass --tier %s=MODEL", tier)
	}
	return "", "", "", fmt.Errorf("%s", msg)
}
