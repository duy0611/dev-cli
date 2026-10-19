// Package k8s runs devcontainers in a cluster. The devcontainer CLI is used
// only to read the merged configuration and build the image; kubectl does
// everything else.
package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

// Generous: the first start pulls an image that may be gigabytes.
const rolloutTimeout = 10 * time.Minute

// Normally a second or two, as the pod handles SIGTERM. The timeout covers a
// loaded node.
const (
	terminationTimeout = 2 * time.Minute
	pollInterval       = 500 * time.Millisecond
)

// Provider runs containers as Deployments scaled between 0 and 1.
type Provider struct {
	cfg  Config
	kube kubectl
}

func init() {
	provider.Register(model.KindK8s, func(p model.Provider) (provider.Provider, error) {
		cfg, err := ParseConfig(p.Config)
		if err != nil {
			return nil, err
		}
		return &Provider{cfg: cfg, kube: newKubectl(cfg)}, nil
	})
}

// Up creates the container if the cluster has none, and starts it otherwise.
// The image is built only on create, judged from the cluster rather than
// tracked.
func (p *Provider) Up(ctx context.Context, c model.Container, env []provider.EnvVar) error {
	exists, err := p.deploymentExists(ctx, c)
	if err != nil {
		return err
	}
	return p.apply(ctx, c, env, applyOpts{build: !exists, firstCreate: !exists})
}

// Rebuild recreates the container from its configuration.
func (p *Provider) Rebuild(ctx context.Context, c model.Container, env []provider.EnvVar, opts provider.RebuildOpts) error {
	if opts.Seed {
		return errors.New("a k8s container is isolated from create; nothing to seed")
	}
	return p.apply(ctx, c, env, applyOpts{
		build:   true,
		noCache: opts.NoCache,
		// The tag is always :latest, so only a changed pod template gives a new
		// ReplicaSet.
		rebuiltAt: time.Now().UTC().Format(time.RFC3339),
	})
}

type applyOpts struct {
	build       bool
	firstCreate bool
	noCache     bool
	rebuiltAt   string
}

func (p *Provider) apply(ctx context.Context, c model.Container, env []provider.EnvVar, opts applyOpts) error {
	dev, lc, err := readConfiguration(ctx, c.Source, c.ConfigPath)
	if err != nil {
		return err
	}

	image := imageTag(p.cfg.Registry, c)
	if opts.build {
		fmt.Fprintf(os.Stderr, "dev: building %s\n", image)
		if err := buildAndPush(ctx, c.Source, c.ConfigPath, image, p.cfg.Platform, opts.noCache, os.Stderr); err != nil {
			return err
		}
	}

	manifest, err := buildManifest(manifestInput{
		Container: c,
		Config:    p.cfg,
		Dev:       dev,
		Image:     image,
		Env:       env,
		Replicas:  1,
		RebuiltAt: opts.rebuiltAt,
	})
	if err != nil {
		return err
	}
	if err := p.kube.apply(ctx, manifest); err != nil {
		return err
	}
	if err := p.waitReady(ctx, c); err != nil {
		return err
	}

	// Seeded before the lifecycle commands, which usually need the project's
	// files.
	if opts.firstCreate && c.SourceKind == model.SourceFolder {
		if err := p.seedWorkspace(ctx, c); err != nil {
			return err
		}
	}
	return p.runLifecycle(ctx, c, dev, lc, env, opts.rebuiltAt != "")
}

// Exec runs a command inside the container.
func (p *Provider) Exec(ctx context.Context, c model.Container, command []string, opts provider.ExecOpts) error {
	if len(command) == 0 {
		return fmt.Errorf("no command given")
	}
	dev, _, err := readConfiguration(ctx, c.Source, c.ConfigPath)
	if err != nil {
		return err
	}

	env := make([]envVar, 0, len(opts.Env))
	for _, e := range opts.Env {
		env = append(env, envVar{Key: e.Key, Value: e.Value})
	}
	inner := append(envPrefix(env), command...)

	args := []string{"exec"}
	if opts.TTY {
		args = append(args, "-i", "-t")
	} else if opts.Stdin != nil {
		args = append(args, "-i")
	}
	args = append(args, "deployment/"+objectName(c), "--")
	args = append(args, asRemoteUser(dev.RemoteUser, dev.HomeDir(), dev.WorkspaceFolder, inner)...)

	return p.kube.stream(ctx, streamOpts{
		Stdin:  opts.Stdin,
		Stdout: opts.Stdout,
		Stderr: opts.Stderr,
	}, args...)
}

// Stop scales to zero and waits for the pod to go. `kubectl scale` returns
// before the pod is gone, and docker stop does not behave that way.
func (p *Provider) Stop(ctx context.Context, c model.Container) error {
	exists, err := p.deploymentExists(ctx, c)
	if err != nil {
		return err
	}
	if !exists {
		return nil // already not running, which is what the caller wanted
	}
	if err := p.kube.run(ctx, "scale", "deployment/"+objectName(c), "--replicas=0"); err != nil {
		return err
	}
	return p.waitGone(ctx, c)
}

// Remove deletes everything belonging to the container, the volume included.
//
// Leaving the claim behind would keep billing for storage nothing references.
func (p *Provider) Remove(ctx context.Context, c model.Container) error {
	name := objectName(c)
	for _, target := range []struct{ kind, name string }{
		{"deployment", name},
		{"secret", secretName(c)},
		{"persistentvolumeclaim", name},
	} {
		if err := p.kube.deleteIgnoringMissing(ctx, target.kind, target.name); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) Status(ctx context.Context, c model.Container) (model.Status, error) {
	out, err := p.kube.output(ctx,
		"get", "deployment", objectName(c), "--ignore-not-found", "-o", "json")
	if err != nil {
		return model.StatusAbsent, err
	}
	if strings.TrimSpace(out) == "" {
		return model.StatusAbsent, nil
	}

	var d struct {
		Status struct {
			ReadyReplicas int `json:"readyReplicas"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		return model.StatusAbsent, fmt.Errorf("reading the deployment: %w", err)
	}
	if d.Status.ReadyReplicas > 0 {
		return model.StatusRunning, nil
	}
	// Stopped covers both scaled to zero and not yet ready; neither takes an
	// exec.
	return model.StatusStopped, nil
}

func (p *Provider) Logs(ctx context.Context, c model.Container, follow bool, out io.Writer) error {
	args := []string{"logs", "deployment/" + objectName(c)}
	if follow {
		args = append(args, "-f")
	}
	return p.kube.stream(ctx, streamOpts{Stdout: out, Stderr: os.Stderr}, args...)
}

func (p *Provider) deploymentExists(ctx context.Context, c model.Container) (bool, error) {
	out, err := p.kube.output(ctx,
		"get", "deployment", objectName(c), "--ignore-not-found", "-o", "name")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// waitGone blocks until no pod carries this container's labels. Polled, not
// `kubectl wait --for=delete`, which errors when nothing matches (that is
// success).
func (p *Provider) waitGone(ctx context.Context, c model.Container) error {
	deadline := time.Now().Add(terminationTimeout)
	for {
		out, err := p.kube.output(ctx, "get", "pods", "-l", selector(c), "-o", "name")
		if err != nil {
			return err
		}
		if strings.TrimSpace(out) == "" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("container %s did not stop within %s", c.Name, terminationTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// waitReady blocks until the pod is serving. rollout status understands the
// Recreate strategy and fails, rather than hangs, when the pod cannot be
// scheduled or pulled.
func (p *Provider) waitReady(ctx context.Context, c model.Container) error {
	return p.kube.run(ctx,
		"rollout", "status", "deployment/"+objectName(c),
		"--timeout", rolloutTimeout.String())
}
