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
	"runtime"
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
	if err := requireBinary(dockerBin); err != nil {
		return DevConfig{}, Lifecycle{}, fmt.Errorf("reading the devcontainer config: %w", err)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, devcontainerBin,
		"read-configuration",
		"--workspace-folder", folder,
		"--include-merged-configuration",
		"--log-format", "json")
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

// hostArch is what an image built without an explicit platform comes out as.
// A variable so a test can pretend to be on the other architecture.
var hostArch = runtime.GOARCH

// buildxAvailable reports whether the engine has the buildx plugin.
//
// The devcontainer CLI refuses --platform and --push outright without BuildKit
// ("--platform or --push require BuildKit enabled"), so this decides which of
// the two build paths is even possible. Docker Desktop ships buildx; Podman and
// a bare docker CLI often do not.
func buildxAvailable(ctx context.Context) bool {
	if _, err := exec.LookPath(dockerBin); err != nil {
		return false
	}
	return exec.CommandContext(ctx, dockerBin, "buildx", "version").Run() == nil
}

// buildAndPush builds the image on the host and pushes it to the registry.
//
// The devcontainer CLI does the building because that is the part worth not
// reimplementing: Features, a Dockerfile, build args and the rest. It needs
// Docker, which is why a k8s provider still expects an engine on the operator's
// machine.
//
// Two paths, because the CLI's --platform and --push both require BuildKit:
//
//   - with buildx, it builds for the target platform and pushes in one step;
//   - without it, the image can only be built for the host's own architecture,
//     so the CLI loads it locally and docker pushes it afterwards.
//
// The second path cannot honour a cross-architecture request, and producing the
// wrong architecture silently is the worst outcome available: the pod
// crash-loops with "exec format error", which says nothing about the build. So
// that combination is refused with the two ways out.
func buildAndPush(ctx context.Context, folder, image, platform string, noCache bool, progress io.Writer) error {
	if err := requireBinary(devcontainerBin); err != nil {
		return err
	}

	args := []string{"build", "--workspace-folder", folder, "--image-name", image}
	if noCache {
		args = append(args, "--no-cache")
	}

	useBuildx := buildxAvailable(ctx)
	if useBuildx {
		args = append(args, "--platform", platform, "--push")
	} else if err := requireHostPlatform(platform); err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, devcontainerBin, args...)
	// A build is minutes of silence otherwise, so its progress goes straight
	// through; only the CLI's own result JSON on stdout is dropped.
	cmd.Stdout = io.Discard
	cmd.Stderr = progressOr(progress)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("building %s: %w", image, err)
	}
	if useBuildx {
		return nil // --push already sent it
	}

	push := exec.CommandContext(ctx, dockerBin, "push", image)
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
		"cannot build for %s on a %s host without buildx: install the docker buildx "+
			"plugin (Docker Desktop includes it), or set the provider's platform to "+
			"linux/%s if the cluster nodes are %s",
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
