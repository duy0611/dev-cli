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
	// Still needed on a k8s provider: the image is built on the host, and the
	// CLI shells out to docker even to read a configuration.
	dockerBin = "docker"
)

// Lifecycle holds the merged lifecycle commands, still encoded.
//
// Each element is a string, an array of strings, or an object of named
// commands — all three are legal and all three occur. They are decoded where
// they are run, not here, so that reading the configuration cannot fail over a
// command shape that would only have mattered later.
type Lifecycle struct {
	OnCreate      []json.RawMessage
	UpdateContent []json.RawMessage
	PostCreate    []json.RawMessage
	PostStart     []json.RawMessage
	PostAttach    []json.RawMessage
}

// readConfiguration asks the devcontainer CLI for the merged configuration.
//
// Merged, not raw: Features contribute lifecycle commands and environment of
// their own, and only the CLI knows how to combine them. Its merged form uses
// plural names holding arrays — onCreateCommands, not onCreateCommand — which
// is why this does not simply unmarshal devcontainer.json itself.
func readConfiguration(ctx context.Context, folder string) (DevConfig, Lifecycle, error) {
	if err := requireBinary(devcontainerBin); err != nil {
		return DevConfig{}, Lifecycle{}, err
	}
	// The CLI runs `docker ps` while reading a configuration, to see whether a
	// container already exists, and fails with "spawn docker ENOENT" without
	// it. Checked here so the error names the missing tool instead.
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
	if engine != dockerBin {
		args = append(args, "--docker-path", engine)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, devcontainerBin, args...)
	cmd.Stdout = &stdout
	// The CLI logs progress on stderr and puts the result on stdout; the log is
	// only interesting when something failed.
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return DevConfig{}, Lifecycle{}, fmt.Errorf("reading the devcontainer config: %w: %s",
			err, lastLine(stderr.String()))
	}

	var out struct {
		Merged struct {
			// Null when the configuration does not set one, which is the
			// common case.
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
		// The same default the devcontainer CLI uses when the configuration is
		// silent, so a project behaves the same on both providers.
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

// mergeEnv combines containerEnv and remoteEnv.
//
// The spec distinguishes them — one is set on the container, the other only for
// user commands — but here everything arrives through one Secret, so remoteEnv
// simply wins where they disagree, matching the order the CLI applies them.
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

// buildAndPush builds the image on the host and pushes it to the registry.
//
// The devcontainer CLI does the building because that is the part worth not
// reimplementing: Features, a Dockerfile, build args and the rest. It needs
// Docker, which is why a k8s provider still expects an engine on the operator's
// machine — docker or podman.
//
// How it builds depends on what the host has; see selectBuilder. A builder that
// cannot cross-build is only usable when the target platform is this host's
// own, because producing the wrong architecture silently is the worst outcome
// available — the pod crash-loops with "exec format error", which says nothing
// about the build.
func buildAndPush(ctx context.Context, folder, image, platform string, noCache bool, progress io.Writer) error {
	if err := requireBinary(devcontainerBin); err != nil {
		return err
	}

	b := selectBuilder(ctx)

	args := append([]string{"build", "--workspace-folder", folder, "--image-name", image},
		b.dockerPathArgs()...)
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
	// A build is minutes of silence otherwise, so its progress goes straight
	// through; only the CLI's own result JSON on stdout is dropped.
	cmd.Stdout = io.Discard
	cmd.Stderr = progressOr(progress)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("building %s: %w", image, err)
	}
	if b.pushWithCLI {
		return nil // --push already sent it
	}

	push := exec.CommandContext(ctx, b.engine, "push", image)
	push.Stdout = io.Discard
	push.Stderr = progressOr(progress)
	if err := push.Run(); err != nil {
		return fmt.Errorf("pushing %s: %w", image, err)
	}
	return nil
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

// lastLine returns the final non-empty line, which for the CLI's JSON log is
// the record that explains the failure.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}
