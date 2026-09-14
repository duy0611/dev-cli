// Package k8s runs devcontainers in a Kubernetes cluster.
//
// The devcontainer CLI speaks Docker only, so it is used for the two things it
// alone can do — reading the merged configuration and building an image from
// Features and a Dockerfile — and kubectl does everything else.
package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/model"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/provider"
)

// rolloutTimeout bounds the wait for a pod to come up. Generous because the
// first start of a container pulls an image that may be gigabytes.
const rolloutTimeout = 10 * time.Minute

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
//
// The image is built only when the Deployment does not exist yet. That is the
// difference between a create and a start, and it is read from the cluster
// rather than tracked: `start` must not spend minutes rebuilding, and there is
// nowhere better to ask.
func (p *Provider) Up(ctx context.Context, c model.Container, env []provider.EnvVar) error {
	exists, err := p.deploymentExists(ctx, c)
	if err != nil {
		return err
	}
	return p.apply(ctx, c, env, applyOpts{build: !exists})
}

// Rebuild recreates the container from its configuration.
func (p *Provider) Rebuild(ctx context.Context, c model.Container, env []provider.EnvVar, noCache bool) error {
	return p.apply(ctx, c, env, applyOpts{
		build:   true,
		noCache: noCache,
		// The tag never changes, so without something new in the pod template
		// there is no new ReplicaSet and the freshly pushed image is ignored.
		rebuiltAt: time.Now().UTC().Format(time.RFC3339),
	})
}

type applyOpts struct {
	build     bool
	noCache   bool
	rebuiltAt string
}

func (p *Provider) apply(ctx context.Context, c model.Container, env []provider.EnvVar, opts applyOpts) error {
	dev, _, err := readConfiguration(ctx, c.Source)
	if err != nil {
		return err
	}

	image := imageTag(p.cfg.Registry, c)
	if opts.build {
		fmt.Fprintf(os.Stderr, "dev: building %s\n", image)
		if err := buildAndPush(ctx, c.Source, image, p.cfg.Platform, opts.noCache, os.Stderr); err != nil {
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
	return p.waitReady(ctx, c)
}

// Exec runs a command inside the container.
func (p *Provider) Exec(ctx context.Context, c model.Container, command []string, opts provider.ExecOpts) error {
	if len(command) == 0 {
		return fmt.Errorf("no command given")
	}
	dev, _, err := readConfiguration(ctx, c.Source)
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
	args = append(args, asRemoteUser(dev.RemoteUser, dev.WorkspaceFolder, inner)...)

	return p.kube.stream(ctx, streamOpts{
		Stdin:  opts.Stdin,
		Stdout: opts.Stdout,
		Stderr: opts.Stderr,
	}, args...)
}

// Stop scales to zero. The PVC, and so the workspace and home directories,
// survive.
func (p *Provider) Stop(ctx context.Context, c model.Container) error {
	exists, err := p.deploymentExists(ctx, c)
	if err != nil {
		return err
	}
	if !exists {
		return nil // already not running, which is what the caller wanted
	}
	return p.kube.run(ctx, "scale", "deployment/"+objectName(c), "--replicas=0")
}

// Remove deletes everything belonging to the container, the volume included.
//
// The PVC goes too: `dev container remove` means the container is gone, and
// leaving a claim behind would quietly bill for storage nothing references.
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
	// Scaled to zero, or scaled up but not ready yet. Neither can take an exec,
	// which is the only thing a caller does with this.
	return model.StatusStopped, nil
}

func (p *Provider) Logs(ctx context.Context, c model.Container, follow bool, out io.Writer) error {
	args := []string{"logs", "deployment/" + objectName(c)}
	if follow {
		args = append(args, "-f")
	}
	return p.kube.stream(ctx, streamOpts{Stdout: out, Stderr: os.Stderr}, args...)
}

// Sync satisfies provider.Syncer. Implemented in sync.go.

func (p *Provider) deploymentExists(ctx context.Context, c model.Container) (bool, error) {
	out, err := p.kube.output(ctx,
		"get", "deployment", objectName(c), "--ignore-not-found", "-o", "name")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// waitReady blocks until the pod is serving.
//
// `rollout status` rather than polling: it is the one command that understands
// a Recreate strategy, and it fails rather than hanging when the pod cannot be
// scheduled or the image cannot be pulled.
func (p *Provider) waitReady(ctx context.Context, c model.Container) error {
	return p.kube.run(ctx,
		"rollout", "status", "deployment/"+objectName(c),
		"--timeout", rolloutTimeout.String())
}
