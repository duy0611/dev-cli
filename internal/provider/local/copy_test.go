package local

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

const snapTag = "dev-ws-demo:from-image"

// copyDocker is a docker whose engine has the snapshot tag, and a container
// only when haveContainer is set.
func copyDocker(f *fakeBin, t *testing.T, haveContainer bool) {
	ps := ""
	if haveContainer {
		ps = `printf 'abc123\n'`
	}
	f.installScripted(t, dockerBin, `
case "$1" in
  image) [ "$5" = `+snapTag+` ] && printf 'sha256:snap\n' || exit 1 ;;
  ps) `+ps+` ;;
  version) exit 1 ;;
esac`)
}

func writeDoc(t *testing.T, doc string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "devcontainer.json")
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// installDevcontainerCapturing makes a devcontainer that copies the documents
// up is given, since they are deleted when it returns. Only up: exec is rightly
// given the original override.
func installDevcontainerCapturing(f *fakeBin, t *testing.T) string {
	capture := filepath.Join(f.dir, "captured")
	f.installScripted(t, devcontainerBin, `
[ "$1" = up ] || exit 0
prev=
for a in "$@"; do
  case "$prev" in
    --config) cp "$a" `+shellQuote(capture+".config")+` ;;
    --override-config) cp "$a" `+shellQuote(capture+".override")+` ;;
  esac
  prev=$a
done`)
	return capture
}

const buildDoc = `{
  // a comment
  "name": "x",
  "build": {"dockerfile": "Dockerfile"},
  "features": {"ghcr.io/x/y:1": {}},
  "dockerFile": "Dockerfile",
  "context": "..",
  "containerEnv": {"A": "1"}
}`

func readCaptured(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no document captured at %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("captured document: %v: %s", err, b)
	}
	return m
}

func assertSnapshotDoc(t *testing.T, m map[string]any) {
	t.Helper()
	if m["image"] != snapTag {
		t.Errorf("image = %v, want %s", m["image"], snapTag)
	}
	for _, k := range []string{"features", "build", "dockerFile", "context"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s survived the rewrite", k)
		}
	}
	if m["containerEnv"] == nil || m["name"] != "x" {
		t.Errorf("other fields were lost: %v", m)
	}
}

func TestFirstStartFromASnapshotRewritesTheGeneratedConfig(t *testing.T) {
	f := newFakePath(t)
	copyDocker(f, t, false)
	capture := installDevcontainerCapturing(f, t)

	c := testContainer()
	c.GeneratedConfig = "{}"
	c.ConfigPath = writeDoc(t, buildDoc)
	if err := (&Provider{}).Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}

	argv := f.argv(t, devcontainerBin)
	if contains(argv, []string{"--config", c.ConfigPath}) {
		t.Errorf("argv %v still passes the original config", argv)
	}
	if slices.Contains(argv, "--override-config") {
		t.Errorf("a generated config is replaced, not overridden: %v", argv)
	}
	assertSnapshotDoc(t, readCaptured(t, capture+".config"))
	if b, _ := os.ReadFile(c.ConfigPath); string(b) != buildDoc {
		t.Error("the original file was modified")
	}
}

func TestFirstStartFromASnapshotRewritesTheOverride(t *testing.T) {
	f := newFakePath(t)
	copyDocker(f, t, false)
	capture := installDevcontainerCapturing(f, t)

	c := overrideContainer()
	c.WorkspaceName, c.Name = "ws", "demo"
	c.OverrideConfigPath = writeDoc(t, buildDoc)
	c.ConfigPath = writeDoc(t, "{}")
	if err := (&Provider{}).Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}

	argv := f.argv(t, devcontainerBin)
	if !contains(argv, []string{"--config", c.ConfigPath}) {
		t.Errorf("argv %v dropped --config", argv)
	}
	assertSnapshotDoc(t, readCaptured(t, capture+".override"))
}

func TestFirstStartFromASnapshotOverridesAProjectConfigWithNoOverride(t *testing.T) {
	f := newFakePath(t)
	copyDocker(f, t, false)
	capture := installDevcontainerCapturing(f, t)

	c := testContainer()
	c.ConfigPath = writeDoc(t, buildDoc) // JSONC
	if err := (&Provider{}).Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !contains(f.argv(t, devcontainerBin), []string{"--config", c.ConfigPath}) {
		t.Error("the project's own --config must stay")
	}
	assertSnapshotDoc(t, readCaptured(t, capture+".override"))
}

func TestFirstStartFromASnapshotDeletesTheTag(t *testing.T) {
	f := newFakePath(t)
	copyDocker(f, t, false)
	f.installScripted(t, devcontainerBin, "")
	// docker was replaced above; reinstall with the tag present.
	copyDocker(f, t, false)

	c := testContainer()
	c.GeneratedConfig = "{}"
	c.ConfigPath = writeDoc(t, buildDoc)
	if err := (&Provider{}).Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !f.calledWith(t, dockerBin, []string{"rmi", snapTag}) {
		t.Errorf("the snapshot tag was not deleted: %v", f.argvAll(t, dockerBin))
	}
}

func TestUpKeepsTheTagWhenTheCLIFails(t *testing.T) {
	f := newFakePath(t)
	copyDocker(f, t, false)
	f.install(t, devcontainerBin, "", 1)

	c := testContainer()
	c.ConfigPath = writeDoc(t, buildDoc)
	if err := (&Provider{}).Up(context.Background(), c, nil); err == nil {
		t.Fatal("Up succeeded")
	}
	if f.calledWith(t, dockerBin, []string{"rmi", snapTag}) {
		t.Error("the tag was deleted after a failed up")
	}
}

func TestNoRewriteWithoutTheTag(t *testing.T) {
	f := newFakePath(t)
	f.installScripted(t, dockerBin, `[ "$1" = image ] && exit 1`)
	capture := installDevcontainerCapturing(f, t)

	c := testContainer()
	c.ConfigPath = writeDoc(t, buildDoc)
	if err := (&Provider{}).Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !contains(f.argv(t, devcontainerBin), []string{"--config", c.ConfigPath}) {
		t.Error("the original config was not passed")
	}
	if _, err := os.Stat(capture + ".override"); err == nil {
		t.Error("an override was written")
	}
}

func TestNoRewriteWhenTheContainerExists(t *testing.T) {
	f := newFakePath(t)
	copyDocker(f, t, true)
	installDevcontainerCapturing(f, t)

	c := testContainer()
	c.ConfigPath = writeDoc(t, buildDoc)
	if err := (&Provider{}).Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !contains(f.argv(t, devcontainerBin), []string{"--config", c.ConfigPath}) {
		t.Error("the original config was not passed")
	}
}

func TestNoRewriteOnRebuild(t *testing.T) {
	f := newFakePath(t)
	copyDocker(f, t, false)
	installDevcontainerCapturing(f, t)

	c := testContainer()
	c.ConfigPath = writeDoc(t, buildDoc)
	if err := (&Provider{}).Rebuild(context.Background(), c, nil, provider.RebuildOpts{}); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if !contains(f.argv(t, devcontainerBin), []string{"--config", c.ConfigPath}) {
		t.Error("the original config was not passed")
	}
}

func TestSnapshotRefusesCompose(t *testing.T) {
	f := newFakePath(t)
	copyDocker(f, t, false)
	f.install(t, devcontainerBin, "", 0)

	c := testContainer()
	c.ConfigPath = writeDoc(t, `{"dockerComposeFile": "c.yml", "service": "a"}`)
	if err := (&Provider{}).Up(context.Background(), c, nil); err == nil {
		t.Fatal("Up accepted a compose config for a snapshot")
	}
}

func TestRemoveDeletesTheSnapshotTag(t *testing.T) {
	f := newFakePath(t)
	copyDocker(f, t, false)

	if err := (&Provider{}).Remove(context.Background(), testContainer()); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !f.calledWith(t, dockerBin, []string{"rmi", snapTag}) {
		t.Error("the snapshot tag was not deleted")
	}
}

func TestRemoveWithoutATagIsQuiet(t *testing.T) {
	f := newFakePath(t)
	f.installScripted(t, dockerBin, `[ "$1" = image ] && exit 1`)

	if err := (&Provider{}).Remove(context.Background(), testContainer()); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if f.calledWith(t, dockerBin, []string{"rmi", snapTag}) {
		t.Error("rmi ran with no tag")
	}
}

func TestSourceImage(t *testing.T) {
	f := newFakePath(t)
	f.installScripted(t, dockerBin, `
case "$1" in
  ps) printf 'cid\n' ;;
  inspect) printf 'sha256:abc\n' ;;
esac`)
	img, err := (&Provider{}).SourceImage(context.Background(), testContainer())
	if err != nil || img != "sha256:abc" {
		t.Fatalf("SourceImage = %q, %v", img, err)
	}
	if !f.calledWith(t, dockerBin, []string{"inspect", "--type", "container", "--format", "{{.Image}}", "cid"}) {
		t.Errorf("calls: %v", f.argvAll(t, dockerBin))
	}
	if !f.calledWith(t, dockerBin, []string{"filter", "label=dev.workspace=ws"}) &&
		!f.calledWith(t, dockerBin, []string{"--filter", "label=dev.workspace=ws"}) {
		t.Errorf("container not found by label: %v", f.argvAll(t, dockerBin))
	}
}

func TestSourceImageWithoutAContainer(t *testing.T) {
	f := newFakePath(t)
	f.installScripted(t, dockerBin, ``)
	_, err := (&Provider{}).SourceImage(context.Background(), testContainer())
	if !errors.Is(err, provider.ErrNoContainer) {
		t.Fatalf("err = %v, want ErrNoContainer", err)
	}
}

func TestSourceImageGone(t *testing.T) {
	f := newFakePath(t)
	f.installScripted(t, dockerBin, `
case "$1" in
  ps) printf 'cid\n' ;;
  inspect) printf 'sha256:abc\n' ;;
  image) exit 1 ;;
esac`)
	_, err := (&Provider{}).SourceImage(context.Background(), testContainer())
	if !errors.Is(err, provider.ErrNoImage) {
		t.Fatalf("err = %v, want ErrNoImage", err)
	}
}

func TestSnapshotImageTags(t *testing.T) {
	f := newFakePath(t)
	f.installScripted(t, dockerBin, ``)
	if err := (&Provider{}).SnapshotImage(context.Background(), testContainer(), "sha256:abc"); err != nil {
		t.Fatal(err)
	}
	if !f.calledWith(t, dockerBin, []string{"tag", "sha256:abc", snapTag}) {
		t.Errorf("calls: %v", f.argvAll(t, dockerBin))
	}
}

func copyPair() (src, dst model.Container) {
	src = testContainer()
	src.Name = "web"
	dst = testContainer()
	return src, dst
}

func TestCopyStateRunsAReadOnlyRootHelperWithoutPulling(t *testing.T) {
	f := newFakePath(t)
	f.installScripted(t, dockerBin, ``)
	src, dst := copyPair()
	if err := (&Provider{}).CopyState(context.Background(), src, dst, "sha256:abc"); err != nil {
		t.Fatal(err)
	}
	if !f.calledWith(t, dockerBin, []string{"volume", "create", "dev-ws-demo-state"}) {
		t.Errorf("the new volume was not created: %v", f.argvAll(t, dockerBin))
	}
	var run []string
	for _, a := range f.argvAll(t, dockerBin) {
		if a[0] == "run" {
			run = a
		}
	}
	for _, want := range [][]string{
		{"run", "--rm", "--pull=never"},
		{"--user", "0"},
		{"--entrypoint", "cp"},
		{"-v", "dev-ws-web-state:/from:ro"},
		{"-v", "dev-ws-demo-state:/to"},
		{"sha256:abc", "-a", "/from/.", "/to/"},
	} {
		if !contains(run, want) {
			t.Errorf("run %v is missing %v", run, want)
		}
	}
}

func TestCopyStateRefusesAMissingSourceVolume(t *testing.T) {
	f := newFakePath(t)
	f.installScripted(t, dockerBin, `[ "$1" = volume ] && [ "$2" = inspect ] && exit 1`)
	src, dst := copyPair()
	err := (&Provider{}).CopyState(context.Background(), src, dst, "img")
	if !errors.Is(err, provider.ErrNoStateVolume) {
		t.Fatalf("err = %v, want ErrNoStateVolume", err)
	}
	for _, a := range f.argvAll(t, dockerBin) {
		if a[0] == "run" || (a[0] == "volume" && a[1] == "create") {
			t.Errorf("created something after the refusal: %v", a)
		}
	}
}

func TestDiscardCopyRemovesTagAndVolume(t *testing.T) {
	f := newFakePath(t)
	copyDocker(f, t, false)
	if err := (&Provider{}).DiscardCopy(context.Background(), testContainer()); err != nil {
		t.Fatal(err)
	}
	if !f.calledWith(t, dockerBin, []string{"rmi", snapTag}) ||
		!f.calledWith(t, dockerBin, []string{"volume", "rm", "--force", "dev-ws-demo-state"}) {
		t.Errorf("calls: %v", f.argvAll(t, dockerBin))
	}
}

func TestDiscardCopyIsIdempotent(t *testing.T) {
	f := newFakePath(t)
	f.installScripted(t, dockerBin, `[ "$1" = image ] && exit 1`)
	if err := (&Provider{}).DiscardCopy(context.Background(), testContainer()); err != nil {
		t.Fatal(err)
	}
	if f.calledWith(t, dockerBin, []string{"rmi", snapTag}) {
		t.Error("rmi ran with no tag")
	}
}

func TestCheckStateReportsAMissingVolumeAndCreatesNothing(t *testing.T) {
	f := newFakePath(t)
	f.installScripted(t, dockerBin, `[ "$1" = volume ] && [ "$2" = inspect ] && exit 1`)
	src, _ := copyPair()
	err := (&Provider{}).CheckState(context.Background(), src)
	if !errors.Is(err, provider.ErrNoStateVolume) {
		t.Fatalf("err = %v, want ErrNoStateVolume", err)
	}
	if !f.calledWith(t, dockerBin, []string{"volume", "inspect", "dev-ws-web-state"}) {
		t.Errorf("calls: %v", f.argvAll(t, dockerBin))
	}
	for _, a := range f.argvAll(t, dockerBin) {
		if a[0] != "volume" || a[1] != "inspect" {
			t.Errorf("unexpected call %v", a)
		}
	}
}

func TestCheckStatePassesWhenTheVolumeExists(t *testing.T) {
	f := newFakePath(t)
	f.installScripted(t, dockerBin, ``)
	src, _ := copyPair()
	if err := (&Provider{}).CheckState(context.Background(), src); err != nil {
		t.Fatal(err)
	}
}
