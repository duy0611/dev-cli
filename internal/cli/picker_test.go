package cli

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/dcgen"
)

// Escape sequences arrive as whole reads most of the time and split across two
// reads sometimes, which is the bug this table exists to prevent: a decoder
// that assumes three bytes are present treats a lone ESC as an arrow key.
func TestDecodeKey(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want key
		n    int
	}{
		{"up", "\x1b[A", keyUp, 3},
		{"down", "\x1b[B", keyDown, 3},
		{"space toggles", " ", keyToggle, 1},
		{"return accepts", "\r", keyAccept, 1},
		{"newline accepts too", "\n", keyAccept, 1},
		{"ctrl-c cancels", "\x03", keyCancel, 1},
		{"q cancels", "q", keyCancel, 1},
		{"partial escape waits", "\x1b", keyNone, 0},
		{"partial bracket waits", "\x1b[", keyNone, 0},
		{"unknown byte is skipped", "z", keyNone, 1},
		{"nothing to read", "", keyNone, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, n := decodeKey([]byte(tt.in))
			if got != tt.want || n != tt.n {
				t.Errorf("decodeKey(%q) = (%v, %d), want (%v, %d)", tt.in, got, n, tt.want, tt.n)
			}
		})
	}
}

func testItems() []pickItem {
	return []pickItem{
		{ID: "node", Summary: "Node.js", Note: "official"},
		{ID: "gh", Summary: "GitHub CLI", Note: "official"},
		{ID: "yq", Summary: "yq", Note: "community"},
	}
}

func TestMultiSelectTogglesAndAccepts(t *testing.T) {
	// Toggle the first, move down twice, toggle the third, accept.
	in := strings.NewReader(" \x1b[B\x1b[B \r")
	got, err := multiSelect(testItems(), in, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("multiSelect: %v", err)
	}
	if !slices.Equal(got, []string{"node", "yq"}) {
		t.Errorf("selected %v, want [node yq]", got)
	}
}

// Selecting nothing is a real answer: a bare Ubuntu box.
func TestMultiSelectAcceptsAnEmptySelection(t *testing.T) {
	got, err := multiSelect(testItems(), strings.NewReader("\r"), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("multiSelect: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("selected %v, want nothing", got)
	}
}

func TestMultiSelectTogglesOff(t *testing.T) {
	got, err := multiSelect(testItems(), strings.NewReader("  \r"), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("multiSelect: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("selected %v after toggling the same item twice, want nothing", got)
	}
}

func TestMultiSelectCancels(t *testing.T) {
	_, err := multiSelect(testItems(), strings.NewReader(" \x03"), &bytes.Buffer{})
	if !errors.Is(err, errPickCancelled) {
		t.Errorf("err = %v, want errPickCancelled", err)
	}
}

// The cursor must not walk off either end: an index out of range here is a
// panic in the middle of an interactive prompt.
func TestMultiSelectClampsTheCursor(t *testing.T) {
	// Up past the top, then toggle: still the first item.
	got, err := multiSelect(testItems(), strings.NewReader("\x1b[A\x1b[A \r"), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("multiSelect: %v", err)
	}
	if !slices.Equal(got, []string{"node"}) {
		t.Errorf("selected %v, want [node]", got)
	}

	// Down past the bottom, then toggle: still the last item.
	got, err = multiSelect(testItems(), strings.NewReader("\x1b[B\x1b[B\x1b[B\x1b[B \r"), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("multiSelect: %v", err)
	}
	if !slices.Equal(got, []string{"yq"}) {
		t.Errorf("selected %v, want [yq]", got)
	}
}

// End of input without an accept is the operator's terminal going away. It must
// not be read as "they accepted an empty list" and silently build something.
func TestMultiSelectTreatsEOFAsCancellation(t *testing.T) {
	_, err := multiSelect(testItems(), strings.NewReader(" "), &bytes.Buffer{})
	if !errors.Is(err, errPickCancelled) {
		t.Errorf("err = %v, want errPickCancelled", err)
	}
}

// The catalog rows have to say which references are the devcontainers
// project's own and which are somebody's community image, because pulling the
// latter is a decision rather than a default.
func TestCatalogItemsMarkTheSource(t *testing.T) {
	items := catalogItems(dcgen.Catalog())

	byID := map[string]pickItem{}
	for _, it := range items {
		byID[it.ID] = it
	}
	if got := byID["node"].Note; got != "official" {
		t.Errorf("node note = %q, want official", got)
	}
	if got := byID["gcloud"].Note; got != "community" {
		t.Errorf("gcloud note = %q, want community", got)
	}
}
