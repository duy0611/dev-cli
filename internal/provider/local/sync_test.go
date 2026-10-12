package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

// TestProviderImplementsSyncer checks the type assertion the CLI makes.
func TestProviderImplementsSyncer(t *testing.T) {
	var p any = &Provider{}
	if _, ok := p.(provider.Syncer); !ok {
		t.Fatal("local provider does not implement provider.Syncer")
	}
}

// TestSyncTouchesNothing checks Sync runs neither docker nor devcontainer.
func TestSyncTouchesNothing(t *testing.T) {
	f := newFakePath(t)
	// Installed, so a Sync that shells out succeeds and is caught below.
	f.install(t, dockerBin, "", 0)
	f.install(t, devcontainerBin, "", 0)

	c := model.Container{
		Name: "api", WorkspaceName: "ws",
		SourceKind: model.SourceFolder, Source: t.TempDir(),
	}
	env := []provider.EnvVar{{Key: "GH_TOKEN", Value: "t0ken"}}

	if err := (&Provider{}).Sync(context.Background(), c, env); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	for _, bin := range []string{dockerBin, devcontainerBin} {
		if _, err := os.Stat(filepath.Join(f.dir, bin+".argv")); !os.IsNotExist(err) {
			t.Errorf("Sync ran %s; it must not reach the container at all", bin)
		}
	}
}

// TestSyncAcceptsAFolderlessContainer checks Sync accepts a folderless
// container.
func TestSyncAcceptsAFolderlessContainer(t *testing.T) {
	newFakePath(t)

	c := model.Container{Name: "scratch", WorkspaceName: "ws", SourceKind: model.SourceNone}
	if err := (&Provider{}).Sync(context.Background(), c, nil); err != nil {
		t.Fatalf("Sync refused a folderless container: %v", err)
	}
}
