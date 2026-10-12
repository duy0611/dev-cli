package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	devcontainerBin = "devcontainer"
	// The CLI shells out to docker even to read a configuration, so it is
	// needed here too.
	dockerBin = "docker"
)

// Lifecycle holds the merged lifecycle commands as raw JSON. Each element may
// be a string, an array or an object, and is decoded where it runs.
type Lifecycle struct {
	OnCreate      []json.RawMessage
	UpdateContent []json.RawMessage
	PostCreate    []json.RawMessage
	PostStart     []json.RawMessage
	PostAttach    []json.RawMessage
}

// readConfiguration returns the merged configuration from the devcontainer CLI.
// Only the CLI combines Feature lifecycle commands, and its plural keys
// (onCreateCommands) do not match devcontainer.json.
func readConfiguration(ctx context.Context, folder, configPath string) (DevConfig, Lifecycle, error) {
	if err := requireBinary(devcontainerBin); err != nil {
		return DevConfig{}, Lifecycle{}, err
	}
	// The CLI runs `docker ps` even to read a configuration and fails with
	// "spawn docker ENOENT" without it, so check here for a clearer error.
	engine := engineBin()
	if err := requireBinary(engine); err != nil {
		return DevConfig{}, Lifecycle{}, fmt.Errorf("reading the devcontainer config: %w", err)
	}

	args := []string{
		"read-configuration",
		"--workspace-folder", folder,
		"--include-merged-configuration",
		"--log-format", "json",
	}
	// Passed, not inferred: a generated configuration lives in a temp directory
	// the CLI does not search.
	if configPath != "" {
		args = append(args, "--config", configPath)
	}
	if engine != dockerBin {
		args = append(args, "--docker-path", engine)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, devcontainerBin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return DevConfig{}, Lifecycle{}, fmt.Errorf("reading the devcontainer config: %w: %s",
			err, lastLine(stderr.String()))
	}

	var out struct {
		Merged struct {
			// Null when unset.
			WorkspaceFolder *string           `json:"workspaceFolder"`
			RemoteUser      string            `json:"remoteUser"`
			ContainerEnv    map[string]string `json:"containerEnv"`
			RemoteEnv       map[string]string `json:"remoteEnv"`

			OnCreateCommands      []json.RawMessage `json:"onCreateCommands"`
			UpdateContentCommands []json.RawMessage `json:"updateContentCommands"`
			PostCreateCommands    []json.RawMessage `json:"postCreateCommands"`
			PostStartCommands     []json.RawMessage `json:"postStartCommands"`
			PostAttachCommands    []json.RawMessage `json:"postAttachCommands"`
		} `json:"mergedConfiguration"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return DevConfig{}, Lifecycle{}, fmt.Errorf("reading the devcontainer config: %w", err)
	}

	dev := DevConfig{
		RemoteUser:   out.Merged.RemoteUser,
		ContainerEnv: mergeEnv(out.Merged.ContainerEnv, out.Merged.RemoteEnv),
	}
	if out.Merged.WorkspaceFolder != nil && *out.Merged.WorkspaceFolder != "" {
		dev.WorkspaceFolder = *out.Merged.WorkspaceFolder
	} else {
		// Same default as the devcontainer CLI, so both providers agree.
		dev.WorkspaceFolder = "/workspaces/" + filepath.Base(strings.TrimRight(folder, "/"))
	}

	lc := Lifecycle{
		OnCreate:      out.Merged.OnCreateCommands,
		UpdateContent: out.Merged.UpdateContentCommands,
		PostCreate:    out.Merged.PostCreateCommands,
		PostStart:     out.Merged.PostStartCommands,
		PostAttach:    out.Merged.PostAttachCommands,
	}
	return dev, lc, nil
}

// mergeEnv flattens containerEnv and remoteEnv into one map. remoteEnv wins
// where they disagree, as it does in the CLI.
func mergeEnv(containerEnv, remoteEnv map[string]string) map[string]string {
	out := make(map[string]string, len(containerEnv)+len(remoteEnv))
	for k, v := range containerEnv {
		out[k] = v
	}
	for k, v := range remoteEnv {
		out[k] = v
	}
	return out
}

// buildAndPush builds the image on the host with the devcontainer CLI and
// pushes it. That needs docker or podman locally. A builder that cannot
// cross-build is refused for a foreign platform: the wrong architecture would
// build silently and crash-loop in the pod with "exec format error".
func buildAndPush(ctx context.Context, folder, configPath, image, platform string, noCache bool, progress io.Writer) error {
	if err := requireBinary(devcontainerBin); err != nil {
		return err
	}

	b := selectBuilder(ctx)

	args := append([]string{"build", "--workspace-folder", folder, "--image-name", image},
		b.dockerPathArgs()...)
	// build takes no --override-config: --config is the only way to pass a
	// generated configuration.
	if configPath != "" {
		args = append(args, "--config", configPath)
	}
	if noCache {
		args = append(args, "--no-cache")
	}
	if b.crossBuild {
		args = append(args, "--platform", platform)
	} else if err := requireHostPlatform(platform); err != nil {
		return err
	}
	if b.pushWithCLI {
		args = append(args, "--push")
	}

	cmd := exec.CommandContext(ctx, devcontainerBin, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = progressOr(progress)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("building %s: %w", image, err)
	}
	if b.pushWithCLI {
		return nil // --push already sent it
	}

	// Teed so the failure text can be read afterwards: the exit status alone
	// does not say a push was rejected.
	var captured bytes.Buffer
	push := exec.CommandContext(ctx, b.engine, "push", image)
	push.Stdout = io.Discard
	push.Stderr = io.MultiWriter(progressOr(progress), &captured)
	if err := push.Run(); err != nil {
		return pushError(b.engine, image, captured.String(), err)
	}
	return nil
}

// pushError explains a rejected push. The engine reports an auth failure only
// as "exit status 125", so the stderr text is what identifies it.
func pushError(engine, image, stderr string, err error) error {
	if isAuthFailure(stderr) {
		return fmt.Errorf("pushing %s: the registry refused the credentials. Run: %s login %s",
			image, engine, registryHost(image))
	}
	if msg := strings.TrimSpace(lastLine(stderr)); msg != "" {
		return fmt.Errorf("pushing %s: %s", image, msg)
	}
	return fmt.Errorf("pushing %s: %w", image, err)
}

func isAuthFailure(stderr string) bool {
	s := strings.ToLower(stderr)
	for _, sign := range []string{"401", "403", "unauthorized", "forbidden", "authentication required"} {
		if strings.Contains(s, sign) {
			return true
		}
	}
	return false
}

// registryHost returns the registry a login applies to, or "" for a Docker Hub
// name, where login takes no argument.
func registryHost(image string) string {
	head, _, found := strings.Cut(image, "/")
	if !found {
		return ""
	}
	// The first segment is a registry only if it looks like a host.
	if strings.ContainsAny(head, ".:") || head == "localhost" {
		return head
	}
	return ""
}

// requireHostPlatform refuses a build that would quietly produce the wrong
// architecture.
func requireHostPlatform(platform string) error {
	_, arch, found := strings.Cut(platform, "/")
	if !found {
		arch = platform
	}
	if arch == hostArch {
		return nil
	}
	return fmt.Errorf(
		"cannot build for %s on a %s host: no cross-architecture builder found. "+
			"Install the docker buildx plugin (Docker Desktop includes it) or podman, "+
			"or set the provider's platform to linux/%s if the cluster nodes are %s",
		platform, hostArch, hostArch, hostArch)
}

func progressOr(w io.Writer) io.Writer {
	if w == nil {
		return os.Stderr
	}
	return w
}

// lastLine returns the final non-empty line: for the CLI's JSON log, the record
// that explains a failure.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}
