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

// Sync makes the cluster's copy of the settings current, for the replacement
// pods the ReplicaSet creates. The running pod keeps its environment.
//
// Only the Secret is applied. A server-side apply of the Deployment without
// rebuiltAt would clear that annotation and change the pod template, and
// Recreate would then tear down the running pod. stringData is replaced, not
// merged, so an unset setting is dropped.
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

// seedWorkspace streams a tar of the host folder into a new container. It
// unpacks over the workspace, so it must only ever run from Up on first create,
// never against a container an agent is working in.
func (p *Provider) seedWorkspace(ctx context.Context, c model.Container) error {
	if c.SourceKind != model.SourceFolder {
		// Checked before the cluster is read, so an unreachable cluster cannot
		// mask it.
		return fmt.Errorf("container %s has no folder to seed from", c.Name)
	}
	dev, _, err := readConfiguration(ctx, c.Source, c.ConfigPath)
	if err != nil {
		return err
	}

	// -p, or the container's umask strips the executable bit off every script.
	return p.kube.pipeInto(ctx,
		func(w io.Writer) error { return tarDir(c.Source, w) },
		"exec", "-i", "deployment/"+objectName(c), "--",
		"tar", "-x", "-p", "-C", dev.WorkspaceFolder)
}

// tarDir writes folder to w as a tar stream, .git included.
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

		// A tar cannot carry sockets or devices; skip them rather than fail the
		// sync.
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
		// Forward slashes: the archive is read on Linux, whatever the host.
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
		// Skip tw.Close: a closed, truncated archive would unpack without
		// complaint.
		return fmt.Errorf("reading %s: %w", strings.TrimSuffix(folder, "/"), err)
	}
	return tw.Close()
}
