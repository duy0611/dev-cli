// Package gitguard keeps host git from running what a container wrote.
//
// A container's workspace is a writable bind mount, and host git — including
// the IDE's `git status`, every few seconds — executes programs named in files
// under .git: core.fsmonitor and friends in config, scripts in hooks, and a
// commondir or a worktree's gitdir that redirect git to read some other
// directory's config. One `git config core.fsmonitor` inside the container is
// code on the operator's host.
//
// Two mechanisms. Where the layout is known, the paths are mounted so the
// container cannot change what the host reads: config read-only (git writes it
// by lock-and-rename, and a rename onto a bind-mounted file fails, so a
// writable copy would behave the same), hooks as a container-only copy, and
// .git bound onto itself so it cannot be renamed away. Where it is not known —
// a project that names its own workspace mount — a fingerprint taken before
// a command is compared after it.
//
// This package reads and copies; it never runs git and never writes into a
// project's folder. The CLI decides when.
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

// Layout is where a folder's .git is on the host and where the devcontainer
// CLI will put it inside the container.
type Layout struct {
	// GitDir is the repository's .git directory on the host.
	GitDir string
	// ContainerGitDir is the same directory inside the container.
	ContainerGitDir string
}

// LayoutOf returns the layout the devcontainer CLI gives folder by default, or
// false when there is nothing to guard.
//
// The CLI's rule, mirrored: it mounts the repository's root rather than the
// folder when the folder is inside one (mount-workspace-git-root, default on),
// finding the root by walking up to the first directory holding .git/config,
// and mounts it at /workspaces/<basename of the root>. A project that names its
// own workspaceMount or workspaceFolder replaces that rule, and the caller must
// not use this layout for it.
//
// A worktree checkout, whose .git is a file, has no .git/config to find and is
// declined: dev's worktree containers mount the checkout at its host path, and
// WorktreeMounts covers them.
func LayoutOf(folder string) (Layout, bool) {
	for dir := folder; ; {
		gitdir := filepath.Join(dir, ".git")
		if info, err := os.Stat(filepath.Join(gitdir, "config")); err == nil && info.Mode().IsRegular() {
			return Layout{
				GitDir: gitdir,
				// posix, not filepath: this is a path inside a Linux container
				// whatever the host is.
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
// .git is bound onto itself first, so the two below land on a mount point the
// container cannot rename or remove — without it, `mv .git .git.old` and a
// fresh .git would leave both guards standing over nothing. Order matters: a
// mount inside another must come after it.
func Mounts(l Layout, hooks string) []string {
	return []string{
		bind(l.GitDir, l.ContainerGitDir, false),
		bind(filepath.Join(l.GitDir, "config"), path.Join(l.ContainerGitDir, "config"), true),
		bind(hooks, path.Join(l.ContainerGitDir, "hooks"), false),
	}
}

// WorktreeMounts returns the mounts entries that guard a worktree container,
// whose checkout and common directory are each mounted at their own host path
// (invariant 11). common is git's common directory, checkout the worktree.
//
// Beyond config and hooks, a worktree has redirects of its own: the checkout's
// .git file names the administration directory, and that directory's commondir
// and gitdir name the rest. Rewriting any of them points host git at a config
// the container wrote, so all three are read-only. config.worktree only exists
// when the repository uses extensions.worktreeConfig — which the container
// cannot turn on, since that is a write to the read-only main config.
//
// HEAD, index and refs stay writable, or commits stop working.
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

// adminDir reads a worktree checkout's .git file for the administration
// directory it names.
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

// SeedHooks makes dst a copy of the hooks in src: emptied first, so what a
// container installed is discarded, then filled from the project's own. Host to
// container only; nothing is ever copied back.
//
// dst's contents are written by the container, so nothing here follows a
// symlink: os.RemoveAll removes a link rather than what it names, and a hook
// that is a symlink is copied as one, never read through. A src that does not
// exist — a repository whose hooks directory was deleted — leaves dst empty.
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
		// Directories and anything else are skipped: git runs only files in
		// hooks/, and recursing would be one more thing to get wrong.
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
// would execute or follow: config, every hook, and a commondir.
type Fingerprint map[string]string

// Snapshot fingerprints gitdir. Nothing git writes in ordinary use — refs,
// objects, the index, logs — is in it, so a commit inside the container is not
// a change.
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
// checkout never has one; a container that writes one points host git at
// another directory's config. No mount can close this — a mount needs an
// existing target, and an empty commondir breaks host git outright — so it is
// checked instead, before and after every command.
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
