package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestAskUsesTheDefaultOnAnEmptyAnswer(t *testing.T) {
	var out bytes.Buffer
	p := newPrompter(strings.NewReader("\n"), &out)

	got, err := p.ask("namespace", "default")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if got != "default" {
		t.Errorf("ask = %q, want the default", got)
	}
	// The default has to be visible, or pressing return is a guess.
	if !strings.Contains(out.String(), "[default]") {
		t.Errorf("prompt %q does not show the default", out.String())
	}
}

func TestAskTakesTheAnswer(t *testing.T) {
	p := newPrompter(strings.NewReader("  sandboxes  \n"), &bytes.Buffer{})

	got, err := p.ask("namespace", "default")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if got != "sandboxes" {
		t.Errorf("ask = %q, want the trimmed answer", got)
	}
}

// A piped answer often has no trailing newline; treating that as a failure
// would break every scripted use.
func TestAskAcceptsEOFWithoutANewline(t *testing.T) {
	p := newPrompter(strings.NewReader("sandboxes"), &bytes.Buffer{})

	got, err := p.ask("namespace", "default")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if got != "sandboxes" {
		t.Errorf("ask = %q", got)
	}
}

func TestAskOnEOFTakesTheDefault(t *testing.T) {
	p := newPrompter(strings.NewReader(""), &bytes.Buffer{})

	got, err := p.ask("namespace", "default")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if got != "default" {
		t.Errorf("ask = %q, want the default", got)
	}
}

// Some settings have no workable default, and accepting an empty answer would
// store a provider that cannot work.
func TestAskRequiredRepeatsUntilAnswered(t *testing.T) {
	var out bytes.Buffer
	p := newPrompter(strings.NewReader("\n\nreg.example/dev\n"), &out)

	got, err := p.askRequired("registry", "")
	if err != nil {
		t.Fatalf("askRequired: %v", err)
	}
	if got != "reg.example/dev" {
		t.Errorf("askRequired = %q", got)
	}
	if n := strings.Count(out.String(), "is required"); n != 2 {
		t.Errorf("said %q %d times, want 2", "is required", n)
	}
}
