// Package seed turns a host folder into what an isolated container starts
// with: a tar stream to unpack into its workspace, and a script to run there
// afterwards that makes the result a repository of its own.
//
// The repository travels as a git bundle inside the stream, never as .git's
// files. Copying .git would carry the host's config and hooks into a
// repository the agent then owns, and a worktree's .git is a file naming a
// host path that does not exist in the container. A bundle holds commits and
// branch names and nothing that runs.
//
// The working tree travels as it is on the host: uncommitted changes, untracked
// files, and ignored ones — .env, node_modules — which are why a copy is
// useful at all.
package seed

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BundleName is where the bundle sits in the archive, at the workspace root.
// The script removes it once the repository is built from it.
const BundleName = ".dev-seed.bundle"

// Plan is what the script needs to know about the folder that was archived.
type Plan struct {
	// Repository says the folder is in a git repository, so the archive holds
	// a bundle and the script builds a repository from it.
	Repository bool
	// Branch is the branch the folder has checked out, empty when detached.
	Branch string
	// Commit is HEAD, for a detached folder.
	Commit string
	// Origin is the folder's origin URL, empty when it has none.
	Origin string
}

// ScriptArgs are the positional arguments Script takes, for a workspace at dir.
func (p Plan) ScriptArgs(dir string) []string {
	repo := "0"
	if p.Repository {
		repo = "1"
	}
	return []string{"seed", dir, repo, BundleName, p.Branch, p.Commit, p.Origin}
}

// Script runs inside the container, in sh, after the archive is unpacked into
// the workspace. Arguments: $1 the workspace, $2 "1" for a repository, $3 the
// bundle's name, $4 the branch, $5 the commit, $6 the origin URL.
//
// The branches are fetched straight into refs/heads, HEAD is pointed at the
// folder's branch, and the index is rebuilt from it without touching the
// files — so the working tree, already unpacked, reads as exactly the changes
// it had on the host.
const Script = `set -e
cd "$1"
[ "$2" = 1 ] || exit 0
command -v git >/dev/null 2>&1 || { echo "the image has no git; add it to the project's devcontainer.json to use isolated mode" >&2; exit 1; }
git init -q .
git fetch -q --update-head-ok "$3" 'refs/heads/*:refs/heads/*'
if [ -n "$4" ]; then
  git symbolic-ref HEAD "refs/heads/$4"
else
  git update-ref --no-deref HEAD "$5"
fi
git reset -q
[ -z "$6" ] || git remote add origin "$6"
rm -f "$3"
`

// Archive writes folder as a tar stream to w, with a bundle of its branches at
// BundleName when it is in a repository, and returns what the script needs.
func Archive(ctx context.Context, folder string, w io.Writer) (Plan, error) {
	plan, bundle, err := bundleOf(ctx, folder)
	if err != nil {
		return Plan{}, err
	}
	tw := tar.NewWriter(w)
	if err := walk(folder, tw); err != nil {
		return Plan{}, err
	}
	if plan.Repository {
		if err := tw.WriteHeader(&tar.Header{
			Name: BundleName, Mode: 0o600, Size: int64(len(bundle)), Typeflag: tar.TypeReg,
		}); err != nil {
			return Plan{}, err
		}
		if _, err := tw.Write(bundle); err != nil {
			return Plan{}, err
		}
	}
	return plan, tw.Close()
}

// bundleOf bundles folder's branches, or reports it is not a repository.
func bundleOf(ctx context.Context, folder string) (Plan, []byte, error) {
	if _, err := git(ctx, folder, "rev-parse", "--git-dir"); err != nil {
		return Plan{}, nil, nil
	}
	plan := Plan{Repository: true}
	if branch, err := git(ctx, folder, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		plan.Branch = branch
	} else {
		commit, err := git(ctx, folder, "rev-parse", "HEAD")
		if err != nil {
			// A repository with no commits has nothing to bundle; the files
			// go in, and the agent starts the history.
			return Plan{}, nil, nil
		}
		plan.Commit = commit
	}
	plan.Origin, _ = git(ctx, folder, "remote", "get-url", "origin")

	cmd := exec.CommandContext(ctx, "git", "bundle", "create", "-", "--branches")
	cmd.Dir = folder
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return Plan{}, nil, fmt.Errorf("bundling %s: %w: %s", folder, err, strings.TrimSpace(stderr.String()))
	}
	return plan, out.Bytes(), nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// walk writes folder's contents to tw, leaving out .git at the root: the
// repository goes in as the bundle. A .git deeper down — a submodule's, a
// vendored repository's — is ordinary content and goes in with the rest.
func walk(folder string, tw *tar.Writer) error {
	err := filepath.Walk(folder, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(folder, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rel == ".git" {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Sockets, devices and named pipes cannot be carried in a tar and are
		// never part of a source tree worth copying.
		if info.Mode()&(os.ModeSocket|os.ModeDevice|os.ModeNamedPipe|os.ModeCharDevice) != 0 {
			return nil
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(path); err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		// Relative, and always with forward slashes: the archive is read on
		// Linux whatever the host is.
		header.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			header.Name += "/"
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		// Finishing the archive anyway would hand the container a truncated
		// tree that unpacks without complaint.
		return fmt.Errorf("reading %s: %w", strings.TrimSuffix(folder, "/"), err)
	}
	return nil
}
