package team

import (
	"maps"
	"testing"
)

func TestParseTier(t *testing.T) {
	for in, want := range map[string]Tier{"small": TierSmall, "MEDIUM": TierMedium, "Large": TierLarge} {
		if got, ok := ParseTier(in); !ok || got != want {
			t.Errorf("ParseTier(%q) = %q, %v", in, got, ok)
		}
	}
	for _, in := range []string{"huge", ""} {
		if got, ok := ParseTier(in); ok {
			t.Errorf("ParseTier(%q) = %q, want not ok", in, got)
		}
	}
}

func TestDefaultTier(t *testing.T) {
	want := map[Role]Tier{
		RoleResearcher: TierSmall, RoleArchitect: TierLarge, RoleCoder: TierMedium,
		RoleTester: TierMedium, RoleReviewer: TierLarge,
	}
	for r, w := range want {
		if got := DefaultTier(r); got != w {
			t.Errorf("DefaultTier(%s) = %s, want %s", r, got, w)
		}
	}
}

func TestBuiltinModel(t *testing.T) {
	for tier, want := range map[Tier]string{TierSmall: "haiku", TierMedium: "sonnet", TierLarge: "opus"} {
		if got, ok := BuiltinModel("claude", tier); !ok || got != want {
			t.Errorf("BuiltinModel(claude, %s) = %q, %v", tier, got, ok)
		}
		if got, ok := BuiltinModel("opencode", tier); ok {
			t.Errorf("BuiltinModel(opencode, %s) = %q, want none", tier, got)
		}
	}
	if len(AllTiers()) != 3 {
		t.Errorf("AllTiers = %v", AllTiers())
	}
}

func TestResolveOrder(t *testing.T) {
	saved := map[Tier]string{TierMedium: "saved-m"}
	tests := []struct {
		name      string
		r         Resolver
		wantModel string
		wantSrc   TierSource
	}{
		{"flag", Resolver{Tool: "claude", Overrides: map[Tier]string{TierMedium: "flag-m"}, Saved: saved}, "flag-m", "flag"},
		{"saved", Resolver{Tool: "claude", Saved: saved}, "saved-m", "saved"},
		{"built-in", Resolver{Tool: "claude"}, "sonnet", "built-in"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, tier, src, err := tt.r.Resolve("medium")
			if err != nil || m != tt.wantModel || tier != TierMedium || src != tt.wantSrc {
				t.Errorf("Resolve = %q, %q, %q, %v", m, tier, src, err)
			}
		})
	}
	m, tier, _, err := Resolver{Tool: "claude"}.Resolve("Large")
	if err != nil || m != "opus" || tier != TierLarge {
		t.Errorf("Resolve(Large) = %q, %q, %v", m, tier, err)
	}
}

func TestResolveLiteralPassesThrough(t *testing.T) {
	for _, v := range []string{"claude-sonnet-5-5", "openrouter/a=b"} {
		m, tier, src, err := Resolver{Tool: "claude"}.Resolve(v)
		if err != nil || m != v || tier != "" || src != "" {
			t.Errorf("Resolve(%q) = %q, %q, %q, %v", v, m, tier, src, err)
		}
	}
}

func TestResolveUnmapped(t *testing.T) {
	_, _, _, err := Resolver{Tool: "opencode", Overrides: map[Tier]string{}}.Resolve("medium")
	want := `tier "medium" has no model for opencode: run dev container team tier set opencode medium=MODEL, or pass --tier medium=MODEL`
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
	_, _, _, err = Resolver{Tool: "opencode"}.Resolve("medium")
	want = `tier "medium" has no model for opencode: run dev container team tier set opencode medium=MODEL`
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

func TestParseTierOverrides(t *testing.T) {
	got, err := ParseTierOverrides([]string{"medium=x", "LARGE=a=b"})
	if err != nil || !maps.Equal(got, map[Tier]string{TierMedium: "x", TierLarge: "a=b"}) {
		t.Errorf("got %v, %v", got, err)
	}
	for _, bad := range [][]string{{"huge=x"}, {"medium="}, {"medium"}, {"medium=a", "medium=b"}} {
		_, err := ParseTierOverrides(bad)
		if err == nil {
			t.Errorf("ParseTierOverrides(%q) want error", bad)
		}
	}
}
