package local

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

var snapTag = SnapshotTag("ws", "demo")

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

func TestFirstStartFromASnapshotKeepsTheTag(t *testing.T) {
	f := newFakePath(t)
	copyDocker(f, t, false)
	f.installScripted(t, devcontainerBin, "")
	copyDocker(f, t, false)

	c := testContainer()
	c.GeneratedConfig = "{}"
	c.ConfigPath = writeDoc(t, buildDoc)
	if err := (&Provider{}).Up(context.Background(), c, nil); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if f.calledWith(t, dockerBin, []string{"rmi"}) {
		t.Errorf("the snapshot is the container's own image and must stay: %v", f.argvAll(t, dockerBin))
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
	if !f.calledWith(t, dockerBin, []string{"rmi", snapTag}) {
		t.Errorf("the snapshot tag outlived a successful rebuild: %v", f.argvAll(t, dockerBin))
	}
}

func TestFailedRebuildKeepsTheSnapshotTag(t *testing.T) {
	f := newFakePath(t)
	copyDocker(f, t, false)
	f.install(t, devcontainerBin, "", 1)

	c := testContainer()
	c.ConfigPath = writeDoc(t, buildDoc)
	if err := (&Provider{}).Rebuild(context.Background(), c, nil, provider.RebuildOpts{}); err == nil {
		t.Fatal("Rebuild succeeded")
	}
	if f.calledWith(t, dockerBin, []string{"rmi"}) {
		t.Error("the tag was deleted after a failed rebuild")
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

func TestRemoveDeletesTheSnapshotAfterTheContainer(t *testing.T) {
	f := newFakePath(t)
	copyDocker(f, t, true)

	if err := (&Provider{}).Remove(context.Background(), testContainer()); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	rm, rmi := -1, -1
	for i, a := range f.argvAll(t, dockerBin) {
		if contains(a, []string{"rm", "-f", "abc123"}) {
			rm = i
		}
		if contains(a, []string{"rmi", snapTag}) {
			rmi = i
		}
	}
	if rm < 0 || rmi < 0 || rmi < rm {
		t.Errorf("want rm -f then rmi, got %v", f.argvAll(t, dockerBin))
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

// snapshotDocker is a docker that reports labels for the source image and
// numbers a temporary container "tmpc".
func snapshotDocker(f *fakeBin, t *testing.T, labels, failVerb string) {
	f.installScripted(t, dockerBin, `
case "$1" in
  image) printf '%s\n' `+shellQuote(labels)+` ;;
esac
[ "$1" = create ] && printf 'tmpc\n'
[ "$1" = "`+failVerb+`" ] && exit 1`)
}

func commitCall(t *testing.T, f *fakeBin) []string {
	t.Helper()
	for _, a := range f.argvAll(t, dockerBin) {
		if a[0] == "commit" {
			return a
		}
	}
	t.Fatalf("no commit: %v", f.argvAll(t, dockerBin))
	return nil
}

func TestSnapshotImageKeepsOnlyFeatureMetadata(t *testing.T) {
	f := newFakePath(t)
	meta := `[{"id":"ghcr.io/x/y:1","containerEnv":{"A":"$1"}},{"mounts":[{"source":"/host/.git","target":"/w/.git"}],"postCreateCommand":"chown x","privileged":true},{"id":"base","remoteUser":"v\"s"}]`
	labels, _ := json.Marshal(map[string]string{"devcontainer.metadata": meta, "other": "1"})
	snapshotDocker(f, t, string(labels), "")

	if err := (&Provider{}).SnapshotImage(context.Background(), testContainer(), "sha256:abc"); err != nil {
		t.Fatal(err)
	}
	calls := f.argvAll(t, dockerBin)
	if !contains(calls[0], []string{"image", "inspect", "--format", "{{json .Config.Labels}}", "sha256:abc"}) {
		t.Errorf("labels were not read first: %v", calls[0])
	}
	if !f.calledWith(t, dockerBin, []string{"create", "--pull=never", "sha256:abc"}) {
		t.Errorf("calls: %v", calls)
	}
	commit := commitCall(t, f)
	want := []string{"commit", "--change",
		`LABEL devcontainer.metadata="[{\"id\":\"ghcr.io/x/y:1\",\"containerEnv\":{\"A\":\"\$1\"}},{\"id\":\"base\",\"remoteUser\":\"v\\\"s\"}]"`,
		"tmpc", snapTag}
	if !slices.Equal(commit, want) {
		t.Errorf("commit = %q\nwant     %q", commit, want)
	}
	if !f.calledWith(t, dockerBin, []string{"rm", "tmpc"}) {
		t.Errorf("the temporary container was not removed: %v", calls)
	}
}

func TestSnapshotImageAcceptsASingleObjectLabel(t *testing.T) {
	f := newFakePath(t)
	labels, _ := json.Marshal(map[string]string{"devcontainer.metadata": `{"id":"a"}`})
	snapshotDocker(f, t, string(labels), "")
	if err := (&Provider{}).SnapshotImage(context.Background(), testContainer(), "sha256:abc"); err != nil {
		t.Fatal(err)
	}
	if got := commitCall(t, f)[2]; got != `LABEL devcontainer.metadata="[{\"id\":\"a\"}]"` {
		t.Errorf("change = %s", got)
	}
}

func TestSnapshotImageWithoutTheLabelJustTags(t *testing.T) {
	for _, labels := range []string{`null`, `{"a":"b"}`} {
		f := newFakePath(t)
		snapshotDocker(f, t, labels, "")
		if err := (&Provider{}).SnapshotImage(context.Background(), testContainer(), "sha256:abc"); err != nil {
			t.Fatal(err)
		}
		if !f.calledWith(t, dockerBin, []string{"tag", "sha256:abc", snapTag}) {
			t.Errorf("labels %s: calls %v", labels, f.argvAll(t, dockerBin))
		}
		for _, a := range f.argvAll(t, dockerBin) {
			if a[0] == "create" || a[0] == "commit" {
				t.Errorf("labels %s: needless %v", labels, a)
			}
		}
	}
}

func TestSnapshotImageRemovesTheTemporaryContainerOnFailure(t *testing.T) {
	f := newFakePath(t)
	labels, _ := json.Marshal(map[string]string{"devcontainer.metadata": `[{"x":1}]`})
	snapshotDocker(f, t, string(labels), "commit")
	if err := (&Provider{}).SnapshotImage(context.Background(), testContainer(), "sha256:abc"); err == nil {
		t.Fatal("SnapshotImage succeeded")
	}
	if !f.calledWith(t, dockerBin, []string{"rm", "tmpc"}) {
		t.Errorf("the temporary container leaked: %v", f.argvAll(t, dockerBin))
	}
}

func copyPair() (src, dst model.Container) {
	src = testContainer()
	src.Name = "web"
	dst = testContainer()
	return src, dst
}

// volumesOnly is a docker that has exactly the named volumes.
func volumesOnly(f *fakeBin, t *testing.T, names ...string) {
	f.installScripted(t, dockerBin, `
if [ "$1" = volume ] && [ "$2" = inspect ]; then
  case "$3" in `+strings.Join(names, "|")+`) exit 0 ;; esac
  exit 1
fi
[ "$1" = image ] && exit 1`)
}

func TestCopyStateRunsAReadOnlyRootHelperWithoutPulling(t *testing.T) {
	f := newFakePath(t)
	volumesOnly(f, t, "dev-ws-web-state")
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
		{"--network", "none"},
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

func TestCopyStateRefusesAnExistingTargetVolumeAndDeletesNothing(t *testing.T) {
	f := newFakePath(t)
	volumesOnly(f, t, "dev-ws-web-state", "dev-ws-demo-state")
	src, dst := copyPair()
	err := (&Provider{}).CopyState(context.Background(), src, dst, "img")
	if !errors.Is(err, provider.ErrTargetExists) {
		t.Fatalf("err = %v, want ErrTargetExists", err)
	}
	for _, a := range f.argvAll(t, dockerBin) {
		if a[0] != "volume" || a[1] != "inspect" {
			t.Errorf("unexpected call %v", a)
		}
	}
}

func TestCheckTarget(t *testing.T) {
	_, dst := copyPair()
	for name, c := range map[string]struct {
		script string
		exists bool
	}{
		"volume": {`[ "$1" = image ] && exit 1`, true},
		"tag":    {`[ "$1" = volume ] && exit 1; printf 'sha256:x\n'`, true},
		"none":   {`exit 1`, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakePath(t)
			f.installScripted(t, dockerBin, c.script)
			err := (&Provider{}).CheckTarget(context.Background(), dst)
			if c.exists != errors.Is(err, provider.ErrTargetExists) || (!c.exists && err != nil) {
				t.Fatalf("err = %v, exists = %v", err, c.exists)
			}
			for _, a := range f.argvAll(t, dockerBin) {
				if a[0] != "volume" && a[0] != "image" || a[1] != "inspect" {
					t.Errorf("unexpected call %v", a)
				}
			}
		})
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
