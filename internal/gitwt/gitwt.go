// Package gitwt drives git's worktree commands.
//
// Shelling out rather than go-git, for the reason the rest of this tool shells
// out: git's own behaviour is the behaviour wanted, including its error
// messages, and a reimplementation would be a second opinion about a format
// git owns.
package gitwt

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const gitBin = "git"

// ErrNotRepo is returned when a directory is not inside a git repository.
var ErrNotRepo = errors.New("not a git repository")

// Entry is one checkout as `git worktree list` reports it.
type Entry struct {
	Path   string
	Branch string // short name, empty for a detached HEAD
}

// CommonDir returns the absolute path of the repository's common directory —
// the directory holding the object store and the worktree administration.
//
// This answers two questions with one call, deliberately. It says whether dir
// is in a repository at all, and it is the directory that must be bind-mounted
// into a container: a worktree's .git file holds `gitdir: <common>/worktrees/
// <name>`, an absolute host path. A separate IsRepo could disagree with it.
//
// --git-common-dir rather than --show-toplevel: from inside a worktree the
// latter answers the worktree, so creating a worktree from inside another one
// would register it against the wrong place. It is also correct for a bare
// repository, which has no working tree to be the top level of.
func CommonDir(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, gitBin,
		"rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		// Git's message names ".git" rather than the directory the operator
		// was standing in, so it is dropped and the caller writes its own.
		return "", fmt.Errorf("%w: %s", ErrNotRepo, dir)
	}
	return strings.TrimSpace(string(out)), nil
}

// Add creates a checkout at path.
//
// branch is checked out when it already exists and created otherwise, because
// `git worktree add -b` fails outright on a branch that exists. base names the
// start point for a new branch and is ignored for an existing one — the caller
// rejects that combination before getting here.
func Add(ctx context.Context, repo, path, branch, base string) error {
	args := []string{"worktree", "add", "--quiet"}
	if branchExists(ctx, repo, branch) {
		args = append(args, path, branch)
	} else {
		args = append(args, path, "-b", branch)
		if base != "" {
			args = append(args, base)
		}
	}
	return runGit(ctx, repo, args...)
}

// Remove deletes the checkout and its administrative directory.
//
// The branch is never touched; that is git's behaviour and the command's
// promise. force passes through to git, which refuses a checkout holding
// modified or untracked files without it.
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

	// The porcelain format is one blank-line-separated record per checkout,
	// each a sequence of "key value" lines. Parsed rather than the plain
	// format because that one pads with spaces and cannot be split safely.
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
			// Reported as a full ref; the short name is what a human reads.
			cur.Branch = strings.TrimPrefix(
				strings.TrimPrefix(line, "branch "), "refs/heads/")
		}
	}
	flush()
	return entries, nil
}

// branchExists reports whether the repository already has this local branch.
//
// --verify on the full ref, not `git branch --list`: the latter prints nothing
// and exits 0 for a branch that does not exist, so its exit code says nothing.
func branchExists(ctx context.Context, repo, branch string) bool {
	cmd := exec.CommandContext(ctx, gitBin,
		"rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = repo
	return cmd.Run() == nil
}

func runGit(ctx context.Context, repo string, args ...string) error {
	cmd := exec.CommandContext(ctx, gitBin, args...)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %s", args[1], strings.TrimSpace(string(out)))
	}
	return nil
}
