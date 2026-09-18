package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/dcgen"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/model"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/provider/local"
)

// parseToolList splits a --tools value. Empty entries are dropped rather than
// rejected, so a trailing comma is not a usage error.
func parseToolList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// folderlessMount describes where a folderless container keeps its work.
//
// The container name is the in-container path as well as half the volume name,
// so that two containers in one workspace never share a workspace directory
// and `docker volume ls` reads as the container list.
func folderlessMount(workspace, container string) dcgen.Mount {
	return dcgen.Mount{
		Volume: local.VolumeName(workspace, container),
		Folder: "/workspaces/" + container,
	}
}

// generatedConfigFor decides whether to generate a configuration and renders it.
//
// Three paths, matching `provider configure`: told to, so do it; not told but
// at a terminal, so ask; neither, so return nothing and let the caller report
// the missing configuration. The scripted run must never block on a question
// nobody will see.
func generatedConfigFor(name string, generate bool, tools []string, mount dcgen.Mount, in *os.File, out io.Writer) (string, error) {
	if !generate {
		if !isTerminal(in) {
			return "", nil // the caller reports the missing configuration
		}
		if !confirm(in, out, "generate a base Ubuntu devcontainer?") {
			return "", nil
		}
		picked, err := pickTools(catalogItems(dcgen.Catalog()))
		if err != nil {
			if errors.Is(err, errPickCancelled) {
				// Nothing is written: a cancelled question is not an answer.
				return "", errors.New("cancelled")
			}
			return "", err
		}
		tools = picked
	}

	resolved, err := dcgen.Resolve(tools)
	if err != nil {
		return "", usageError(err)
	}
	// Say what was added on the operator's behalf. Silently installing a tool
	// they did not ask for is the kind of surprise that gets noticed months
	// later, in an image nobody can explain.
	for _, id := range resolved {
		if !slices.Contains(tools, id) {
			fmt.Fprintf(out, "adding %s, which the selection requires\n", id)
		}
	}

	config, err := dcgen.Render(name, resolved, mount)
	if err != nil {
		return "", usageError(err)
	}
	return config, nil
}

// applyToolDiff works out the new tool set.
//
// Every entry prefixed + or - adjusts the current set; entries with no prefix
// replace it wholesale. Mixing the two forms is refused rather than guessed at:
// "node,+yq" reads as one intent and means another.
func applyToolDiff(current, spec []string) ([]string, error) {
	var diffs, plain int
	for _, s := range spec {
		if strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") {
			diffs++
		} else {
			plain++
		}
	}
	if diffs > 0 && plain > 0 {
		return nil, usageErrorf("--tools takes either a list or +/- changes, not both")
	}
	if diffs == 0 {
		resolved, err := dcgen.Resolve(spec)
		if err != nil {
			return nil, usageError(err)
		}
		return resolved, nil
	}

	set := map[string]bool{}
	for _, id := range current {
		set[id] = true
	}
	for _, s := range spec {
		id := s[1:]
		if id == "" {
			return nil, usageErrorf("--tools: %q names no tool", s)
		}
		if s[0] == '+' {
			set[id] = true
		} else {
			delete(set, id)
		}
	}

	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	resolved, err := dcgen.Resolve(out)
	if err != nil {
		return nil, usageError(err)
	}
	return resolved, nil
}

// rewriteGeneratedTools changes which tools a generated container installs.
//
// Reads the current set back out of the stored document rather than from a
// column of its own, so a configuration edited by hand is still something this
// can reason about.
func rewriteGeneratedTools(a *app, workspace, name string, spec []string) error {
	t, err := a.resolve(workspace, name)
	if err != nil {
		return err
	}
	defer t.release()

	if t.container.GeneratedConfig == "" {
		return usageErrorf("container %s uses the project's own config; edit %s instead",
			name, t.container.ConfigPath)
	}

	current, err := dcgen.ToolsOf(t.container.GeneratedConfig)
	if err != nil {
		return err
	}
	next, err := applyToolDiff(current, spec)
	if err != nil {
		return err
	}

	// Derived from the stored row, not from a flag: a rebuild must not be able
	// to change what a container is mounted on. A folder container renders the
	// zero Mount, which is the CLI's own bind mount.
	var mount dcgen.Mount
	if t.container.SourceKind == model.SourceNone {
		mount = folderlessMount(t.workspace.Name, name)
	}
	config, err := dcgen.Render(name, next, mount)
	if err != nil {
		return usageError(err)
	}

	st, err := a.store()
	if err != nil {
		return err
	}
	return st.UpdateContainerConfig(t.workspace.Name, name, config)
}
