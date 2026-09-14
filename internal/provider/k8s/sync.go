package k8s

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/model"
)

// Sync copies the host folder into the container's workspace.
//
// A tar streamed over `kubectl exec`, which is what `kubectl cp` does too — but
// built here so the archive is produced in Go rather than by the host's tar,
// whose flags differ between BSD and GNU, and so the contents are this code's
// decision rather than a shell glob's.
//
// One direction, on demand. The container's copy is the working one once an
// agent is running in it; overwriting that automatically would destroy work
// nobody asked to discard.
func (p *Provider) Sync(ctx context.Context, c model.Container) error {
	dev, _, err := readConfiguration(ctx, c.Source)
	if err != nil {
		return err
	}
	if c.SourceKind != model.SourceFolder {
		return fmt.Errorf("container %s was not created from a folder", c.Name)
	}

	// -p keeps the modes the archive carries; without it the container's umask
	// silently strips the executable bit off every script.
	return p.kube.pipeInto(ctx,
		func(w io.Writer) error { return tarDir(c.Source, w) },
		"exec", "-i", "deployment/"+objectName(c), "--",
		"tar", "-x", "-p", "-C", dev.WorkspaceFolder)
}

// tarDir writes folder's contents to w as a tar stream.
//
// Everything is included, .git as well: an agent working in the container needs
// the history to branch and commit, and a repository without it is a surprise
// discovered late.
func tarDir(folder string, w io.Writer) error {
	tw := tar.NewWriter(w)

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

		// Sockets, devices and named pipes cannot be carried in a tar and are
		// never part of a source tree worth copying. A stray one — an editor's
		// socket, a database's — should not stop the whole sync.
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
		// Closing anyway would finish an archive that is missing files, and the
		// container would happily unpack the truncated result.
		return fmt.Errorf("reading %s: %w", strings.TrimSuffix(folder, "/"), err)
	}
	return tw.Close()
}
