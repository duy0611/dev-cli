package agent

import (
	"fmt"
	"maps"
	"slices"

	"github.com/duy0611/dev-cli/internal/agentcfg"
)

// Step is one thing to do inside a container to apply an agents.yaml. Exactly
// one of Cmd or File is set.
type Step struct {
	// Desc names the step in progress output and in errors.
	Desc string
	// Cmd runs, with Stdin on its standard input when non-nil.
	Cmd   []string
	Stdin []byte
	// Check runs before Cmd; if Done accepts its stdout, Cmd is skipped. It
	// runs again after Cmd, and a false answer fails the step even when Cmd
	// exited 0.
	Check []string
	Done  func(stdout []byte) bool
	// File is a shell expression for the file that Edit rewrites. An absent
	// state volume falls back to the agent's default location. A missing file
	// reads as empty.
	File string
	Edit func(current []byte) ([]byte, error)
}

// Exec runs a command inside the container and returns its stdout. Errors
// should carry stderr.
type Exec func(cmd []string, stdin []byte) (stdout []byte, err error)

// Run carries out one step.
func Run(s Step, run Exec) error {
	if s.Check != nil {
		// A failing check is not fatal: the list it reads may not exist yet.
		if out, err := run(s.Check, nil); err == nil && s.Done(out) {
			return nil
		}
	}
	if s.File != "" {
		cur, err := run(readFileCmd(s.File), nil)
		if err != nil {
			return fmt.Errorf("%s: reading: %w", s.Desc, err)
		}
		next, err := s.Edit(cur)
		if err != nil {
			return fmt.Errorf("%s: %w", s.Desc, err)
		}
		if _, err := run(writeFileCmd(s.File), next); err != nil {
			return fmt.Errorf("%s: writing: %w", s.Desc, err)
		}
		return nil
	}
	if _, err := run(s.Cmd, s.Stdin); err != nil {
		return fmt.Errorf("%s: %w", s.Desc, err)
	}
	if s.Check != nil {
		if out, err := run(s.Check, nil); err != nil || !s.Done(out) {
			return fmt.Errorf("%s: the command succeeded but the change is not visible afterwards", s.Desc)
		}
	}
	return nil
}

// readFileCmd and writeFileCmd splice File in unquoted, for ${VAR:-default} to
// expand. That is safe only because adapters build File from constants. The
// assignment form avoids word splitting on a $HOME with spaces.
func readFileCmd(file string) []string {
	return []string{"sh", "-c", `f=` + file + `; if [ -f "$f" ]; then cat "$f"; fi`}
}

// The write goes to a temporary and is renamed over the file, so an exec that
// dies mid-stream leaves the old file whole. A truncated one would silently
// drop the operator's settings on the next merge.
func writeFileCmd(file string) []string {
	return []string{"sh", "-c", `f=` + file + `; mkdir -p "$(dirname "$f")" && ` +
		`cat > "$f.dev-tmp" && mv -f "$f.dev-tmp" "$f"`}
}

// Apply runs every configurable agent's steps for spec. An agent the container
// lacks is skipped with a warning, since one file serves containers with
// different tool sets.
func Apply(spec *agentcfg.Spec, run Exec, progress, warn func(string)) error {
	for _, ag := range Configurable() {
		v, ok := spec.View(ag.ID)
		if !ok {
			continue
		}
		// A shell builtin, so it works where which(1) is absent.
		if _, err := run([]string{"sh", "-c", "command -v " + ag.Binary}, nil); err != nil {
			warn(fmt.Sprintf("agents.yaml: %s is not installed in this container; skipping it", ag.ID))
			continue
		}
		steps, err := ag.Configure(v)
		if err != nil {
			return err
		}
		for _, s := range steps {
			progress(s.Desc)
			if err := Run(s, run); err != nil {
				return err
			}
		}
	}
	return nil
}

// sortedKeys returns a map's keys in order, so steps come out the same every
// run.
func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
