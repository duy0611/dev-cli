package k8s

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/model"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/provider"
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

func TestSyncStreamsIntoTheWorkspaceFolder(t *testing.T) {
	s := clusterStubs(t, true)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	c := model.Container{Name: "api", WorkspaceName: "ws", SourceKind: model.SourceFolder, Source: dir}

	if err := testProvider().Sync(context.Background(), c); err != nil {
		t.Fatalf("Sync: %v", err)
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
		t.Errorf("sync unpacks somewhere unexpected: %q", call)
	}
	// -i, or kubectl never attaches the stream the archive is written to.
	if !strings.Contains(call, "exec -i") {
		t.Errorf("sync did not attach stdin: %q", call)
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
