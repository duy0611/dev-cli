package k8s

import (
	"context"
	"os/exec"
	"regexp"
	"runtime"
)

const podmanBin = "podman"

// hostArch is the host architecture; a variable so a test can override it.
var hostArch = runtime.GOARCH

// Matches the devcontainer CLI's BuildKit probe: any semver in the output of
// `<docker-path> buildx version`.
var semver = regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+`)

// builder is how the image gets built and pushed on this host.
type builder struct {
	// dockerPath is the CLI's --docker-path. Empty means docker.
	dockerPath string
	// engine is the binary to run directly, for the push.
	engine string
	// crossBuild permits --platform. The CLI refuses it without BuildKit.
	crossBuild bool
	// pushWithCLI permits --push, which saves a separate push.
	pushWithCLI bool
}

// selectBuilder picks how to build and push, in order of preference:
//
//   - docker with buildx: cross-builds and pushes in one step.
//   - podman: passes the CLI's BuildKit probe, and `buildx build` is an alias for
//     `build`, which honours --platform. It has no --push, so the push is separate.
//   - docker alone: builds only for this host's architecture.
//
// Docker with buildx is preferred to podman because Features under rootless
// podman fail on the Dockerfile's bind mount (devcontainers/cli#548).
func selectBuilder(ctx context.Context) builder {
	if hasBuildx(ctx, dockerBin) {
		return builder{engine: dockerBin, crossBuild: true, pushWithCLI: true}
	}
	if hasBuildx(ctx, podmanBin) {
		return builder{dockerPath: podmanBin, engine: podmanBin, crossBuild: true}
	}
	if available(dockerBin) {
		return builder{engine: dockerBin}
	}
	if available(podmanBin) {
		return builder{dockerPath: podmanBin, engine: podmanBin}
	}
	// Nothing to build with. The caller reports the missing binary by name.
	return builder{engine: dockerBin}
}

// hasBuildx reports whether the CLI would find BuildKit through this binary.
func hasBuildx(ctx context.Context, bin string) bool {
	if !available(bin) {
		return false
	}
	out, err := exec.CommandContext(ctx, bin, "buildx", "version").Output()
	if err != nil {
		return false
	}
	return semver.Match(out)
}

func available(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// dockerPathArgs returns the --docker-path pair, if this builder needs one.
func (b builder) dockerPathArgs() []string {
	if b.dockerPath == "" {
		return nil
	}
	return []string{"--docker-path", b.dockerPath}
}

// engineBin is the engine the devcontainer CLI is pointed at for non-build
// calls. The CLI needs it even to read a configuration, so podman-only hosts
// need it too.
func engineBin() string {
	if available(dockerBin) {
		return dockerBin
	}
	if available(podmanBin) {
		return podmanBin
	}
	return dockerBin
}
