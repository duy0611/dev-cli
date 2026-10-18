package cli

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/duy0611/dev-cli/internal/folders"
	"github.com/duy0611/dev-cli/internal/gitguard"
	"github.com/duy0611/dev-cli/internal/gitwt"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider/local"
	"github.com/duy0611/dev-cli/internal/xpath"
)

// resolveExtraFolders turns --folder arguments beyond the first into folders,
// each resolved and placed for mode. Worktree checkouts are read from git, so
// one made outside dev is placed as correctly as one dev made.
func resolveExtraFolders(ctx context.Context, args []string, mode model.Mode) ([]model.Folder, error) {
	out := make([]model.Folder, 0, len(args))
	for _, arg := range args {
		host, err := xpath.Resolve(arg)
		if err != nil {
			return nil, usageError(err)
		}
		if err := refuseBroadFolder(host); err != nil {
			return nil, err
		}
		out = append(out, placeFolder(ctx, host, mode))
	}
	return out, nil
}

// placeFolder decides where a resolved extra folder appears in a container of
// mode.
func placeFolder(ctx context.Context, host string, mode model.Mode) model.Folder {
	isolated := mode == model.ModeIsolated
	f := model.Folder{HostPath: host}
	if repo, ok := gitwt.WorktreeRepo(ctx, host); ok && !isolated {
		f.WorktreeRepo = repo
	}
	f.ContainerPath = folders.ContainerPath(host, f.WorktreeRepo != "", isolated)
	return f
}

// sortFolders orders folders by host path, the order the store returns them in.
func sortFolders(fs []model.Folder) {
	slices.SortFunc(fs, func(a, b model.Folder) int { return strings.Compare(a.HostPath, b.HostPath) })
}

// primaryPath is where c's primary folder appears inside the container: the
// devcontainer CLI's rule of mounting the git root at /workspaces/<basename>
// for a guarded one, its own path for a worktree, the volume for an isolated
// one.
func primaryPath(c model.Container) (container, extent string) {
	switch {
	case c.Mode == model.ModeIsolated:
		return local.WorkspaceFolderOf(c), c.Source
	case c.WorktreeRepo != "":
		return c.Source, c.Source
	}
	if layout, ok := gitguard.LayoutOf(c.Source); ok {
		return path.Dir(layout.ContainerGitDir), filepath.Dir(layout.GitDir)
	}
	return path.Join("/workspaces", filepath.Base(c.Source)), c.Source
}

// checkFolders refuses a set of folders that overlap or share a container path.
func checkFolders(c model.Container) error {
	if len(c.Folders) == 0 {
		return nil
	}
	container, extent := primaryPath(c)
	entries := []folders.Entry{{Host: c.Source, Extent: extent, Container: container}}
	for _, f := range c.Folders {
		entries = append(entries, folders.Entry{Host: f.HostPath, Extent: f.HostPath, Container: f.ContainerPath})
	}
	if err := folders.Check(entries); err != nil {
		return usageError(err)
	}
	return nil
}

// folderMounts are the mounts entries that bring c's extra folders into the
// container, each followed by its guard, and their targets. A guarded folder is
// a bind; an isolated one is a volume of its own.
func folderMounts(c model.Container) (mounts, targets []string) {
	for _, f := range c.Folders {
		var add []string
		switch c.Mode {
		case model.ModeIsolated:
			add = []string{fmt.Sprintf("source=%s,target=%s,type=volume",
				local.FolderVolumeName(c.WorkspaceName, c.Name, f.HostPath), f.ContainerPath)}
		case model.ModeGuarded:
			add = []string{fmt.Sprintf("source=%s,target=%s,type=bind", f.HostPath, f.ContainerPath)}
			if f.WorktreeRepo != "" {
				add = append(add, gitguard.ReadOnlyWorktreeMounts(f.WorktreeRepo)...)
			} else if layout, ok := gitguard.RootLayout(f.HostPath, f.ContainerPath); ok {
				add = append(add, gitguard.ReadOnlyMounts(layout)...)
			}
		}
		mounts = append(mounts, add...)
		for _, m := range add {
			targets = append(targets, mountTarget(m))
		}
	}
	return mounts, targets
}

// mountTarget reads the target of a mounts entry dev wrote.
func mountTarget(entry string) string {
	for field := range strings.SplitSeq(entry, ",") {
		if v, ok := strings.CutPrefix(field, "target="); ok {
			return v
		}
	}
	return ""
}

// guardedRepos are the host repositories whose .git a guarded container holds
// read-only, primary first.
func guardedRepos(c model.Container) []string {
	if c.Mode != model.ModeGuarded {
		return nil
	}
	var out []string
	if c.GitGuard {
		out = append(out, c.Source)
	}
	for _, f := range c.Folders {
		if _, ok := gitguard.RootLayout(f.HostPath, f.ContainerPath); ok || f.WorktreeRepo != "" {
			out = append(out, f.HostPath)
		}
	}
	return out
}

// requireFolders refuses to start a guarded container whose extra folder is
// gone from the host, rather than leave the engine to fail the mount.
func requireFolders(c model.Container) error {
	if c.Mode != model.ModeGuarded {
		return nil
	}
	for _, f := range c.Folders {
		if info, err := os.Stat(f.HostPath); err != nil || !info.IsDir() {
			return fmt.Errorf("container %s's folder %s is gone; "+
				"restore it, or drop it with: dev container rebuild %s --remove-folder %s",
				c.Name, xpath.Shorten(f.HostPath), c.Name, f.HostPath)
		}
	}
	return nil
}

// folderSummary is the SOURCE column: the primary, then +N for extra folders.
func folderSummary(c model.Container) string {
	s := xpath.Shorten(c.Source)
	if n := len(c.Folders); n > 0 {
		s += fmt.Sprintf(" +%d", n)
	}
	return s
}

// changeFolders applies rebuild's --add-folder and --remove-folder to c, which
// must be a guarded container. A removed folder is matched by its stored path,
// since it may be gone from the host.
func changeFolders(ctx context.Context, c *model.Container, add, remove []string) error {
	if c.Mode != model.ModeGuarded {
		if c.Mode == model.ModeIsolated {
			return usageErrorf("container %s is isolated; its folders are fixed, recreate it to change them", c.Name)
		}
		return usageErrorf("container %s is %s; only a guarded container's folders can change", c.Name, displayMode(c.Mode))
	}
	next := slices.Clone(c.Folders)
	for _, arg := range remove {
		host := storedPath(arg)
		if host == c.Source {
			return usageErrorf("%s is container %s's primary folder; recreate the container to change it",
				xpath.Shorten(host), c.Name)
		}
		i := slices.IndexFunc(next, func(f model.Folder) bool { return f.HostPath == host })
		if i < 0 {
			return usageErrorf("%s is not one of container %s's folders", xpath.Shorten(host), c.Name)
		}
		next = slices.Delete(next, i, i+1)
	}
	added, err := resolveExtraFolders(ctx, add, c.Mode)
	if err != nil {
		return err
	}
	for _, f := range added {
		if f.HostPath == c.Source || slices.ContainsFunc(next, func(g model.Folder) bool { return g.HostPath == f.HostPath }) {
			return usageErrorf("%s is already one of container %s's folders", xpath.Shorten(f.HostPath), c.Name)
		}
	}
	next = append(next, added...)
	sortFolders(next)
	c.Folders = next
	return checkFolders(*c)
}

// storedPath is the path a folder was stored under: resolved when it still
// exists, otherwise made absolute.
func storedPath(arg string) string {
	if p, err := xpath.Resolve(arg); err == nil {
		return p
	}
	if abs, err := filepath.Abs(arg); err == nil {
		return abs
	}
	return arg
}
