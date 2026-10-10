package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/duy0611/dev-cli/internal/dcgen"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider/local"
	"github.com/duy0611/dev-cli/internal/store"
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

// worktreeMount describes how a worktree container is mounted.
//
// Two bind mounts, each at the identical host path. The checkout's .git file
// holds `gitdir: <repo>/worktrees/<name>` and the repository holds a backlink
// to the checkout — both absolute host paths, and `git gc --auto` prunes the
// registration when the backlink does not resolve.
func worktreeMount(repo, path string) dcgen.Mount {
	return dcgen.Mount{Host: path, Bind: repo}
}

// stateFor describes where a container keeps its agents' configuration.
//
// The zero value when the container does not persist state, which renders a
// document with no mounts and no containerEnv.
func stateFor(persist bool, workspace, container string) dcgen.State {
	if !persist {
		return dcgen.State{}
	}
	return dcgen.State{Volume: local.StateVolumeName(workspace, container)}
}

// generatedConfigFor decides whether to generate a configuration and renders it.
//
// Three paths, matching `provider configure`: told to, so do it; not told but
// at a terminal, so ask; neither, so return nothing and let the caller report
// the missing configuration. The scripted run must never block on a question
// nobody will see.
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

	config, err := dcgen.Render(name, resolved, mount, state)
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

// applyFrom rewrites opts so that the create following it renders from another
// container's tools and inherits its persisted choices, as --from asks.
//
// The tool list is what is copied, never the document: dcgen.Render bakes the
// container's name, its workspace volume, its state volume and any worktree
// binds into it, so a copy would mount the source's volumes into the new
// container. The caller renders afresh for the new name instead, the route
// rewriteGeneratedTools takes.
//
// Every check here runs before the caller writes anything — for worktree
// create, before git makes a checkout — so a mistyped source costs nothing to
// undo. The source row is only read.
func applyFrom(a *app, wsName string, opts *createOpts) error {
	if opts.from == "" {
		return nil
	}
	// applyToolDiff treats a plain list as a replacement, so allowing one would
	// silently discard everything --from exists to carry.
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
		// Pointing at another project's file would leave a relative dockerfile
		// anchored to that project; copying it would freeze a document dev does
		// not own. Neither is worth doing silently.
		return usageErrorf("container %s uses its project's own config; --from copies only generated ones",
			opts.from)
	}

	tools, err := dcgen.ToolsOf(src.GeneratedConfig)
	if err != nil {
		// A corrupt row rather than a malformed request, so exit 1.
		return fmt.Errorf("container %s: %w", opts.from, err)
	}
	// Only when changes were given: applyToolDiff resolves an empty spec to an
	// empty list, which would install none of the source's tools.
	if len(opts.tools) > 0 {
		if tools, err = applyToolDiff(tools, opts.tools); err != nil {
			return err
		}
	}
	opts.tools = tools
	// Always generate, so the picker never opens: --from has already said
	// which tools.
	opts.generate = true

	// Inherited unless a flag said otherwise. --no-persist-state can only turn
	// persistence off, so a source without it passes that on.
	if !src.PersistState {
		opts.noPersistState = true
	}
	if opts.agentConfig == "" && !opts.noAgentConfig {
		// "" stays "": it means the project's own agents.yaml, which for the
		// new container is the new folder's file, not the source's.
		switch src.AgentConfig {
		case agentConfigNone:
			opts.noAgentConfig = true
		default:
			opts.agentConfig = src.AgentConfig
		}
	}
	return nil
}

// rewriteGeneratedTools changes which tools a generated container installs.
//
// Reads the current set back out of the stored document rather than from a
// column of its own, so a configuration edited by hand is still something this
// can reason about.
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

	// Derived from the stored rows, not from a flag: a rebuild must not be able
	// to change what a container is mounted on. This re-renders the whole
	// document, so any mount not rebuilt here is destroyed — the failure
	// invariant 10 records for the state volume, and the same one for a
	// worktree's binds.
	var mount dcgen.Mount
	switch t.container.SourceKind {
	case model.SourceNone:
		mount = folderlessMount(t.workspace.Name, name)
	default:
		// A folder container renders the zero Mount unless it is
		// worktree-backed, which is the CLI's own bind mount either way.
		//
		// Not re-derived from the checkout on disk: one the operator has
		// already deleted by hand would answer nothing, and the rebuild would
		// then quietly produce a container whose git does not work.
		st0, err := a.store()
		if err != nil {
			return err
		}
		if w, err := st0.GetWorktree(t.workspace.Name, name); err == nil {
			mount = worktreeMount(w.Repo, w.Path)
		}
	}
	// Derived from the stored column for the same reason, and load-bearing: this
	// re-renders the whole document, so a state mount that lived only in the
	// stored JSON would be dropped here and the next up would start a container
	// with no volume and no warning.
	state := stateFor(t.container.PersistState, t.workspace.Name, name)
	config, err := dcgen.Render(name, next, mount, state)
	if err != nil {
		return usageError(err)
	}

	st, err := a.store()
	if err != nil {
		return err
	}
	return st.UpdateContainerConfig(t.workspace.Name, name, config)
}
