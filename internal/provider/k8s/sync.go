package k8s

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

// Sync makes the cluster's copy of the workspace's settings current.
//
// The pod reads them through envFrom at start, so this changes nothing for a
// process already running there — Sync says so on stderr rather than leaving
// the operator to wonder. What it buys is the pod nobody drives: an eviction,
// an OOM kill or a node drain gets a replacement from the ReplicaSet, and that
// pod reads the Secret as it stands in the cluster. Without this, a token
// rotated since the last up comes back stale.
//
// The Secret alone, never the Deployment. The apply is server-side and drops
// fields the applier no longer sets, so an apply built here — with no rebuiltAt
// — would clear that annotation, change the pod template, and let the Recreate
// strategy tear down the very work this command exists to leave alone.
//
// Dropping absent fields is also what carries an unset setting away: the
// object's stringData is replaced, not merged.
func (p *Provider) Sync(ctx context.Context, c model.Container, env []provider.EnvVar) error {
	dev, _, err := readConfiguration(ctx, c.Source, c.ConfigPath)
	if err != nil {
		return err
	}

	manifest, err := json.Marshal(buildSecret(manifestInput{Container: c, Dev: dev, Env: env}))
	if err != nil {
		return fmt.Errorf("rendering the secret for container %s: %w", c.Name, err)
	}
	if err := p.kube.apply(ctx, manifest); err != nil {
		return err
	}

	fmt.Fprint(os.Stderr,
		"dev: the pod keeps the environment it started with; anything already running\n"+
			"dev: there sees the new values only after stop and start\n")
	return nil
}

// seedWorkspace copies the host folder into a freshly created container.
//
// A tar streamed over `kubectl exec`, which is what `kubectl cp` does too — but
// built here so the archive is produced in Go rather than by the host's tar,
// whose flags differ between BSD and GNU, and so the contents are this code's
// decision rather than a shell glob's.
//
// Reachable only from Up, and only on first create. That is what makes it safe:
// it unpacks over the workspace, so running it against a container an agent has
// been working in would destroy whatever is half-done there. An empty volume is
// the only thing it may ever land on.
func (p *Provider) seedWorkspace(ctx context.Context, c model.Container) error {
	if c.SourceKind != model.SourceFolder {
		// An assertion about the caller now rather than a refusal the operator
		// can trigger. Checked before reading the configuration all the same: a
		// complaint that needs a cluster to produce it is one that fails for
		// the wrong reason when the cluster is unreachable.
		return fmt.Errorf("container %s has no folder to seed from", c.Name)
	}
	dev, _, err := readConfiguration(ctx, c.Source, c.ConfigPath)
	if err != nil {
		return err
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
