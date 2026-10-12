// Package wtseed copies chosen ignored files (.env, node_modules) from one
// checkout into a new worktree. git decides what matches; cp does the copying.
package wtseed

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/duy0611/dev-cli/internal/gitwt"
)

// FileName is the manifest listing carried files, at the checkout's root. The
// name and format match treehouse's.
const FileName = ".worktreeinclude"

// ErrNoSource is returned when patterns were given but there is no checkout to
// copy from.
var ErrNoSource = errors.New("no checkout to copy from")

// PatternFiles returns the manifests to apply, in order: the source's own
// .worktreeinclude, then includeFile, so its negations win. includeFile is made
// absolute because git later runs from the source checkout.
func PatternFiles(source, includeFile string) ([]string, error) {
	var files []string
	if source != "" {
		own := filepath.Join(source, FileName)
		if _, err := os.Stat(own); err == nil {
			files = append(files, own)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if includeFile == "" {
		return files, nil
	}
	if source == "" {
		return nil, ErrNoSource
	}
	abs, err := filepath.Abs(includeFile)
	if err != nil {
		return nil, err
	}
	// Opened, not stat'd: git silently skips an unreadable --exclude-from.
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return nil, err
	} else if fi.IsDir() {
		return nil, fmt.Errorf("%s is a directory", includeFile)
	}
	return append(files, abs), nil
}

// Select returns what to copy from source into dest, relative to each root. A
// directory copied whole ends in "/". A file qualifies when the patterns match
// it, source ignores it, and dest does not track it; requiring git to ignore it
// keeps a broad pattern such as `*` off tracked files and .git.
func Select(ctx context.Context, source, dest string, patternFiles []string) ([]string, error) {
	if len(patternFiles) == 0 {
		return nil, nil
	}
	matched, err := gitwt.MatchingFiles(ctx, source, patternFiles)
	if err != nil {
		return nil, err
	}
	ignored, err := gitwt.IgnoredFiles(ctx, source)
	if err != nil {
		return nil, err
	}
	all, err := gitwt.AllFiles(ctx, source)
	if err != nil {
		return nil, err
	}
	tracked, err := gitwt.TrackedFiles(ctx, dest)
	if err != nil {
		return nil, err
	}
	return selectFrom(matched, ignored, all, tracked), nil
}

// Seed selects and copies in one step, returning what it copied. On failure the
// caller must roll back the partial checkout.
func Seed(ctx context.Context, source, dest string, patternFiles []string) ([]string, error) {
	entries, err := Select(ctx, source, dest, patternFiles)
	if err != nil {
		return nil, err
	}
	if err := Copy(ctx, source, dest, entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// selectFrom is Select without git, so the set logic can be tested alone.
func selectFrom(matched, ignored, all, tracked []string) []string {
	isIgnored := set(ignored)
	trackedFile := set(tracked)
	trackedDir := map[string]bool{}
	for _, t := range tracked {
		for d := parent(t); d != ""; d = parent(d) {
			trackedDir[d] = true
		}
	}
	blocked := func(p string) bool {
		if trackedFile[p] || trackedDir[p] {
			return true
		}
		for d := parent(p); d != ""; d = parent(d) {
			if trackedFile[d] {
				return true
			}
		}
		return false
	}

	var chosen []string
	for _, p := range matched {
		if isIgnored[p] && !blocked(p) {
			chosen = append(chosen, p)
		}
	}

	// A directory is copied whole only when every source file under it was
	// chosen, and never if dest tracks anything in it: cp would nest inside the
	// existing one.
	total, picked := map[string]int{}, map[string]int{}
	for _, p := range all {
		for d := parent(p); d != ""; d = parent(d) {
			total[d]++
		}
	}
	for _, p := range chosen {
		for d := parent(p); d != ""; d = parent(d) {
			picked[d]++
		}
	}
	whole := func(d string) bool { return !trackedDir[d] && picked[d] == total[d] }

	var out []string
	seen := map[string]bool{}
	for _, p := range chosen {
		entry := p
		for d := parent(p); d != ""; d = parent(d) {
			if whole(d) {
				entry = d + "/"
			}
		}
		if !seen[entry] {
			seen[entry] = true
			out = append(out, entry)
		}
	}
	slices.Sort(out)
	return out
}

// copyCommands are the cp invocations tried in order for one entry. macOS -c
// clones on APFS and is refused elsewhere, hence the fallback. Linux
// --reflink=auto clones where the filesystem can.
var copyCommands = func() [][]string {
	switch runtime.GOOS {
	case "darwin":
		return [][]string{{"cp", "-c", "-Rp"}, {"cp", "-Rp"}}
	case "linux":
		return [][]string{{"cp", "-a", "--reflink=auto"}}
	default:
		return [][]string{{"cp", "-Rp"}}
	}
}()

// Copy copies each entry from source into dest, creating parent directories.
// Symlinks are copied, never followed. The error names the failing entry.
func Copy(ctx context.Context, source, dest string, entries []string) error {
	for _, e := range entries {
		rel := strings.TrimSuffix(e, "/")
		src, dst := filepath.Join(source, rel), filepath.Join(dest, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("copying %s: %w", e, err)
		}
		if _, err := os.Lstat(dst); err == nil {
			// cp would nest inside an existing directory rather than fail.
			return fmt.Errorf("copying %s: already exists in the new checkout", e)
		}
		if err := copyOne(ctx, src, dst); err != nil {
			return fmt.Errorf("copying %s: %w", e, err)
		}
	}
	return nil
}

func copyOne(ctx context.Context, src, dst string) error {
	var last error
	for _, argv := range copyCommands {
		cmd := exec.CommandContext(ctx, argv[0], append(argv[1:], src, dst)...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		last = fmt.Errorf("%s: %s", strings.Join(argv, " "), strings.TrimSpace(string(out)))
		// A failed attempt may leave a partial tree that the next would copy
		// into. Nothing was at dst before, so removing it is safe.
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
	}
	return last
}

func set(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// parent returns the slash-separated parent of a git path, "" at the root.
func parent(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return ""
	}
	return p[:i]
}
