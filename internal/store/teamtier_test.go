package store

import (
	"errors"
	"maps"
	"testing"
)

func TestTeamTiers(t *testing.T) {
	s := openTest(t)

	if got, err := s.ListTeamTiers("claude"); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("unknown tool: got %v, %v; want empty map, nil", got, err)
	}

	for _, c := range [][3]string{
		{"claude", "fast", "haiku"},
		{"claude", "smart", "opus"},
		{"opencode", "fast", "other"},
	} {
		if err := s.SetTeamTier(c[0], c[1], c[2]); err != nil {
			t.Fatalf("SetTeamTier: %v", err)
		}
	}
	got, err := s.ListTeamTiers("claude")
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"fast": "haiku", "smart": "opus"}; !maps.Equal(got, want) {
		t.Errorf("claude tiers = %v, want %v", got, want)
	}

	if err := s.SetTeamTier("claude", "fast", "haiku-2"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListTeamTiers("claude")
	if got["fast"] != "haiku-2" || len(got) != 2 {
		t.Errorf("after replace = %v", got)
	}

	if err := s.UnsetTeamTier("claude", "fast"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListTeamTiers("claude")
	if _, ok := got["fast"]; ok || len(got) != 1 {
		t.Errorf("after unset = %v", got)
	}
	other, _ := s.ListTeamTiers("opencode")
	if other["fast"] != "other" {
		t.Errorf("other tool changed: %v", other)
	}

	if err := s.UnsetTeamTier("claude", "fast"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unset absent = %v, want ErrNotFound", err)
	}
}
