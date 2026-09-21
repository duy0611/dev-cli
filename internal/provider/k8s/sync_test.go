package k8s

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

// readTar returns the archive's entries by name.
func readTar(t *testing.T, r io.Reader) map[string]*tar.Header {
	t.Helper()
	out := map[string]*tar.Header{}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("reading the archive: %v", err)
		}
		out[h.Name] = h
	}
}

func TestTarDirCarriesTheTree(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	write("main.go", "package main", 0o644)
	write("scripts/run.sh", "#!/bin/sh\n", 0o755)
	// An agent in the container needs the history to branch and commit.
	write(".git/HEAD", "ref: refs/heads/main\n", 0o644)
	if err := os.Symlink("main.go", filepath.Join(dir, "link.go")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	var buf bytes.Buffer
	if err := tarDir(dir, &buf); err != nil {
		t.Fatalf("tarDir: %v", err)
	}
	entries := readTar(t, &buf)

	for _, want := range []string{"main.go", "scripts/", "scripts/run.sh", ".git/HEAD", "link.go"} {
		if _, ok := entries[want]; !ok {
			t.Errorf("%q is missing from the archive: %v", want, keys(entries))
		}
	}
	// Without the mode, every script arrives unexecutable.
	if got := entries["scripts/run.sh"].Mode & 0o111; got == 0 {
		t.Errorf("scripts/run.sh lost its executable bit (mode %o)", entries["scripts/run.sh"].Mode)
	}
	if entries["link.go"].Typeflag != tar.TypeSymlink {
		t.Errorf("link.go is type %v, want a symlink", entries["link.go"].Typeflag)
	}
	if entries["link.go"].Linkname != "main.go" {
		t.Errorf("link.go points at %q", entries["link.go"].Linkname)
	}
	// Paths must be relative, or tar -C unpacks them to absolute locations.
	for name := range entries {
		if strings.HasPrefix(name, "/") {
			t.Errorf("archive holds an absolute path: %q", name)
		}
	}
}

// A stray socket — an editor's, a database's — should not stop a sync.
func TestTarDirSkipsUnarchivableFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := mkfifo(filepath.Join(dir, "pipe")); err != nil {
		t.Skipf("cannot create a fifo here: %v", err)
	}

	var buf bytes.Buffer
	if err := tarDir(dir, &buf); err != nil {
		t.Fatalf("tarDir: %v", err)
	}
	entries := readTar(t, &buf)
	if _, ok := entries["keep.txt"]; !ok {
		t.Error("the regular file was dropped")
	}
	if _, ok := entries["pipe"]; ok {
		t.Error("the fifo was archived")
	}
}

func TestSeedWorkspaceStreamsIntoTheWorkspaceFolder(t *testing.T) {
	s := clusterStubs(t, true)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	c := model.Container{Name: "api", WorkspaceName: "ws", SourceKind: model.SourceFolder, Source: dir}

	if err := testProvider().seedWorkspace(context.Background(), c); err != nil {
		t.Fatalf("seedWorkspace: %v", err)
	}

	var call string
	for _, k := range kubectlCalls(t, s) {
		if strings.Contains(k, "tar") {
			call = k
		}
	}
	if call == "" {
		t.Fatalf("no tar call: %v", kubectlCalls(t, s))
	}
	// The stub's read-configuration answers with this workspaceFolder.
	if !strings.Contains(call, "tar -x -p -C /workspaces/api") {
		t.Errorf("the seed unpacks somewhere unexpected: %q", call)
	}
	// -i, or kubectl never attaches the stream the archive is written to.
	if !strings.Contains(call, "exec -i") {
		t.Errorf("the seed did not attach stdin: %q", call)
	}
}

// Sync applies the Secret and nothing else. The Deployment's absence is the
// point: a server-side apply drops the fields it does not set, so re-applying
// the Deployment without a rebuiltAt would clear that annotation, change the
// pod template, and let the Recreate strategy destroy the work this command
// exists to leave alone.
func TestSyncAppliesTheSecretAndNotTheDeployment(t *testing.T) {
	s := clusterStubs(t, true)
	c := k8sContainer(t)

	env := []provider.EnvVar{{Key: "GH_TOKEN", Value: "t0ken"}}
	if err := testProvider().Sync(context.Background(), c, env); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	calls := kubectlCalls(t, s)
	if !anyCall(calls, "apply --server-side") {
		t.Fatalf("Sync applied nothing: %v", calls)
	}

	manifest := s.stdinOf(t, kubectlBin, "apply")
	var obj struct {
		Kind       string            `json:"kind"`
		StringData map[string]string `json:"stringData"`
	}
	if err := json.Unmarshal([]byte(manifest), &obj); err != nil {
		t.Fatalf("the applied manifest is not JSON: %v: %s", err, manifest)
	}
	if obj.Kind != "Secret" {
		t.Errorf("Sync applied a %s; only the Secret may be applied", obj.Kind)
	}
	if got := obj.StringData["GH_TOKEN"]; got != "t0ken" {
		t.Errorf("GH_TOKEN = %q, want %q", got, "t0ken")
	}
	// Belt and braces: a List carrying a Deployment would unmarshal with an
	// unsurprising kind above and still restart the pod.
	if strings.Contains(manifest, "Deployment") {
		t.Errorf("the applied manifest names a Deployment, which would restart the pod: %s", manifest)
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "scale") || strings.Contains(call, "rollout restart") {
			t.Errorf("Sync disturbed the pod: %q", call)
		}
	}
}

// A setting the workspace no longer has must leave the cluster's copy. The
// apply replaces the object's stringData wholesale, so this falls out of the
// mechanism — but it is the operator-visible half of "rotate and unset", and
// nothing else asserts it.
func TestSyncCarriesAnUnsetKeyAway(t *testing.T) {
	s := clusterStubs(t, true)
	c := k8sContainer(t)

	if err := testProvider().Sync(context.Background(), c, []provider.EnvVar{{Key: "KEPT", Value: "y"}}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	manifest := s.stdinOf(t, kubectlBin, "apply")
	if strings.Contains(manifest, "GONE") {
		t.Errorf("the applied Secret still carries a setting the workspace does not have: %s", manifest)
	}
	if !strings.Contains(manifest, "KEPT") {
		t.Errorf("the applied Secret is missing the setting the workspace does have: %s", manifest)
	}
}

// Settings belong to a container whether or not it has a folder. The old
// refusal was about files, and there are none here to be missing.
func TestSyncAcceptsAFolderlessContainer(t *testing.T) {
	clusterStubs(t, true)

	c := model.Container{Name: "scratch", WorkspaceName: "ws", SourceKind: model.SourceNone}
	if err := testProvider().Sync(context.Background(), c, nil); err != nil {
		t.Fatalf("Sync refused a folderless container: %v", err)
	}
}

// The interface is optional by design, so the assertion the CLI makes has to
// actually hold for this provider.
func TestProviderImplementsSyncer(t *testing.T) {
	var p any = &Provider{}
	if _, ok := p.(provider.Syncer); !ok {
		t.Fatal("the k8s provider does not implement provider.Syncer")
	}
}

func keys(m map[string]*tar.Header) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// There is no host tree to copy. Unpacking nothing over a workspace an agent is
// working in would be worse than complaining, and the caller is Up rather than
// the operator, so this is a bug report and not a usage error.
func TestSeedWorkspaceRefusesAFolderlessContainer(t *testing.T) {
	clusterStubs(t, true)

	c := model.Container{Name: "scratch", WorkspaceName: "ws", SourceKind: model.SourceNone}
	err := testProvider().seedWorkspace(context.Background(), c)
	if err == nil {
		t.Fatal("seedWorkspace accepted a container with no folder")
	}
	if !strings.Contains(err.Error(), "no folder") {
		t.Errorf("error %q does not say why", err)
	}
}
