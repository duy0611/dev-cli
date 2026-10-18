// Package gitguard keeps host git from running what a container wrote. The
// workspace is a writable mount, and host git (including the IDE's git status)
// executes programs named under .git: core.fsmonitor, hooks, and commondir or
// gitdir redirects.
//
// Where the layout is known, the paths are mounted or copied so the container
// cannot change what the host reads. Where it is not, a fingerprint is compared
// before and after a command. FindTree covers the working tree, the third place
// host git runs from. The package reads and copies only; the CLI decides when
// to act.
package gitguard

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
)

// Layout is where a folder's .git is on the host and inside the container.
type Layout struct {
	// GitDir is the repository's .git directory on the host.
	GitDir string
	// ContainerGitDir is the same directory inside the container.
	ContainerGitDir string
}

// LayoutOf returns the layout the devcontainer CLI gives folder by default, or
// false when there is nothing to guard. It mirrors the CLI's
// mount-workspace-git-root rule: the root is the first ancestor holding
// .git/config, mounted at /workspaces/<basename>. Not for a project that names
// its own workspace mount. A worktree checkout is declined; WorktreeMounts
// covers it.
func LayoutOf(folder string) (Layout, bool) {
	for dir := folder; ; {
		gitdir := filepath.Join(dir, ".git")
		if info, err := os.Stat(filepath.Join(gitdir, "config")); err == nil && info.Mode().IsRegular() {
			return Layout{
				GitDir: gitdir,
				// posix: the path is inside a Linux container whatever the host
				// is.
				ContainerGitDir: path.Join("/workspaces", filepath.Base(dir), ".git"),
			}, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Layout{}, false
		}
		dir = parent
	}
}

// Mounts returns the mounts entries that guard an ordinary checkout, in
// devcontainer.json's string form. hooks is the host directory holding the
// container's copy of the hooks.
//
// .git is bound onto itself first, so the container cannot rename it away from
// under the mounts below. Order matters: a mount inside another must come after
// it.
func Mounts(l Layout, hooks string) []string {
	return []string{
		bind(l.GitDir, l.ContainerGitDir, false),
		bind(filepath.Join(l.GitDir, "config"), path.Join(l.ContainerGitDir, "config"), true),
		bind(hooks, path.Join(l.ContainerGitDir, "hooks"), false),
	}
}

// ReadOnlyMounts returns the mounts entry that guards an ordinary checkout in
// guarded mode: all of .git, read-only, as one directory mount.
//
// One directory, not a mount per file: host git replaces files inside .git by
// writing a lock and renaming it over the original, and renaming over a mount
// point detaches the mount. git never replaces .git itself, so a mount on the
// directory survives. On a case-insensitive host, .git/CONFIG misses a mount on
// .git/config but resolves through the directory mount.
func ReadOnlyMounts(l Layout) []string {
	return []string{bind(l.GitDir, l.ContainerGitDir, true)}
}

// ReadOnlyWorktreeMounts returns the mounts entry that guards a worktree
// container in guarded mode: the common directory, read-only at its own host
// path, so the checkout's gitdir line resolves (invariant 11). The checkout's
// .git file is not mounted: a host rewrite detaches a single-file mount, and on
// macOS a .GIT path evades it.
func ReadOnlyWorktreeMounts(common string) []string {
	return []string{bind(common, common, true)}
}

// WorktreeMounts returns the mounts entries that guard a worktree container,
// with the checkout and common directory each at its own host path (invariant
// 11). common is git's common directory, checkout the worktree. commondir,
// gitdir and config.worktree are read-only because host git follows them; HEAD,
// index and refs stay writable, or commits stop working.
func WorktreeMounts(common, checkout, hooks string) ([]string, error) {
	admin, err := adminDir(checkout)
	if err != nil {
		return nil, err
	}
	out := []string{
		bind(filepath.Join(common, "config"), filepath.Join(common, "config"), true),
		bind(hooks, filepath.Join(common, "hooks"), false),
		bind(filepath.Join(checkout, ".git"), filepath.Join(checkout, ".git"), true),
	}
	for _, name := range []string{"commondir", "gitdir", "config.worktree"} {
		p := filepath.Join(admin, name)
		if _, err := os.Lstat(p); err == nil {
			out = append(out, bind(p, p, true))
		}
	}
	return out, nil
}

// adminDir returns the administration directory a worktree checkout's .git file
// names.
func adminDir(checkout string) (string, error) {
	b, err := os.ReadFile(filepath.Join(checkout, ".git"))
	if err != nil {
		return "", fmt.Errorf("reading the worktree's .git file: %w", err)
	}
	var dir string
	if _, err := fmt.Sscanf(string(b), "gitdir: %s", &dir); err != nil || dir == "" {
		return "", fmt.Errorf("the worktree's .git file at %s names no gitdir", checkout)
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(checkout, dir)
	}
	return filepath.Clean(dir), nil
}

func bind(source, target string, readonly bool) string {
	s := "source=" + source + ",target=" + target + ",type=bind"
	if readonly {
		s += ",readonly"
	}
	return s
}

// SeedHooks makes dst a copy of src's hooks, emptied first. Host to container
// only.
//
// dst is written by the container, so nothing here follows a symlink: RemoveAll
// removes a link itself, and a symlinked hook is copied as a link, never read
// through. A missing src leaves dst empty.
func SeedHooks(src, dst string) error {
	entries, err := os.ReadDir(dst)
	if err != nil {
		return fmt.Errorf("reading %s: %w", dst, err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dst, e.Name())); err != nil {
			return fmt.Errorf("emptying %s: %w", dst, err)
		}
	}

	hooks, err := os.ReadDir(src)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	for _, h := range hooks {
		from, to := filepath.Join(src, h.Name()), filepath.Join(dst, h.Name())
		switch {
		case h.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(from)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, to); err != nil {
				return err
			}
		case h.Type().IsRegular():
			if err := copyFile(from, to); err != nil {
				return err
			}
		}
		// Only files: git runs only files in hooks/.
	}
	return nil
}

func copyFile(from, to string) error {
	info, err := os.Lstat(from)
	if err != nil {
		return err
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// Fingerprint is a hash of everything under a .git directory that host git
// would execute or follow.
type Fingerprint map[string]string

// Snapshot fingerprints gitdir's config, commondir and hooks. Ordinary writes
// such as refs, objects and the index are excluded, so a commit is not a
// change.
func Snapshot(gitdir string) (Fingerprint, error) {
	s := Fingerprint{}
	for _, name := range []string{"config", "commondir", "config.worktree"} {
		if sum, ok, err := fileSum(filepath.Join(gitdir, name)); err != nil {
			return nil, err
		} else if ok {
			s[name] = sum
		}
	}
	hooks, err := os.ReadDir(filepath.Join(gitdir, "hooks"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, h := range hooks {
		p := filepath.Join(gitdir, "hooks", h.Name())
		if h.Type()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return nil, err
			}
			s["hooks/"+h.Name()] = "link:" + target
			continue
		}
		if sum, ok, err := fileSum(p); err != nil {
			return nil, err
		} else if ok {
			s["hooks/"+h.Name()] = sum
		}
	}
	return s, nil
}

// Changed names what differs between two fingerprints, sorted.
func Changed(before, after Fingerprint) []string {
	var out []string
	for k, v := range after {
		if before[k] != v {
			out = append(out, k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// CommondirPlanted reports whether gitdir holds a commondir. An ordinary
// checkout never has one. No mount can close this: a mount needs an existing
// target, and an empty commondir breaks host git, so it is checked before and
// after each command.
func CommondirPlanted(gitdir string) bool {
	_, err := os.Lstat(filepath.Join(gitdir, "commondir"))
	return err == nil
}

func fileSum(p string) (string, bool, error) {
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", false, err
	}
	return hex.EncodeToString(h.Sum(nil)), true, nil
}

// RootLayout returns the layout of dir mounted at containerRoot, or false
// unless dir is itself a repository root. For a container's extra folders,
// which are mounted as given rather than at their git root.
func RootLayout(dir, containerRoot string) (Layout, bool) {
	gitdir := filepath.Join(dir, ".git")
	if info, err := os.Stat(filepath.Join(gitdir, "config")); err != nil || !info.Mode().IsRegular() {
		return Layout{}, false
	}
	return Layout{GitDir: gitdir, ContainerGitDir: path.Join(containerRoot, ".git")}, true
}
