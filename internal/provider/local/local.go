// Package local runs devcontainers on a Docker-compatible engine, by driving
// the devcontainer CLI and docker rather than reimplementing either.
package local

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/duy0611/dev-cli/internal/dcgen"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/seed"
)

// The two binaries this provider drives. docker is used through its CLI, not
// the Go SDK, so DOCKER_HOST picks the engine, podman's socket included.
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

// Up creates or starts the container. The CLI does both in one call.
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

	// An isolated workspace volume starts empty, so on first create the
	// lifecycle hooks are skipped and run after seeding. The CLI writes no
	// marker for a skipped hook, so run-user-commands runs each once. A
	// recreate is never seeded: seeding unpacks over the workspace and would
	// destroy agent work.
	seeding := false
	if c.Mode == model.ModeIsolated && !recreate {
		id, err := p.containerID(ctx, c)
		if err != nil {
			return err
		}
		seeding = id == ""
	}

	args := []string{"up", "--workspace-folder", c.Source}
	args = append(args, idLabelArgs(c)...)
	// Passed explicitly: a generated config lives in a temporary directory the
	// CLI's own lookup never reaches.
	if c.ConfigPath != "" {
		args = append(args, "--config", c.ConfigPath)
	}
	// --override-config replaces the document rather than merging, so dev does
	// the merge. --config stays alongside: it anchors a relative "dockerfile"
	// to the project.
	if c.OverrideConfigPath != "" {
		args = append(args, "--override-config", c.OverrideConfigPath)
	}
	if recreate {
		args = append(args, "--remove-existing-container")
	}
	if noCache {
		args = append(args, "--build-no-cache")
	}
	if seeding {
		args = append(args, "--skip-post-create")
	}
	args = append(args, remoteEnvArgs(env)...)

	cmd := exec.CommandContext(ctx, devcontainerBin, args...)
	// stdout is the JSON result, which is noise. Progress on stderr is kept,
	// since an image build is minutes of silence otherwise.
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("starting container %s: %w", c.Name, err)
	}
	if err := p.claimStateDir(ctx, c); err != nil {
		return err
	}
	if !seeding {
		return nil
	}
	if err := p.seedWorkspace(ctx, c); err != nil {
		return err
	}
	return p.runUserCommands(ctx, c, env)
}

// seedWorkspace copies each folder into its volume, primary first, with each
// repository rebuilt from a bundle so no host .git config or hooks come along.
// Only up on first create calls it, since it unpacks over the workspace.
func (p *Provider) seedWorkspace(ctx context.Context, c model.Container) error {
	if err := p.seedFolder(ctx, c, c.Source, WorkspaceFolderOf(c)); err != nil {
		return err
	}
	for _, f := range c.Folders {
		if err := p.seedFolder(ctx, c, f.HostPath, f.ContainerPath); err != nil {
			return err
		}
	}
	return nil
}

// seedFolder copies one host folder into the container at dir.
func (p *Provider) seedFolder(ctx context.Context, c model.Container, host, dir string) error {
	// The volume is root-owned, and the postCreateCommand that would chown it
	// is held back by this seeding up, so claim the folder before unpacking.
	// Through sh so the ids are the container's.
	var claimErr bytes.Buffer
	if err := p.Exec(ctx, c,
		[]string{"sh", "-c", `[ -w "$1" ] || sudo chown -R "$(id -u):$(id -g)" "$1"`, "claim", dir},
		provider.ExecOpts{Stdout: io.Discard, Stderr: &claimErr}); err != nil {
		return fmt.Errorf("claiming %s in container %s: %w: %s",
			dir, c.Name, err, strings.TrimSpace(claimErr.String()))
	}

	pr, pw := io.Pipe()
	var plan seed.Plan
	go func() {
		var err error
		plan, err = seed.Archive(ctx, host, pw)
		_ = pw.CloseWithError(err)
	}()
	// -p keeps the modes the archive carries; without it the container's umask
	// strips the executable bit off every script.
	var stderr bytes.Buffer
	if err := p.Exec(ctx, c, []string{"tar", "-x", "-p", "-C", dir},
		provider.ExecOpts{Stdin: pr, Stdout: io.Discard, Stderr: &stderr}); err != nil {
		_ = pr.CloseWithError(err)
		return fmt.Errorf("copying %s into container %s: %w: %s",
			host, c.Name, err, strings.TrimSpace(stderr.String()))
	}
	stderr.Reset()
	if err := p.Exec(ctx, c, append([]string{"sh", "-c", seed.Script}, plan.ScriptArgs(dir)...),
		provider.ExecOpts{Stdout: io.Discard, Stderr: &stderr}); err != nil {
		return fmt.Errorf("building the repository of %s in container %s: %w: %s",
			host, c.Name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// runUserCommands runs the lifecycle hooks that the seeding up held back.
func (p *Provider) runUserCommands(ctx context.Context, c model.Container, env []provider.EnvVar) error {
	args := []string{"run-user-commands", "--workspace-folder", c.Source}
	args = append(args, idLabelArgs(c)...)
	if c.ConfigPath != "" {
		args = append(args, "--config", c.ConfigPath)
	}
	if c.OverrideConfigPath != "" {
		args = append(args, "--override-config", c.OverrideConfigPath)
	}
	args = append(args, remoteEnvArgs(env)...)
	cmd := exec.CommandContext(ctx, devcontainerBin, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running the lifecycle commands in container %s: %w", c.Name, err)
	}
	return nil
}

// claimStateDir hands the state volume to the remote user. Docker creates named
// volumes root-owned, and only a generated configuration has a
// postCreateCommand to chown them, so a project-owned container is claimed here
// on every up. The writability test skips a recursive chown over existing agent
// history.
func (p *Provider) claimStateDir(ctx context.Context, c model.Container) error {
	if !c.PersistState || c.GeneratedConfig != "" {
		return nil
	}

	// sh -c: the ids are only known inside the container, and Exec passes argv
	// without a shell to expand them.
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

// execArgs builds the argument list for `devcontainer exec`. The agent relay
// shares it so that it reaches the same container.
func (p *Provider) execArgs(c model.Container, command []string, env ...provider.EnvVar) []string {
	args := []string{"exec", "--workspace-folder", c.Source}
	args = append(args, idLabelArgs(c)...)
	// Passed explicitly, as in up: a generated config lives in a temporary
	// directory.
	if c.ConfigPath != "" {
		args = append(args, "--config", c.ConfigPath)
	}
	// Must match up's document, or exec sees an empty /var/dev-state with no
	// error.
	if c.OverrideConfigPath != "" {
		args = append(args, "--override-config", c.OverrideConfigPath)
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
		return nil
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

	// docker rm leaves named volumes behind, so they are removed here. Last,
	// because a volume still referenced by a container cannot be removed.
	var volumes []string
	if c.SourceKind == model.SourceNone || c.Mode == model.ModeIsolated {
		volumes = append(volumes, VolumeName(c.WorkspaceName, c.Name))
	}
	if c.Mode == model.ModeIsolated {
		for _, f := range c.Folders {
			volumes = append(volumes, FolderVolumeName(c.WorkspaceName, c.Name, f.HostPath))
		}
	}
	if c.PersistState {
		volumes = append(volumes, StateVolumeName(c.WorkspaceName, c.Name))
	}
	for _, name := range volumes {
		fmt.Fprintf(os.Stderr, "dev: removing volume %s\n", name)
		// --force: a never-started container has no volume to report missing.
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
		// Every other state cannot take an exec, so it reads as stopped.
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
	// Keep the container's stderr off stdout, so a pipe over stdout still
	// works.
	cmd.Stdout = out
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Mounts reports the mounts an existing container was created with.
func (p *Provider) Mounts(ctx context.Context, c model.Container) ([]provider.Mount, bool, error) {
	id, err := p.containerID(ctx, c)
	if err != nil || id == "" {
		return nil, false, err
	}
	out, err := output(ctx, dockerBin, "inspect", "--format", "{{json .Mounts}}", id)
	if err != nil {
		return nil, true, err
	}
	var raw []struct {
		Destination string
		RW          bool
	}
	// Empty or null output means no mounts.
	if line := first(out); line != "" && line != "null" {
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			return nil, true, fmt.Errorf("reading the mounts of container %s: %w", c.Name, err)
		}
	}
	mounts := make([]provider.Mount, len(raw))
	for i, m := range raw {
		mounts[i] = provider.Mount{Destination: m.Destination, ReadOnly: !m.RW}
	}
	return mounts, true, nil
}

// containerID returns the id of the container, stopped or running, or "" if
// none.
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
// --remote-env values are visible in the host process list for the length of
// the call. --secrets-file would avoid that, but exists only on up, not exec.
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

// output returns stdout, and on failure folds in stderr, where docker says why.
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

// first returns the first non-empty line of a docker query, which matches one
// container.
func first(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
