package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/duy0611/dev-cli/internal/dcgen"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider/local"
	"github.com/duy0611/dev-cli/internal/store"
)

// parseToolList splits a --tools value, dropping empty entries.
func parseToolList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// folderlessMount describes where a folderless container keeps its work. The
// name is the in-container path and half the volume name.
func folderlessMount(workspace, container string) dcgen.Mount {
	return dcgen.Mount{
		Volume: local.VolumeName(workspace, container),
		Folder: local.WorkspaceFolder(container),
	}
}

// isolatedMount describes where an isolated container keeps its primary
// folder's copy: its own volume, at folder.
func isolatedMount(workspace, container, folder string) dcgen.Mount {
	return dcgen.Mount{Volume: local.VolumeName(workspace, container), Folder: folder}
}

// isolatedPath is where a new isolated container's primary appears, the same
// path a guarded one would give it.
func isolatedPath(source string) string {
	return path.Join("/workspaces", filepath.Base(source))
}

// worktreeMount describes a worktree container's two bind mounts, each at its
// host path (invariant 11).
func worktreeMount(repo, path string) dcgen.Mount {
	return dcgen.Mount{Host: path, Bind: repo}
}

// stateFor describes where a container keeps its agents' configuration. Zero
// when it persists none.
func stateFor(persist bool, workspace, container string) dcgen.State {
	if !persist {
		return dcgen.State{}
	}
	return dcgen.State{Volume: local.StateVolumeName(workspace, container)}
}

// generatedConfigFor generates a configuration when told to, asks at a
// terminal, and otherwise returns nothing. A scripted run must not block on a
// question.
func generatedConfigFor(name string, generate bool, tools []string, mount dcgen.Mount, state dcgen.State, in *os.File, out io.Writer) (string, error) {
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
				// A cancelled question is not an answer, so nothing is written.
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
	// Say what was added: silently installing an unrequested tool is a
	// surprise.
	for _, id := range resolved {
		if !slices.Contains(tools, id) {
			fmt.Fprintf(out, "adding %s, which the selection requires\n", id)
		}
	}

	config, err := dcgen.Render(name, resolved, mount, state)
	if err != nil {
		return "", usageError(err)
	}
	return config, nil
}

// applyToolDiff applies a +/- diff to the current set, or replaces it when no
// entry is prefixed.
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

// applyFrom points opts at another container's tools and persisted choices, as
// --from asks. Only the tool list is copied, never the document: it bakes in
// names and volumes.
func applyFrom(a *app, wsName string, opts *createOpts) error {
	if opts.from == "" {
		return nil
	}
	// A plain list would replace the inherited tools, so it is refused.
	for _, s := range opts.tools {
		if !strings.HasPrefix(s, "+") && !strings.HasPrefix(s, "-") {
			return usageErrorf("with --from, --tools takes +/- changes to %s's tools", opts.from)
		}
	}

	st, err := a.store()
	if err != nil {
		return err
	}
	src, err := st.GetContainer(wsName, opts.from)
	if errors.Is(err, store.ErrNotFound) {
		return notFoundErrorf("no such container: %s (workspace %s)", opts.from, wsName)
	}
	if err != nil {
		return err
	}
	if src.GeneratedConfig == "" {
		// A project's own config cannot be copied: a relative dockerfile would
		// stay anchored to that project.
		return usageErrorf("container %s uses its project's own config; --from copies only generated ones",
			opts.from)
	}

	tools, err := dcgen.ToolsOf(src.GeneratedConfig)
	if err != nil {
		// A corrupt row is exit 1, not a malformed request.
		return fmt.Errorf("container %s: %w", opts.from, err)
	}
	// An empty spec would install none of the source's tools.
	if len(opts.tools) > 0 {
		if tools, err = applyToolDiff(tools, opts.tools); err != nil {
			return err
		}
	}
	opts.tools = tools
	// --from names the tools, so the picker must not open.
	opts.generate = true

	// --no-persist-state can only turn persistence off, so a source without it
	// passes that on.
	if !src.PersistState {
		opts.noPersistState = true
	}
	if opts.agentConfig == "" && !opts.noAgentConfig {
		// "" means the project's own agents.yaml, which for the new container
		// is the new folder's.
		switch src.AgentConfig {
		case agentConfigNone:
			opts.noAgentConfig = true
		default:
			opts.agentConfig = src.AgentConfig
		}
	}
	return nil
}

// rewriteGeneratedTools changes a generated container's tools. It reads them
// back from the stored document, so a hand-edited configuration still works.
func rewriteGeneratedTools(ctx context.Context, a *app, workspace, name string, spec []string) error {
	t, err := a.resolve(ctx, workspace, name)
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

	config, err := renderGenerated(t.container, next)
	if err != nil {
		return err
	}

	st, err := a.store()
	if err != nil {
		return err
	}
	return st.UpdateContainerConfig(t.workspace.Name, name, config)
}

// renderGenerated re-renders c's generated document with tools, deriving the
// mount and state from c's columns.
func renderGenerated(c model.Container, tools []string) (string, error) {
	// Derived from the rows, never a flag: a rebuild must not change what the
	// container is mounted on.
	var mount dcgen.Mount
	switch c.SourceKind {
	case model.SourceNone:
		mount = folderlessMount(c.WorkspaceName, c.Name)
	default:
		// From the stored column, not the checkout on disk, which may have been
		// deleted.
		switch {
		case c.Mode == model.ModeIsolated:
			mount = isolatedMount(c.WorkspaceName, c.Name, local.WorkspaceFolderOf(c))
		case c.WorktreeRepo != "":
			mount = worktreeMount(c.WorktreeRepo, c.Source)
		}
	}
	// Derived from the stored column, since this re-renders the whole document
	// and a state mount kept only in the JSON would be dropped.
	state := stateFor(c.PersistState, c.WorkspaceName, c.Name)
	config, err := dcgen.Render(c.Name, tools, mount, state)
	if err != nil {
		return "", usageError(err)
	}
	return config, nil
}
