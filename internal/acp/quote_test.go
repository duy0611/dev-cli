package acp

import "testing"

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"":      `''`,
		"abc":   `'abc'`,
		"it's":  `'it'\''s'`,
		"a b$c": `'a b$c'`,
	} {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}
