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

	"github.com/duy0611/dev-cli/internal/dcgen"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
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

// OverridesConfig marks this provider as one that takes a merged
// devcontainer.json. See provider.ConfigOverrider.
func (p *Provider) OverridesConfig() {}

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
	// Only for a project-owned container: a generated document already names
	// this volume in its own "mounts", and docker rejects the whole run with
	// "duplicate mount destination" if it arrives twice. The flag exists for
	// the container dev has no document for, since dev must not write into a
	// project's folder (invariant 9).
	//
	// On up and never on exec: the devcontainer CLI accepts --mount only here,
	// and an unknown flag would be a usage error on every command run inside
	// the container.
	if c.PersistState && c.GeneratedConfig == "" {
		args = append(args, "--mount", fmt.Sprintf("type=volume,source=%s,target=%s",
			StateVolumeName(c.WorkspaceName, c.Name), dcgen.StateDir))
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
	return p.claimStateDir(ctx, c)
}

// claimStateDir hands the state volume to the user the container runs as.
//
// Docker creates a named volume owned by root and, unlike a bind mount, it gets
// no UID remapping from the devcontainer CLI — so the remote user's first write
// there fails with permission denied. A generated configuration does this in its
// postCreateCommand; a container whose project ships its own configuration has
// no hook dev may write into (invariant 9), so it is done here instead.
//
// Only for a project-owned container. A generated one already chowns the
// directory in the postCreateCommand dcgen renders, and doing it twice would be
// a second answer to the same question.
//
// Guarded by a writability test rather than a marker file: it runs on every up,
// and a recursive chown over a state directory holding an agent's history is
// not something to repeat for nothing. The test is what makes it cheap; the
// chown behind it is what makes a fresh volume usable.
func (p *Provider) claimStateDir(ctx context.Context, c model.Container) error {
	if !c.PersistState || c.GeneratedConfig != "" {
		return nil
	}

	// Through sh -c because the ids are only known inside the container, and
	// Exec hands argv straight to the CLI without a shell to expand them. The
	// path is a constant, so there is nothing here to quote.
	var errBuf bytes.Buffer
	err := p.Exec(ctx, c,
		[]string{"sh", "-c", "[ -w " + dcgen.StateDir + " ] || " +
			"sudo chown -R $(id -u):$(id -g) " + dcgen.StateDir},
		provider.ExecOpts{Stdout: io.Discard, Stderr: &errBuf})
	if err != nil {
		return fmt.Errorf("claiming %s in container %s: %w: %s",
			dcgen.StateDir, c.Name, err, strings.TrimSpace(errBuf.String()))
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

	args := p.execArgs(c, command, opts.Env...)

	cmd := exec.CommandContext(ctx, devcontainerBin, args...)
	cmd.Stdin = opts.Stdin
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	return cmd.Run()
}

// execArgs builds the argument list for `devcontainer exec`.
//
// Shared with the agent relay, which starts a long-running exec of its own and
// must reach the same container: the id labels are what decide that, and a
// second copy of this list would be a second chance to get them wrong.
func (p *Provider) execArgs(c model.Container, command []string, env ...provider.EnvVar) []string {
	args := []string{"exec", "--workspace-folder", c.Source}
	args = append(args, idLabelArgs(c)...)
	// The config path is passed rather than inferred. A generated one lives in
	// a temporary directory the CLI's own lookup would never reach, and for a
	// project-owned one this is the path dcconfig already resolved.
	if c.ConfigPath != "" {
		args = append(args, "--config", c.ConfigPath)
	}
	args = append(args, remoteEnvArgs(env)...)
	return append(args, command...)
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
	var volumes []string
	if c.SourceKind == model.SourceNone {
		volumes = append(volumes, VolumeName(c.WorkspaceName, c.Name))
	}
	// Removed with the container, like the workspace volume: the operator asked
	// for the container to be gone, and a volume nothing references is a disk
	// nobody remembers filling.
	if c.PersistState {
		volumes = append(volumes, StateVolumeName(c.WorkspaceName, c.Name))
	}
	for _, name := range volumes {
		fmt.Fprintf(os.Stderr, "dev: removing volume %s\n", name)
		// --force: a container that was never started has no volume, and "no
		// such volume" is not a failure to report.
		if err := runDocker(ctx, "volume", "rm", "--force", name); err != nil {
			return err
		}
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
