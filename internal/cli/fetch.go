package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/provider/local"
	"github.com/duy0611/dev-cli/internal/xpath"
	"github.com/spf13/cobra"
)

func newContainerFetchCmd(a *app) *cobra.Command {
	var workspace string

	cmd := &cobra.Command{
		Use:   "fetch NAME",
		Short: "Bring an isolated container's branches into its folder",
		Long: "Bring an isolated container's branches into its folder.\n\n" +
			"Every branch in the container's repository arrives in the folder it was\n" +
			"created from as NAME/<branch>, a remote-tracking ref: nothing local is\n" +
			"changed, and you decide what to merge.\n\n" +
			"Only commit data crosses. The branches travel as a git bundle, so\n" +
			"nothing from the container's configuration or hooks is read on your\n" +
			"machine, and objects are checked as they arrive.\n\n" +
			"Uncommitted work in the container stays there; commit it inside first.\n" +
			"A guarded container's work is already in its folder, so it has nothing\n" +
			"to fetch.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContainerFetch(cmd.Context(), a, workspace, args[0])
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	return cmd
}

func runContainerFetch(ctx context.Context, a *app, workspace, name string) error {
	t, err := a.resolve(ctx, workspace, name)
	if err != nil {
		return err
	}
	defer t.release()
	if err := requireIsolated(t.container); err != nil {
		return err
	}
	if _, err := os.Stat(t.container.Source); err != nil {
		return fmt.Errorf("container %s was created from %s, which is gone: %w",
			name, xpath.Shorten(t.container.Source), err)
	}

	if isRemote(ctx, t.container.Source, name) {
		return usageErrorf("%s has a git remote named %s; fetching would overwrite its refs/remotes/%s/*",
			xpath.Shorten(t.container.Source), name, name)
	}

	bundle, err := containerBundle(ctx, t)
	if err != nil {
		return err
	}
	if bundle == nil {
		a.printf("container %s has no branches to fetch\n", name)
		return nil
	}

	// A file, not stdin: git fetch refuses "-" as a bundle path.
	f, err := os.CreateTemp("", "dev-fetch-*.bundle")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(bundle); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	// fsckObjects rejects a malformed object from the container. Forced, since
	// the agent may have rewritten a branch.
	cmd := exec.CommandContext(ctx, "git", "-c", "transfer.fsckObjects=true",
		"fetch", "--quiet", f.Name(), "+refs/heads/*:"+fetchedRefs(name)+"*")
	cmd.Dir = t.container.Source
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("fetching container %s's branches into %s: %w: %s",
			name, xpath.Shorten(t.container.Source), err, strings.TrimSpace(stderr.String()))
	}

	refs, err := gitLines(ctx, t.container.Source, "for-each-ref", "--format=%(refname:short)", fetchedRefs(name))
	if err != nil {
		return err
	}
	a.printf("fetched into %s: %s\n", xpath.Shorten(t.container.Source), strings.Join(refs, ", "))
	a.record("fetch", t.workspace.Name, name, map[string]any{"refs": refs})
	return nil
}

func requireIsolated(c model.Container) error {
	if c.Mode != model.ModeIsolated {
		return usageErrorf("container %s is %s; only an isolated container has work outside its folder to fetch",
			c.Name, displayMode(c.Mode))
	}
	return nil
}

// displayMode is a mode as an operator reads it.
func displayMode(m model.Mode) string {
	if m == model.ModeNone {
		return "folderless"
	}
	return string(m)
}

// containerBundle returns a bundle of every branch, or nil when the repository
// has none.
func containerBundle(ctx context.Context, t *target) ([]byte, error) {
	var out, stderr bytes.Buffer
	err := t.provider.Exec(ctx, t.container,
		[]string{"sh", "-c", `cd "$1" && git rev-parse --verify --quiet HEAD >/dev/null || exit 3; git bundle create - --branches`,
			"bundle", workspaceFolderOf(t.container)},
		provider.ExecOpts{Stdout: &out, Stderr: &stderr})
	if err != nil {
		if exitCode(err) == 3 {
			return nil, nil
		}
		return nil, fmt.Errorf("bundling container %s's branches: %w: %s",
			t.container.Name, err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}

// unexported names what an isolated container holds that its folder lacks.
// Empty when removing it loses nothing.
func unexported(ctx context.Context, t *target) ([]string, error) {
	var out, stderr bytes.Buffer
	// One exec for both questions, separated by a line the status cannot
	// contain.
	err := t.provider.Exec(ctx, t.container,
		[]string{"sh", "-c", `cd "$1" || exit 4
git rev-parse --git-dir >/dev/null 2>&1 || exit 4
git status --porcelain
echo '--dev-branches--'
git for-each-ref --format='%(refname:short) %(objectname)' refs/heads`,
			"unexported", workspaceFolderOf(t.container)},
		provider.ExecOpts{Stdout: &out, Stderr: &stderr})
	if err != nil {
		if exitCode(err) == 4 {
			return []string{"files that are not in a git repository, so nothing records what changed"}, nil
		}
		return nil, fmt.Errorf("asking container %s what it holds: %w: %s",
			t.container.Name, err, strings.TrimSpace(stderr.String()))
	}
	status, branches, _ := strings.Cut(out.String(), "--dev-branches--\n")
	var found []string
	if strings.TrimSpace(status) != "" {
		found = append(found, "uncommitted changes")
	}
	for _, line := range strings.Split(strings.TrimSpace(branches), "\n") {
		branch, tip, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		cmd := exec.CommandContext(ctx, "git", "cat-file", "-e", tip+"^{commit}")
		cmd.Dir = t.container.Source
		if cmd.Run() != nil {
			found = append(found, "branch "+branch+" has commits the folder does not")
			continue
		}
		kept, err := keptElsewhere(ctx, t.container.Source, t.container.Name, tip)
		if err != nil {
			return nil, err
		}
		if !kept {
			found = append(found, "branch "+branch+" is fetched but not merged into a branch or pushed to a remote")
		}
	}
	return found, nil
}

// keptElsewhere reports whether tip is reachable from a ref other than the
// ones fetch wrote for container, which remove deletes.
func keptElsewhere(ctx context.Context, source, container, tip string) (bool, error) {
	refs, err := gitLines(ctx, source, "for-each-ref", "--format=%(refname)", "--contains", tip, "refs/heads", "refs/remotes")
	if err != nil {
		return false, err
	}
	remote := isRemote(ctx, source, container)
	for _, ref := range refs {
		if remote || !strings.HasPrefix(ref, fetchedRefs(container)) {
			return true, nil
		}
	}
	return false, nil
}

// fetchedRefs is the ref prefix fetch writes a container's branches under.
func fetchedRefs(container string) string {
	return "refs/remotes/" + container + "/"
}

// isRemote reports whether source has a git remote called name, whose
// tracking refs share the prefix fetch writes under.
func isRemote(ctx context.Context, source, name string) bool {
	remotes, err := gitLines(ctx, source, "remote")
	return err == nil && slices.Contains(remotes, name)
}

// dropFetchedRefs deletes the refs fetch wrote for container.
func dropFetchedRefs(ctx context.Context, source, container string) error {
	if isRemote(ctx, source, container) {
		return nil
	}
	refs, err := gitLines(ctx, source, "for-each-ref", "--format=delete %(refname)", fetchedRefs(container))
	if err != nil || len(refs) == 0 {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "update-ref", "--stdin")
	cmd.Dir = source
	cmd.Stdin = strings.NewReader(strings.Join(refs, "\n") + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git update-ref: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// workspaceFolderOf is where an isolated container's workspace is inside it.
func workspaceFolderOf(c model.Container) string {
	return workspaceRoot(c)
}

// workspaceRoot is a variable so a test can point it at a host directory.
var workspaceRoot = func(c model.Container) string {
	return local.WorkspaceFolder(c.Name)
}

func gitLines(ctx context.Context, dir string, args ...string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// exitCode is the failed command's exit status, or -1.
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
