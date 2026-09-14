package k8s

import (
	"context"
	"os/exec"
	"regexp"
	"runtime"
)

const podmanBin = "podman"

// hostArch is what an image built without an explicit platform comes out as.
// A variable so a test can pretend to be on the other architecture.
var hostArch = runtime.GOARCH

// The devcontainer CLI decides whether BuildKit is available by running
// `<docker-path> buildx version` and looking for a semver anywhere in the
// output. Probing the same way, with the same rule, is what makes this
// agreement rather than a guess.
var semver = regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+`)

// builder is how the image gets built and pushed on this host.
type builder struct {
	// dockerPath is passed to the CLI as --docker-path. Empty means its
	// default, docker.
	dockerPath string
	// engine is the binary to run directly, for the push.
	engine string
	// crossBuild says whether --platform may be passed. The CLI refuses it
	// outright without BuildKit.
	crossBuild bool
	// pushWithCLI says whether --push works, saving a separate step.
	pushWithCLI bool
}

// selectBuilder picks the best available way to produce the image.
//
// Three real configurations, in order of preference:
//
//   - docker with the buildx plugin: cross-builds and pushes in one step.
//   - podman: satisfies the CLI's BuildKit probe, because `podman buildx
//     version` prints a version, and `podman buildx build` is an alias for
//     `podman build`, which honours --platform. It has no --push, so the image
//     is pushed separately.
//   - docker without buildx: neither, so the image can only be built for this
//     host's architecture.
//
// Preferring docker+buildx is not only about the single step: building
// Features under rootless podman is known to fail on the bind mount the
// generated Dockerfile uses (devcontainers/cli#548).
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
	// Nothing to build with. The caller reports the missing binary by name
	// rather than this pretending otherwise.
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

// args returns the --docker-path pair, if this builder needs one.
func (b builder) dockerPathArgs() []string {
	if b.dockerPath == "" {
		return nil
	}
	return []string{"--docker-path", b.dockerPath}
}

// engineBin is the engine the devcontainer CLI should be pointed at for
// anything other than a build.
//
// The CLI shells out to it even to read a configuration, so a host with only
// podman needs this too.
func engineBin() string {
	if available(dockerBin) {
		return dockerBin
	}
	if available(podmanBin) {
		return podmanBin
	}
	return dockerBin
}
