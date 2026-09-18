// Package local runs devcontainers on a Docker-compatible engine, by driving
// the devcontainer CLI and docker rather than reimplementing either.
package local

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/model"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/provider"
)

// The two binaries this provider drives.
//
// docker rather than the Docker Go SDK: DOCKER_HOST already points the CLI at
// whichever engine is running (podman's socket included), the SDK would be a
// large dependency for four commands, and shelling out keeps one story for both
// providers — the Kubernetes one will drive kubectl the same way.
const (
	devcontainerBin = "devcontainer"
	dockerBin       = "docker"
)

// Provider runs containers through the devcontainer CLI.
type Provider struct{}

func init() {
	provider.Register(model.KindLocal, func(model.Provider) (provider.Provider, error) {
		return &Provider{}, nil
	})
}

// Up creates or starts the container. The devcontainer CLI treats this as one
// operation, so there is no separate create.
func (p *Provider) Up(ctx context.Context, c model.Container, env []provider.EnvVar) error {
	return p.up(ctx, c, env, false, false)
}

// Rebuild recreates the container from its configuration.
//
// There is no `devcontainer rebuild`: the CLI spells it as flags on `up`.
func (p *Provider) Rebuild(ctx context.Context, c model.Container, env []provider.EnvVar, noCache bool) error {
	return p.up(ctx, c, env, true, noCache)
}

func (p *Provider) up(ctx context.Context, c model.Container, env []provider.EnvVar, recreate, noCache bool) error {
	if err := requireBinary(devcontainerBin); err != nil {
		return err
	}

	args := []string{"up", "--workspace-folder", c.Source}
	args = append(args, idLabelArgs(c)...)
	// The config path is passed rather than inferred. A generated one lives in
	// a temporary directory the CLI's own lookup would never reach, and for a
	// project-owned one this is the path dcconfig already resolved.
	if c.ConfigPath != "" {
		args = append(args, "--config", c.ConfigPath)
	}
	if recreate {
		args = append(args, "--remove-existing-container")
	}
	if noCache {
		args = append(args, "--build-no-cache")
	}
	args = append(args, remoteEnvArgs(env)...)

	cmd := exec.CommandContext(ctx, devcontainerBin, args...)
	// `up` prints a JSON result line on success and progress on stderr. The
	// progress is worth showing — an image build is minutes of silence
	// otherwise — but the JSON is noise, so stdout is swallowed.
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("starting container %s: %w", c.Name, err)
	}
	return nil
}

// Exec runs a command inside the container.
func (p *Provider) Exec(ctx context.Context, c model.Container, command []string, opts provider.ExecOpts) error {
	if err := requireBinary(devcontainerBin); err != nil {
		return err
	}
	if len(command) == 0 {
		return fmt.Errorf("no command given")
	}

	args := []string{"exec", "--workspace-folder", c.Source}
	args = append(args, idLabelArgs(c)...)
	// The config path is passed rather than inferred. A generated one lives in
	// a temporary directory the CLI's own lookup would never reach, and for a
	// project-owned one this is the path dcconfig already resolved.
	if c.ConfigPath != "" {
		args = append(args, "--config", c.ConfigPath)
	}
	args = append(args, remoteEnvArgs(opts.Env)...)
	args = append(args, command...)

	cmd := exec.CommandContext(ctx, devcontainerBin, args...)
	cmd.Stdin = opts.Stdin
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	return cmd.Run()
}

func (p *Provider) Stop(ctx context.Context, c model.Container) error {
	id, err := p.containerID(ctx, c)
	if err != nil {
		return err
	}
	if id == "" {
		return nil // already gone; the caller wanted it not running
	}
	return runDocker(ctx, "stop", id)
}

func (p *Provider) Remove(ctx context.Context, c model.Container) error {
	id, err := p.containerID(ctx, c)
	if err != nil {
		return err
	}
	if id != "" {
		if err := runDocker(ctx, "rm", "-f", id); err != nil {
			return err
		}
	}

	// The volume is named in workspaceMount rather than created by the
	// container, so `docker rm` leaves it behind. Removing it here is what
	// makes `container remove` mean the container is gone, and matches the k8s
	// provider deleting its PVC. Last, because the volume cannot be removed
	// while a container still references it.
	if c.SourceKind == model.SourceNone {
		name := VolumeName(c.WorkspaceName, c.Name)
		fmt.Fprintf(os.Stderr, "dev: removing volume %s\n", name)
		// --force: a folderless container that was never started has no
		// volume, and "no such volume" is not a failure to report.
		return runDocker(ctx, "volume", "rm", "--force", name)
	}
	return nil
}

func (p *Provider) Status(ctx context.Context, c model.Container) (model.Status, error) {
	if err := requireBinary(dockerBin); err != nil {
		return model.StatusAbsent, err
	}

	args := append([]string{"ps", "-a", "--format", "{{.State}}"}, dockerFilterArgs(c)...)
	out, err := output(ctx, dockerBin, args...)
	if err != nil {
		return model.StatusAbsent, err
	}
	switch first(out) {
	case "":
		return model.StatusAbsent, nil
	case "running":
		return model.StatusRunning, nil
	default:
		// created, exited, paused, restarting, dead: none of them can take an
		// exec, so they are all "stopped" as far as a caller is concerned.
		return model.StatusStopped, nil
	}
}

func (p *Provider) Logs(ctx context.Context, c model.Container, follow bool, out io.Writer) error {
	id, err := p.containerID(ctx, c)
	if err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("container %s does not exist yet", c.Name)
	}

	args := []string{"logs"}
	if follow {
		args = append(args, "-f")
	}
	args = append(args, id)

	cmd := exec.CommandContext(ctx, dockerBin, args...)
	// Container logs arrive on both streams; keep them apart rather than
	// merging, so a `| grep` over stdout still behaves.
	cmd.Stdout = out
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// containerID returns the engine's id for a container, or "" when there is
// none. Includes stopped containers: stop and remove both need to find one.
func (p *Provider) containerID(ctx context.Context, c model.Container) (string, error) {
	if err := requireBinary(dockerBin); err != nil {
		return "", err
	}
	args := append([]string{"ps", "-aq"}, dockerFilterArgs(c)...)
	out, err := output(ctx, dockerBin, args...)
	if err != nil {
		return "", err
	}
	return first(out), nil
}

// --- helpers -----------------------------------------------------------------

// remoteEnvArgs renders environment variables as devcontainer CLI arguments.
//
// --remote-env puts the value in this process's argv, where it is visible to
// anyone who can read the host's process list for the length of the call. The
// CLI's --secrets-file would avoid that, but it exists only on `up`, not on
// `exec`, so a split would make the two paths behave differently for no gain on
// the one that runs most often.
func remoteEnvArgs(env []provider.EnvVar) []string {
	var args []string
	for _, e := range env {
		args = append(args, "--remote-env", e.Key+"="+e.Value)
	}
	return args
}

func requireBinary(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("%s is required but not on PATH", name)
	}
	return nil
}

func runDocker(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, dockerBin, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// output runs a command and returns its stdout, quoting stderr on failure:
// docker reports why it refused there, and dropping it leaves only "exit 1".
func output(ctx context.Context, name string, args ...string) (string, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return string(out), nil
}

// first returns the first non-empty line, which is what every docker query here
// wants: the filters match one container, and a trailing newline is always
// present.
func first(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
