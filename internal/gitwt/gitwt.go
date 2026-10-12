// Package gitwt drives git's worktree commands.
//
// Shells out to git rather than using go-git, so git's own behaviour and error
// messages are what the operator sees.
package gitwt

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
)

const gitBin = "git"

// ErrNotRepo is returned when a directory is not inside a git repository.
var ErrNotRepo = errors.New("not a git repository")

// ErrNoWorkTree is returned for a directory in a repository but in no
// checkout of it: a bare repository, or its common directory.
var ErrNoWorkTree = errors.New("not in a checkout")

// Entry is one checkout as `git worktree list` reports it.
type Entry struct {
	Path   string
	Branch string // short name, empty for a detached HEAD
}

// CommonDir returns the absolute path of the repository's common directory,
// which holds the object store and worktree administration. It also reports
// whether dir is in a repository at all.
//
// --git-common-dir rather than --show-toplevel: from inside a linked worktree
// the latter names the worktree, not the repository.
func CommonDir(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, gitBin,
		"rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		// Git's message names ".git", not dir, so it is replaced.
		return "", fmt.Errorf("%w: %s", ErrNotRepo, dir)
	}
	return strings.TrimSpace(string(out)), nil
}

// Add creates a checkout at path. An existing branch is checked out, since
// `git worktree add -b` fails on one that exists.
func Add(ctx context.Context, repo, path, branch, base string) error {
	args := []string{"worktree", "add", "--quiet"}
	if BranchExists(ctx, repo, branch) {
		args = append(args, path, branch)
	} else {
		args = append(args, path, "-b", branch)
		if base != "" {
			args = append(args, base)
		}
	}
	return runGit(ctx, repo, args...)
}

// Remove deletes the checkout and its administrative directory. The branch is
// kept.
func Remove(ctx context.Context, repo, path string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	return runGit(ctx, repo, append(args, path)...)
}

// List reports the repository's checkouts, the main one included.
func List(ctx context.Context, repo string) ([]Entry, error) {
	cmd := exec.CommandContext(ctx, gitBin, "worktree", "list", "--porcelain")
	cmd.Dir = repo
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("listing worktrees: %s", msg)
	}

	// Porcelain rather than plain output, which pads with spaces and cannot be
	// split safely.
	var (
		entries []Entry
		cur     Entry
	)
	flush := func() {
		if cur.Path != "" {
			entries = append(entries, cur)
		}
		cur = Entry{}
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			cur.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			// Reported as a full ref.
			cur.Branch = strings.TrimPrefix(
				strings.TrimPrefix(line, "branch "), "refs/heads/")
		}
	}
	flush()
	return entries, nil
}

// BranchExists reports whether the repository already has this local branch.
//
// --verify on the full ref: `git branch --list` exits 0 for a missing branch.
func BranchExists(ctx context.Context, repo, branch string) bool {
	cmd := exec.CommandContext(ctx, gitBin,
		"rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = repo
	return cmd.Run() == nil
}

// WorktreeRepo returns the common directory when dir is in a linked worktree,
// and false otherwise. A linked worktree's git directory differs from the
// common one; the main checkout's is the same.
func WorktreeRepo(ctx context.Context, dir string) (string, bool) {
	cmd := exec.CommandContext(ctx, gitBin,
		"rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 || lines[0] == lines[1] {
		return "", false
	}
	return lines[1], true
}

// Toplevel returns the root of the checkout holding dir.
//
// Toplevel returns the checkout dir is in, which for a linked worktree is the
// worktree itself rather than the repository.
func Toplevel(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, gitBin, "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNoWorkTree, dir)
	}
	return strings.TrimSpace(string(out)), nil
}

// IgnoredFiles lists the untracked files in a checkout that its own ignore
// rules exclude, relative to its root.
//
// One entry per file, never --directory, which groups the same tree differently
// from one listing to the next.
func IgnoredFiles(ctx context.Context, dir string) ([]string, error) {
	return lsFiles(ctx, dir, "--others", "--ignored", "--exclude-standard")
}

// MatchingFiles lists the untracked files in a checkout that the pattern files
// match, in gitignore syntax, ignoring the checkout's own ignore rules.
//
// Later pattern files override earlier ones.
func MatchingFiles(ctx context.Context, dir string, patternFiles []string) ([]string, error) {
	args := []string{"--others", "--ignored"}
	for _, f := range patternFiles {
		args = append(args, "--exclude-from="+f)
	}
	return lsFiles(ctx, dir, args...)
}

// TrackedFiles lists the files git tracks in a checkout.
func TrackedFiles(ctx context.Context, dir string) ([]string, error) {
	return lsFiles(ctx, dir, "--cached")
}

// AllFiles lists every file in a checkout outside .git, tracked or not,
// ignored or not.
func AllFiles(ctx context.Context, dir string) ([]string, error) {
	return lsFiles(ctx, dir, "--cached", "--others")
}

// lsFiles runs `git ls-files` and returns its paths sorted and deduplicated.
//
// -z: a path may contain a newline, which git would otherwise quote. Duplicates
// come from --cached listing a conflicted file once per stage.
func lsFiles(ctx context.Context, dir string, args ...string) ([]string, error) {
	cmd := exec.CommandContext(ctx, gitBin, append([]string{"ls-files", "-z"}, args...)...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("listing files in %s: %s", dir, msg)
	}
	var paths []string
	for p := range strings.SplitSeq(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths), nil
}

func runGit(ctx context.Context, repo string, args ...string) error {
	cmd := exec.CommandContext(ctx, gitBin, args...)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %s", args[1], strings.TrimSpace(string(out)))
	}
	return nil
}
