package agent

import (
	"fmt"
	"maps"
	"slices"

	"github.com/duy0611/dev-cli/internal/agentcfg"
)

// Step is one thing to do inside a container to apply an agents.yaml.
//
// Plain data rather than a function that runs itself, so that an adapter is
// tested by looking at what it would do, without a container or a stub. Exactly
// one of Cmd or File is set.
type Step struct {
	// Desc names the step in progress output and in the error when it fails.
	Desc string
	// Cmd runs with Stdin on its standard input when Stdin is non-nil.
	Cmd   []string
	Stdin []byte
	// Check runs before Cmd; if Done says its stdout shows the work is
	// already there, Cmd is skipped. It runs again after Cmd, and a false
	// answer then fails the step: a command that exits 0 without doing what
	// it was asked is still a failure.
	Check []string
	Done  func(stdout []byte) bool
	// File and Edit make the step a read-modify-write of one file. File is a
	// shell expression — "${OPENCODE_CONFIG_DIR:-$HOME/.config/opencode}/…" —
	// so a container without the state volume gets the agent's default
	// location with no branch in dev. A file that does not exist reads as
	// empty.
	File string
	Edit func(current []byte) ([]byte, error)
}

// Exec runs a command inside the container and returns its stdout. An error
// should carry the command's stderr, since it is what the operator will read.
type Exec func(cmd []string, stdin []byte) (stdout []byte, err error)

// Run carries out one step.
func Run(s Step, run Exec) error {
	if s.Check != nil {
		// A failing check is not fatal: the list it reads may not exist yet.
		// Cmd runs, and the check afterwards is the one that has to pass.
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

// readFileCmd and writeFileCmd splice File in unquoted, which is safe only
// because File is always built by an adapter from constants and never from
// agents.yaml: it has to be unquoted for ${VAR:-default} to expand. The
// assignment is not word-split, so a $HOME with spaces still works.
func readFileCmd(file string) []string {
	return []string{"sh", "-c", `f=` + file + `; if [ -f "$f" ]; then cat "$f"; fi`}
}

// The write goes to a temporary beside the file and is renamed over it, so an
// exec that dies mid-stream leaves the old file whole. A truncated one would
// be read back by the retry as the starting point of the next merge, silently
// dropping every setting the operator had in it.
func writeFileCmd(file string) []string {
	return []string{"sh", "-c", `f=` + file + `; mkdir -p "$(dirname "$f")" && ` +
		`cat > "$f.dev-tmp" && mv -f "$f.dev-tmp" "$f"`}
}

// Apply runs every configurable agent's steps for spec. An agent the file
// reaches but the container does not have is a warning and a skip: one file
// has to work across containers with different tool sets. Anything that fails
// after that stops the apply.
func Apply(spec *agentcfg.Spec, run Exec, progress, warn func(string)) error {
	for _, ag := range Configurable() {
		v, ok := spec.View(ag.ID)
		if !ok {
			continue
		}
		// `command -v`, the check start-agent makes: a shell builtin, so it
		// works in images that ship no which(1).
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

// sortedKeys orders a map's keys, so the steps built from it come out the same
// on every run.
func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
