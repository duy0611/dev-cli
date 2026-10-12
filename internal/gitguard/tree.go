package gitguard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Root is a host directory a container can write, and where it appears in the
// container.
type Root struct {
	// Host is the directory on the host, resolved.
	Host string
	// Container is the same directory inside the container.
	Container string
}

// TreePath is something host git runs or reads config from that lies in a
// directory the container can write: a core.hooksPath into the checkout, an
// include.path naming a file there, or a symlinked hook's target.
type TreePath struct {
	// Why names the setting or hook that points here, for the operator.
	Why string
	// Host is the path on the host, resolved.
	Host string
	// Container is the same path inside the container.
	Container string
	// Root is the writable directory Host lies in.
	Root Root
	// Missing says Host does not exist yet. Nothing can be mounted at a missing
	// path.
	Missing bool
	// Symlink is a link inside Root that Host is reached through, or empty. A
	// mount over its target would not follow a repoint.
	Symlink string
}

// Unmountable says why no mount can hold p, or is empty when one can.
func (p TreePath) Unmountable() string {
	switch {
	case p.Missing:
		return "it does not exist yet, and the container creating it is the same as editing it"
	case p.Symlink != "":
		return "it is reached through " + p.Symlink + ", a symlink the container can repoint"
	case p.Host == p.Root.Host:
		return "it is the whole workspace"
	}
	return ""
}

// Bind is one bind mount the tree guard asks for.
type Bind struct {
	Host, Container string
	ReadOnly        bool
	// Why is the TreePath Why this mount serves.
	Why string
}

// Entry is b in devcontainer.json's mounts string form.
func (b Bind) Entry() string { return bind(b.Host, b.Container, b.ReadOnly) }

// gitBin is the git FindTree runs. A variable so a test can point it at a stub.
var gitBin = "git"

// FindTree asks host git what it would run, or read configuration from, inside
// roots, for the checkout at checkout. shielded are host paths already guarded
// another way. Host git answers because core.hooksPath and includes can come
// from any config and resolve relative to their file. Every include is followed
// whatever its condition. A path that cannot be mounted is still returned, so
// the caller can refuse and say why.
func FindTree(ctx context.Context, checkout string, roots []Root, shielded []string) ([]TreePath, error) {
	var cands []include

	// Run from the top of the checkout: config origins are relative to it,
	// --git-path is relative to the directory asked from.
	if _, err := exec.LookPath(gitBin); err != nil {
		return nil, nil
	}
	top, err := runGit(ctx, checkout, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	top = strings.TrimSpace(top)
	hooks, err := runGit(ctx, top, "rev-parse", "--git-path", "hooks")
	if err != nil {
		return nil, err
	}
	hooks = strings.TrimSpace(hooks)
	if !filepath.IsAbs(hooks) {
		hooks = filepath.Join(top, hooks)
	}
	cands = append(cands, include{"core.hooksPath", hooks})

	// A symlinked hook runs whatever it names, so its target is checked too.
	entries, err := os.ReadDir(hooks)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading %s: %w", hooks, err)
	}
	for _, e := range entries {
		if e.Type()&fs.ModeSymlink == 0 {
			continue
		}
		target, err := os.Readlink(filepath.Join(hooks, e.Name()))
		if err != nil {
			return nil, err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(hooks, target)
		}
		cands = append(cands, include{"hook " + e.Name(), target})
	}

	includes, err := findIncludes(ctx, top)
	if err != nil {
		return nil, err
	}
	cands = append(cands, includes...)

	var out []TreePath
	seen := map[string]int{}
	for _, c := range cands {
		phys, root, missing, link, err := locate(c.path, roots, 0)
		if err != nil {
			return nil, fmt.Errorf("following %s for %s: %w", c.path, c.why, err)
		}
		if root == nil || under(phys, shielded) {
			continue
		}
		if i, ok := seen[phys]; ok {
			out[i].Why += ", " + c.why
			continue
		}
		rel, err := filepath.Rel(root.Host, phys)
		if err != nil {
			return nil, err
		}
		seen[phys] = len(out)
		out = append(out, TreePath{
			Why:  c.why,
			Host: phys,
			// posix, not filepath: a path inside a Linux container whatever
			// the host is.
			Container: path.Join(root.Container, filepath.ToSlash(rel)),
			Root:      *root,
			Missing:   missing,
			Symlink:   link,
		})
	}
	return out, nil
}

// include is a path host git reads or runs, and the setting that names it.
type include struct{ why, path string }

// findIncludes lists every include.path and includeIf.*.path git sees from top,
// resolved as git resolves them: ~/ against the home directory, a relative path
// against the file that names it.
func findIncludes(ctx context.Context, top string) ([]include, error) {
	out, err := runGit(ctx, top, "config", "-z", "--show-origin", "--get-regexp", `^include(if\..+)?\.path$`)
	if err != nil {
		var exit *exec.ExitError
		// Exit 1 is "no such key", which is the ordinary answer.
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	// -z: origin NUL key LF value NUL, repeated.
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	var incs []include
	for i := 0; i+1 < len(fields); i += 2 {
		origin := fields[i]
		key, value, _ := strings.Cut(fields[i+1], "\n")
		file, ok := strings.CutPrefix(origin, "file:")
		// Command-line and blob origins name no file a relative path could hang
		// from.
		if !ok || value == "" {
			continue
		}
		if !filepath.IsAbs(file) {
			file = filepath.Join(top, file)
		}
		p := value
		switch {
		case strings.HasPrefix(p, "~/"):
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			p = filepath.Join(home, p[2:])
		case !filepath.IsAbs(p):
			p = filepath.Join(filepath.Dir(file), p)
		}
		incs = append(incs, include{why: key + " in " + file, path: p})
	}
	return incs, nil
}

// locate walks p one component at a time, as the kernel would, following a
// symlink outside every root. It returns the physical path, its root (nil if
// none), whether it is missing, and any symlink inside a root that it is
// reached through. It walks rather than calling EvalSymlinks: that answers
// where p leads today, not whether the container can change it tomorrow.
func locate(p string, roots []Root, depth int) (string, *Root, bool, string, error) {
	// The kernel's own limit on a chain of links.
	if depth > 40 {
		return "", nil, false, "", fmt.Errorf("too many levels of symbolic links at %s", p)
	}
	p = filepath.Clean(p)
	comps := strings.Split(strings.TrimPrefix(p, string(filepath.Separator)), string(filepath.Separator))
	cur := string(filepath.Separator)
	for i, comp := range comps {
		next := filepath.Join(cur, comp)
		info, err := os.Lstat(next)
		if errors.Is(err, fs.ErrNotExist) {
			phys := filepath.Join(append([]string{next}, comps[i+1:]...)...)
			r := rootOf(phys, roots)
			return phys, r, r != nil, "", nil
		}
		if err != nil {
			return "", nil, false, "", err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			if r := rootOf(next, roots); r != nil {
				return p, r, false, next, nil
			}
			target, err := os.Readlink(next)
			if err != nil {
				return "", nil, false, "", err
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(cur, target)
			}
			return locate(filepath.Join(append([]string{target}, comps[i+1:]...)...), roots, depth+1)
		}
		cur = next
	}
	return cur, rootOf(cur, roots), false, "", nil
}

// rootOf returns the root p lies in, the deepest when roots nest, or nil.
func rootOf(p string, roots []Root) *Root {
	var best *Root
	for i, r := range roots {
		if within(p, r.Host) && (best == nil || len(r.Host) > len(best.Host)) {
			best = &roots[i]
		}
	}
	return best
}

func under(p string, dirs []string) bool {
	return slices.ContainsFunc(dirs, func(d string) bool { return within(p, d) })
}

// within reports whether p is dir or lies below it, by path segment.
func within(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}

// TreeBinds returns the mounts that hold each mountable tree path read-only,
// skipping any destination in existing. Each directory between the root and the
// target is bound onto itself too: a mount follows its directory on rename, so
// a parent could be swapped out from under a read-only target.
func TreeBinds(paths []TreePath, existing []string) []Bind {
	have := map[string]bool{}
	for _, e := range existing {
		have[entryTarget(e)] = true
	}
	var out []Bind
	add := func(b Bind) {
		if !have[b.Container] {
			have[b.Container] = true
			out = append(out, b)
		}
	}
	for _, p := range paths {
		rel, err := filepath.Rel(p.Root.Host, p.Host)
		// Unmountable paths are refused by the caller, not mounted.
		if p.Unmountable() != "" || err != nil {
			continue
		}
		comps := strings.Split(rel, string(filepath.Separator))
		for i := 1; i < len(comps); i++ {
			sub := filepath.Join(comps[:i]...)
			add(Bind{
				Host:      filepath.Join(p.Root.Host, sub),
				Container: path.Join(p.Root.Container, filepath.ToSlash(sub)),
				Why:       p.Why,
			})
		}
		add(Bind{Host: p.Host, Container: p.Container, ReadOnly: true, Why: p.Why})
	}
	slices.SortStableFunc(out, func(a, b Bind) int {
		return strings.Count(a.Container, "/") - strings.Count(b.Container, "/")
	})
	return out
}

// entryTarget reads the target of a mounts entry in string form.
func entryTarget(entry string) string {
	for field := range strings.SplitSeq(entry, ",") {
		k, v, _ := strings.Cut(field, "=")
		switch strings.TrimSpace(k) {
		case "target", "dst", "destination":
			return v
		}
	}
	return ""
}

// SnapshotPaths fingerprints paths for the fallback guard: a file by its
// contents, a directory by its entries, a symlink by its target, and a missing
// path by its absence.
func SnapshotPaths(paths []string) (Fingerprint, error) {
	s := Fingerprint{}
	for _, p := range paths {
		info, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if err := snapshotEntry(s, p); err != nil {
				return nil, err
			}
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if err := snapshotEntry(s, filepath.Join(p, e.Name())); err != nil {
				return nil, err
			}
		}
	}
	return s, nil
}

func snapshotEntry(s Fingerprint, p string) error {
	info, err := os.Lstat(p)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(p)
		if err != nil {
			return err
		}
		s[p] = "link:" + target
	case info.Mode().IsRegular():
		sum, _, err := fileSum(p)
		if err != nil {
			return err
		}
		s[p] = sum
	default:
		// git runs only the files at the top of hooks/.
		s[p] = "dir"
	}
	return nil
}

// runGit runs host git in dir with core.fsmonitor forced off, since the config
// read may be one a container tampered with.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, gitBin, append([]string{"-c", "core.fsmonitor=false"}, args...)...)
	cmd.Dir = dir
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		// Exit 1 with no stderr is git config's no-such-key answer; pass it
		// through.
		if errors.As(err, &exit) && exit.ExitCode() == 1 && stderr.Len() == 0 {
			return "", err
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("asking host git in %s what it runs: %s", dir, msg)
	}
	return string(out), nil
}
