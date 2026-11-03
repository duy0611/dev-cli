//go:build smoke

// Package smoke drives the real binary against a real container engine. Run it
// with `make smoke`, which needs the devcontainer CLI, an engine and network
// access.
package smoke

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/duy0611/dev-cli/internal/provider/local"
)

// A tiny image, so the pull is seconds rather than minutes.
const fixtureConfig = `{
  "name": "dev-smoke",
  "image": "docker.io/library/alpine:3.20"
}`

const (
	workspaceName = "dev-smoke-ws"
	containerName = "dev-smoke"
	providerName  = "dev-smoke-local"
)

func TestSmoke(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker")

	// A state directory of its own, never the operator's database.
	state := t.TempDir()
	t.Setenv("DEV_STATE", state)

	bin := buildBinary(t)
	project := fixtureProject(t)

	dev := func(args ...string) string {
		t.Helper()
		return run(t, bin, args...)
	}

	// Runs even when an assertion fails, so the next run finds no stale
	// container.
	t.Cleanup(func() {
		_ = exec.Command(bin, "container", "remove", containerName, "--force").Run()
	})

	dev("provider", "configure", providerName, "--kind", "local")
	dev("workspace", "init", workspaceName, "--provider", providerName)
	dev("workspace", "set", "GREETING", "literal:hello")

	t.Log("creating the container; the first run pulls an image")
	dev("container", "create", containerName, "--folder", project)

	// Both labels must match, or the container is not isolated from others.
	ids := dockerPS(t, "-q",
		"--filter", "label=dev.workspace="+workspaceName,
		"--filter", "label=dev.container="+containerName)
	if len(ids) != 1 {
		t.Fatalf("docker matched %d containers on the dev.* labels, want 1: %v", len(ids), ids)
	}

	if got := strings.TrimSpace(dev("container", "exec", containerName, "--", "printenv", "GREETING")); got != "hello" {
		t.Errorf("workspace setting did not reach the container: printenv GREETING = %q, want %q", got, "hello")
	}

	// The record is what `list` prints; the status comes from the engine.
	if out := dev("container", "list"); !strings.Contains(out, containerName) || !strings.Contains(out, "running") {
		t.Errorf("container list does not show a running %s:\n%s", containerName, out)
	}

	dev("container", "stop", containerName)
	if out := dev("container", "list"); strings.Contains(out, "running") {
		t.Errorf("container still reads as running after stop:\n%s", out)
	}

	dev("container", "remove", containerName)

	if ids := dockerPS(t, "-aq", "--filter", "label=dev.container="+containerName); len(ids) != 0 {
		t.Errorf("remove left %d containers behind: %v", len(ids), ids)
	}
	if out := dev("container", "list"); strings.Contains(out, containerName) {
		t.Errorf("remove left the record behind:\n%s", out)
	}
}

// --- helpers -------------------------------------------------------------------

// requireAnyBinary skips unless at least one of the named binaries is on PATH.
func requireAnyBinary(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err == nil {
			return
		}
	}
	t.Skipf("none of %v is on PATH", names)
}

func requireBinaries(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s is not on PATH; the smoke test needs a real engine", n)
		}
	}
}

// buildBinary compiles the CLI under test, never a stale dist/dev.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "dev")

	cmd := exec.Command("go", "build", "-o", bin, "../../cmd/dev")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the binary: %v\n%s", err, out)
	}
	return bin
}

func fixtureProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".devcontainer"), 0o755); err != nil {
		t.Fatalf("creating the fixture: %v", err)
	}
	path := filepath.Join(dir, ".devcontainer", "devcontainer.json")
	if err := os.WriteFile(path, []byte(fixtureConfig), 0o600); err != nil {
		t.Fatalf("writing the fixture config: %v", err)
	}
	return dir
}

func run(t *testing.T, bin string, args ...string) string {
	t.Helper()

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Only a cold image pull is slow.
	timer := time.AfterFunc(10*time.Minute, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	defer timer.Stop()

	if err := cmd.Run(); err != nil {
		t.Fatalf("dev %s: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func dockerPS(t *testing.T, args ...string) []string {
	t.Helper()

	out, err := exec.Command("docker", append([]string{"ps"}, args...)...).Output()
	if err != nil {
		t.Fatalf("docker ps: %v", err)
	}
	var ids []string
	for _, line := range strings.Split(string(out), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			ids = append(ids, s)
		}
	}
	return ids
}

// dockerVolumes returns the volume names matching a name filter.
func dockerVolumes(t *testing.T, name string) []string {
	t.Helper()
	out, err := exec.Command("docker", "volume", "ls", "-q", "--filter", "name="+name).Output()
	if err != nil {
		t.Fatalf("docker volume ls: %v", err)
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names
}

// A folder with no configuration of its own, to exercise --generate.
func TestSmokeGeneratedConfig(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker")

	state := t.TempDir()
	t.Setenv("DEV_STATE", state)

	bin := buildBinary(t)
	project := t.TempDir() // deliberately empty

	dev := func(args ...string) string {
		t.Helper()
		return run(t, bin, args...)
	}

	const name = "dev-smoke-gen"
	t.Cleanup(func() {
		_ = exec.Command(bin, "container", "remove", name, "--force").Run()
	})

	dev("provider", "configure", "dev-smoke-gen-local", "--kind", "local")
	dev("workspace", "init", "dev-smoke-gen-ws", "--provider", "dev-smoke-gen-local")

	t.Log("creating a generated container; the first run pulls a base image and installs features")
	dev("container", "create", name, "--folder", project, "--generate", "--tools", "yq")

	// The tool the operator asked for has to actually be in the container: a
	// feature reference that resolves is not the same as one that installs.
	//
	// yq rather than something the base image already ships — an assertion on a
	// tool that is present either way would pass with the feature omitted
	// entirely, and prove nothing.
	if out := dev("container", "exec", name, "--", "yq", "--version"); !strings.Contains(out, "yq") {
		t.Errorf("yq is not installed in the generated container: %q", out)
	}

	// The project folder is dev's to read, never to write.
	entries, err := os.ReadDir(project)
	if err != nil {
		t.Fatalf("reading the project folder: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("dev wrote into the project folder: %v", entries)
	}

	if out := dev("container", "config", "show", name); !strings.Contains(out, "apt-get-packages") {
		t.Errorf("config show did not print the stored configuration:\n%s", out)
	}

	dev("container", "remove", name)
}

// --from copies the source's tool list, and the clone gets its own volumes.
func TestSmokeCreateFrom(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker")

	state := t.TempDir()
	t.Setenv("DEV_STATE", state)

	bin := buildBinary(t)
	dev := func(args ...string) string {
		t.Helper()
		return run(t, bin, args...)
	}

	const (
		src   = "dev-smoke-from-src"
		clone = "dev-smoke-from-clone"
		ws    = "dev-smoke-from-ws"
	)
	t.Cleanup(func() {
		for _, name := range []string{clone, src} {
			_ = exec.Command(bin, "container", "remove", name, "--force").Run()
			_ = exec.Command("docker", "volume", "rm", "--force",
				"dev-"+ws+"-"+name, "dev-"+ws+"-"+name+"-state").Run()
		}
	})

	dev("provider", "configure", "dev-smoke-from-local", "--kind", "local")
	dev("workspace", "init", ws, "--provider", "dev-smoke-from-local")

	// Recorded only; the source never runs.
	dev("container", "create", src, "--no-folder", "--generate", "--tools", "yq", "--no-start")

	t.Log("creating a container --from another; the first run pulls a base image and installs features")
	dev("container", "create", clone, "--from", src, "--no-folder")

	// yq is not in the base image, so finding it proves the tool list was
	// copied.
	if out := dev("container", "exec", clone, "--", "yq", "--version"); !strings.Contains(out, "yq") {
		t.Errorf("yq is not installed in the clone: %q", out)
	}
	if out := dockerVolumes(t, "dev-"+ws+"-"+src); len(out) != 0 {
		t.Errorf("starting the clone created the source's volume: %v", out)
	}

	dev("container", "remove", clone)
	dev("container", "remove", src)
}

// Work in a folderless container survives a stop and start, because it lives in
// a volume.
func TestSmokeFolderless(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker")

	state := t.TempDir()
	t.Setenv("DEV_STATE", state)

	bin := buildBinary(t)
	dev := func(args ...string) string {
		t.Helper()
		return run(t, bin, args...)
	}

	const (
		name = "dev-smoke-none"
		ws   = "dev-smoke-none-ws"
	)
	t.Cleanup(func() {
		_ = exec.Command(bin, "container", "remove", name, "--force").Run()
		// A surviving volume would hand the next run the previous run's files.
		_ = exec.Command("docker", "volume", "rm", "--force", "dev-"+ws+"-"+name).Run()
	})

	dev("provider", "configure", "dev-smoke-none-local", "--kind", "local")
	dev("workspace", "init", ws, "--provider", "dev-smoke-none-local")

	t.Log("creating a folderless container; the first run pulls a base image")
	dev("container", "create", name, "--no-folder")

	dev("container", "exec", name, "--", "sh", "-c", "echo persisted > /workspaces/"+name+"/marker")

	dev("container", "stop", name)
	dev("container", "start", name)

	got := strings.TrimSpace(dev("container", "exec", name, "--", "cat", "/workspaces/"+name+"/marker"))
	if got != "persisted" {
		t.Errorf("the workspace did not survive a stop and start: marker = %q, want %q", got, "persisted")
	}

	// dev owns the volume and must remove it.
	volume := "dev-" + ws + "-" + name
	dev("container", "remove", name)
	if out := dockerVolumes(t, volume); len(out) != 0 {
		t.Errorf("remove left the volume behind: %v", out)
	}
}

// An agent's configuration outlives a rebuild. A rebuild, not a stop and start,
// is what discards the container filesystem and so tests the mount.
func TestSmokePersistState(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker")

	state := t.TempDir()
	t.Setenv("DEV_STATE", state)

	bin := buildBinary(t)
	dev := func(args ...string) string {
		t.Helper()
		return run(t, bin, args...)
	}

	const (
		name = "dev-smoke-state"
		ws   = "dev-smoke-state-ws"
	)
	stateVolume := "dev-" + ws + "-" + name + "-state"
	t.Cleanup(func() {
		_ = exec.Command(bin, "container", "remove", name, "--force").Run()
		_ = exec.Command("docker", "volume", "rm", "--force", "dev-"+ws+"-"+name).Run()
		_ = exec.Command("docker", "volume", "rm", "--force", stateVolume).Run()
	})

	dev("provider", "configure", "dev-smoke-state-local", "--kind", "local")
	dev("workspace", "init", ws, "--provider", "dev-smoke-state-local")

	t.Log("creating a container; persisting the agents' state is the default")
	dev("container", "create", name, "--no-folder")

	// Through the variable, so a missing CLAUDE_CONFIG_DIR fails the read after
	// rebuild.
	dev("container", "exec", name, "--", "sh", "-c",
		`mkdir -p "$CLAUDE_CONFIG_DIR" && echo plugin > "$CLAUDE_CONFIG_DIR/marker"`)
	dev("container", "exec", name, "--", "git", "config", "--global", "alias.st", "status")

	t.Log("rebuilding; this is what used to lose the configuration")
	dev("container", "rebuild", name)

	got := strings.TrimSpace(dev("container", "exec", name, "--", "sh", "-c",
		`cat "$CLAUDE_CONFIG_DIR/marker"`))
	if got != "plugin" {
		t.Errorf("the agent's configuration did not survive a rebuild: marker = %q, want %q",
			got, "plugin")
	}

	alias := strings.TrimSpace(dev("container", "exec", name, "--",
		"git", "config", "--global", "alias.st"))
	if alias != "status" {
		t.Errorf("the global gitconfig did not survive a rebuild: alias.st = %q, want %q",
			alias, "status")
	}

	// A volume arrives root-owned; the postCreate chown makes it writable by
	// the remote user.
	owner := strings.TrimSpace(dev("container", "exec", name, "--", "sh", "-c",
		`[ -w "$CLAUDE_CONFIG_DIR" ] && echo writable`))
	if owner != "writable" {
		t.Errorf("the state directory is not writable by the remote user: %q", owner)
	}

	dev("container", "remove", name)
	if out := dockerVolumes(t, stateVolume); len(out) != 0 {
		t.Errorf("remove left the state volume behind: %v", out)
	}
}

// A copy shares no image tag with its source: it starts from the image ID the
// source's container used, and a rebuild of either leaves the other alone.
func TestSmokeCreateFromCopy(t *testing.T) {
	requireBinaries(t, "devcontainer", "docker")

	t.Setenv("DEV_STATE", t.TempDir())

	bin := buildBinary(t)
	dev := func(args ...string) string {
		t.Helper()
		return run(t, bin, args...)
	}

	const (
		src   = "dev-smoke-copy-src"
		clone = "dev-smoke-copy-clone"
		ws    = "dev-smoke-copy-ws"
	)
	t.Cleanup(func() {
		for _, name := range []string{clone, src} {
			_ = exec.Command(bin, "container", "remove", name, "--force").Run()
			_ = exec.Command("docker", "volume", "rm", "--force",
				"dev-"+ws+"-"+name, "dev-"+ws+"-"+name+"-state").Run()
		}
	})

	imageOf := func(name string) string {
		t.Helper()
		out, err := exec.Command("docker", "ps", "-aq",
			"--filter", "label=dev.workspace="+ws, "--filter", "label=dev.container="+name).Output()
		id := strings.TrimSpace(string(out))
		if err != nil || id == "" {
			t.Fatalf("no container for %s: %v", name, err)
		}
		image, err := exec.Command("docker", "inspect", "--format", "{{.Image}}", id).Output()
		if err != nil {
			t.Fatalf("docker inspect %s: %v", id, err)
		}
		return strings.TrimSpace(string(image))
	}

	dev("provider", "configure", "dev-smoke-copy-local", "--kind", "local")
	dev("workspace", "init", ws, "--provider", "dev-smoke-copy-local")

	t.Log("creating the source; the first run pulls a base image and installs features")
	dev("container", "create", src, "--no-folder", "--generate", "--tools", "yq")
	dev("container", "exec", src, "--", "sh", "-c", "echo marker > /var/dev-state/marker")

	dev("container", "create", clone, "--no-folder", "--from", src, "--copy", "image,state")

	// The devcontainer CLI may add a thin UID-adjustment layer on top of an
	// image when it starts a container, so the image IDs can differ. The clone
	// must still sit on the source's image, with at most a couple of layers
	// added on top.
	srcImage, cloneImage := imageOf(src), imageOf(clone)
	srcLayers, cloneLayers := imageLayers(t, srcImage), imageLayers(t, cloneImage)
	if len(cloneLayers) < len(srcLayers) || !slices.Equal(cloneLayers[:len(srcLayers)], srcLayers) {
		t.Errorf("clone image %s is not built on the source image %s: layers %v, want a prefix of %v",
			cloneImage, srcImage, cloneLayers, srcLayers)
	} else if extra := len(cloneLayers) - len(srcLayers); extra > 2 {
		t.Errorf("clone image adds %d layers to the source's image, want at most 2: something was built", extra)
	}
	if out := dev("container", "exec", clone, "--", "cat", "/var/dev-state/marker"); !strings.Contains(out, "marker") {
		t.Errorf("the state was not copied: %q", out)
	}
	fromTag, keptTag := local.SnapshotTag(ws, clone), local.SnapshotKeptTag(ws, clone)
	if out := dockerImages(t, fromTag); len(out) != 0 {
		t.Errorf("the snapshot tag %s outlived the first start: %v", fromTag, out)
	}
	if out := dockerImages(t, keptTag); len(out) != 1 {
		t.Errorf("the kept tag %s = %v, want one image", keptTag, out)
	}
	if out := strings.TrimSpace(dev("container", "exec", clone, "--", "whoami")); out != "vscode" {
		t.Errorf("whoami in the clone = %q, want vscode", out)
	}
	srcVolume := local.StateVolumeName(ws, src)
	for _, entry := range metadataEntries(t, keptTag) {
		if m, ok := entry["mounts"]; ok && strings.Contains(string(m), srcVolume) {
			t.Errorf("the copied image still carries the source's state mount %s: %s", srcVolume, m)
		}
	}

	// Rebuilding the source recreates its container, and must leave the copy's
	// image alone.
	dev("container", "rebuild", src)
	if got := imageOf(clone); got != cloneImage {
		t.Errorf("clone image after the source's rebuild = %s, want %s", got, cloneImage)
	}

	dev("container", "remove", clone)
	for _, tag := range []string{fromTag, keptTag} {
		if out := dockerImages(t, tag); len(out) != 0 {
			t.Errorf("%s survived remove: %v", tag, out)
		}
	}
	dev("container", "remove", src)
}

// metadataEntries returns the devcontainer.metadata label of an image as a
// list of entries.
func metadataEntries(t *testing.T, image string) []map[string]json.RawMessage {
	t.Helper()
	out, err := exec.Command("docker", "image", "inspect", "--format",
		`{{index .Config.Labels "devcontainer.metadata"}}`, image).Output()
	if err != nil {
		t.Fatalf("docker image inspect %s: %v", image, err)
	}
	raw := strings.TrimSpace(string(out))
	if strings.HasPrefix(raw, "{") {
		raw = "[" + raw + "]"
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		t.Fatalf("decoding the metadata of %s: %v: %s", image, err, raw)
	}
	return entries
}

// imageLayers returns the RootFS layer digests of an image, base first.
func imageLayers(t *testing.T, image string) []string {
	t.Helper()
	out, err := exec.Command("docker", "image", "inspect", "--format", "{{json .RootFS.Layers}}", image).Output()
	if err != nil {
		t.Fatalf("docker image inspect %s: %v", image, err)
	}
	var layers []string
	if err := json.Unmarshal(out, &layers); err != nil {
		t.Fatalf("decoding the layers of %s: %v", image, err)
	}
	return layers
}

// dockerImages returns the image IDs a reference names.
func dockerImages(t *testing.T, ref string) []string {
	t.Helper()
	out, err := exec.Command("docker", "images", "-q", ref).Output()
	if err != nil {
		t.Fatalf("docker images: %v", err)
	}
	return strings.Fields(string(out))
}
