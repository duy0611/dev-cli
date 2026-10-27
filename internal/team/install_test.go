package team

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"
)

// wantInstallScript is the constant the shell runs. The target path is data in
// argv, never part of this text.
const wantInstallScript = `mkdir -p "$(dirname "$1")" && cat > "$1.part" && chmod 700 "$1.part" && mv "$1.part" "$1"`

func TestInstallAtomicCopy(t *testing.T) {
	const path = "/var/dev-state/dev-team/bin/dev-team"
	bin := []byte("\x7fELF the dispatch binary")

	var (
		calls int
		argv  []string
		stdin []byte
	)
	run := func(_ context.Context, command []string, in io.Reader) error {
		calls++
		argv = command
		if in != nil {
			stdin, _ = io.ReadAll(in)
		}
		return nil
	}

	if err := Install(t.Context(), run, path, bin); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if calls != 1 {
		t.Fatalf("Install ran %d commands, want one", calls)
	}
	if !bytes.Equal(stdin, bin) {
		t.Errorf("stdin = %q, want the binary", stdin)
	}
	if want := []string{"sh", "-c", wantInstallScript, "sh", path}; !slices.Equal(argv, want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}

	script := argv[2]
	// The copy is written to .part and moved, so a container cannot run half a
	// binary at the final path.
	mkdir := strings.Index(script, "mkdir -p")
	copyBin := strings.Index(script, "cat >")
	chmod := strings.Index(script, "chmod 700")
	move := strings.Index(script, "mv ")
	if mkdir < 0 || copyBin < 0 || chmod < 0 || move < 0 {
		t.Fatalf("script lacks a step: %s", script)
	}
	if mkdir > copyBin || copyBin > chmod || chmod > move {
		t.Errorf("steps are out of order: %s", script)
	}
	if strings.Contains(script, path) {
		t.Errorf("script names the target path: %s", script)
	}
}

func TestInstallPathIsArgumentNotScript(t *testing.T) {
	// Spaces, a quote and a command substitution would all change meaning if
	// the path were spliced into the script.
	const path = "/tmp/dev team/it's $(touch pwned)/dev-team"
	bin := []byte("binary")

	var argv []string
	run := func(_ context.Context, command []string, _ io.Reader) error {
		argv = command
		return nil
	}

	if err := Install(t.Context(), run, path, bin); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if want := []string{"sh", "-c", wantInstallScript, "sh", path}; !slices.Equal(argv, want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
	for _, part := range []string{"dev team", "it's", "$(touch", "pwned"} {
		if strings.Contains(argv[2], part) {
			t.Errorf("script contains path fragment %q: %s", part, argv[2])
		}
	}
	for i, arg := range argv {
		if i != 4 && strings.Contains(arg, path) {
			t.Errorf("argv[%d] repeats the target path: %q", i, arg)
		}
	}
}
