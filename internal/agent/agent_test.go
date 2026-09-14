package agent

import (
	"strings"
	"testing"
)

func TestLookup(t *testing.T) {
	got, err := Lookup("claude")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.Binary != "claude" {
		t.Errorf("Binary = %q, want %q", got.Binary, "claude")
	}

	// Tolerant of the shapes a shell hands over.
	for _, id := range []string{"CLAUDE", " claude "} {
		if _, err := Lookup(id); err != nil {
			t.Errorf("Lookup(%q) = %v, want it to resolve", id, err)
		}
	}
}

func TestLookupUnknownListsTheChoices(t *testing.T) {
	_, err := Lookup("nope")
	if err == nil {
		t.Fatal("Lookup of an unknown agent succeeded")
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("error %q does not list the known agents", err)
	}
}

func TestCommandAppendsExtraArgs(t *testing.T) {
	a, err := Lookup("claude")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	got := a.Command([]string{"--resume", "last"})
	want := []string{"claude", "--resume", "last"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("Command = %v, want %v", got, want)
	}

	if got := a.Command(nil); len(got) != 1 || got[0] != "claude" {
		t.Errorf("Command(nil) = %v, want just the binary", got)
	}
}

// Herdr classifies a pane by this variable, so every agent has to carry it.
func TestEnvCarriesTheAgentID(t *testing.T) {
	for _, id := range IDs() {
		a, err := Lookup(id)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", id, err)
		}
		if got := a.Env()["HERDR_AGENT"]; got != id {
			t.Errorf("%s: HERDR_AGENT = %q, want %q", id, got, id)
		}
	}
}

func TestIDsAreSorted(t *testing.T) {
	ids := IDs()
	if len(ids) == 0 {
		t.Fatal("no agents registered")
	}
	for i := 1; i < len(ids); i++ {
		if ids[i-1] > ids[i] {
			t.Fatalf("IDs are not sorted: %v", ids)
		}
	}
}
