// Package wtseed carries chosen ignored files from one checkout into a new
// one, so a worktree starts with the .env files and dependency trees git does
// not hand it.
//
// git decides what matches — the manifest is gitignore syntax and git is the
// authority on that — and cp does the copying, which on APFS and on btrfs or
// xfs is a clone that costs neither time nor disk until a file changes.
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

// FileName is the manifest a project lists its carried files in, relative to
// the checkout's root. The name and format are treehouse's, so a project that
// uses both keeps one file.
const FileName = ".worktreeinclude"

// ErrNoSource is returned when patterns were asked for but there is no
// checkout to copy from — a bare repository, run from outside its worktrees.
var ErrNoSource = errors.New("no checkout to copy from")

// PatternFiles returns the manifests to apply, in order: the source's own
// .worktreeinclude when it has one, then includeFile when given, so the
// flag's patterns land on top and its negations win.
//
// It runs before the checkout exists, so a bad --include-file costs nothing to
// roll back. includeFile is made absolute here because git is later run from
// the source checkout, where a relative path would name a different file.
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
	// Opened, not just stat'd: git skips an --exclude-from it cannot read
	// without a word, which would turn a typo into a silent no-op.
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

// Select returns what to copy from source into dest, relative to each root.
// A directory to copy whole ends in "/"; everything else is a file.
//
// A file qualifies when the patterns match it, the source's own rules ignore
// it, and dest does not track it — the branch may commit a file the source
// branch ignores, and copying over it would change the branch's content.
// Requiring git to ignore it is what keeps a broad pattern such as `*` away
// from tracked files and .git.
//
// Qualifying files are then gathered back into the highest directories that
// can be copied whole, so a node_modules of a hundred thousand files is one
// cp rather than a hundred thousand.
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

// Seed selects and copies in one step, returning what it copied.
//
// A failure is the caller's to roll back: a checkout holding half a
// node_modules and no .env looks finished and is not, which is worse than an
// error.
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
	// A tracked path in dest blocks its own path, every directory above it,
	// and anything that would need it to be a directory.
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

	// A directory is copied whole when every file the source has under it was
	// chosen. A directory dest tracks anything in never qualifies: blocked()
	// already refused its files' paths, but a whole-directory cp would land on
	// the existing directory and nest inside it.
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

// copyCommands are the cp invocations tried in order for one entry, source and
// destination appended. A later one runs only when the one before failed.
//
// macOS: -c clones on APFS and is refused elsewhere, hence the plain fallback.
// -R without -L copies a symlink as a symlink. Linux: --reflink=auto clones
// where the filesystem can and copies where it cannot, so one is enough; -a
// keeps symlinks as symlinks too.
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
//
// Symlinks are copied as symlinks and never followed. A relative one — pnpm's
// inside node_modules — resolves inside dest; an absolute one still points
// where it did. The error names the entry that failed, so the operator knows
// which path to look at.
func Copy(ctx context.Context, source, dest string, entries []string) error {
	for _, e := range entries {
		rel := strings.TrimSuffix(e, "/")
		src, dst := filepath.Join(source, rel), filepath.Join(dest, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("copying %s: %w", e, err)
		}
		if _, err := os.Lstat(dst); err == nil {
			// Select never picks a path dest has, so this is a checkout that
			// changed underneath. cp would nest a directory inside it rather
			// than fail, so refuse.
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
		// A failed attempt may have left half a tree, and the next one would
		// copy into it as a subdirectory. Nothing was at dst before — Copy
		// checked — so removing it takes nothing that was there.
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
// git reports paths with "/" on every platform.
func parent(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return ""
	}
	return p[:i]
}
