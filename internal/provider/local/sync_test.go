package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

// TestProviderImplementsSyncer pins the type assertion the CLI makes. A
// provider that silently stopped satisfying the interface would not fail to
// compile anywhere — `dev container sync` would just start reporting that this
// provider cannot do it.
func TestProviderImplementsSyncer(t *testing.T) {
	var p any = &Provider{}
	if _, ok := p.(provider.Syncer); !ok {
		t.Fatal("local provider does not implement provider.Syncer")
	}
}

// Sync has nothing to reconcile here: every command resolves the settings and
// passes them with --remote-env, so no copy exists in the container that could
// have drifted. Running anything would mean writing one, and a file of tokens
// inside a container is what the relay's design exists to avoid.
func TestSyncTouchesNothing(t *testing.T) {
	f := newFakePath(t)
	// Installed so that a Sync which did shell out would succeed and be caught
	// by the assertion below, rather than failing with "not found" and looking
	// like some unrelated breakage.
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

// A folderless container has settings like any other, and there are no files
// here to be missing.
func TestSyncAcceptsAFolderlessContainer(t *testing.T) {
	newFakePath(t)

	c := model.Container{Name: "scratch", WorkspaceName: "ws", SourceKind: model.SourceNone}
	if err := (&Provider{}).Sync(context.Background(), c, nil); err != nil {
		t.Fatalf("Sync refused a folderless container: %v", err)
	}
}
