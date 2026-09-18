package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
)

// Every provider call needs a --workspace-folder that exists: the devcontainer
// CLI takes one on up, exec and read-configuration alike. A folderless
// container has no host directory, so it borrows the temporary one its
// configuration is already written into.
func TestMaterialiseGivesAFolderlessContainerAFolder(t *testing.T) {
	c := model.Container{
		Name: "scratch", WorkspaceName: "ws",
		SourceKind:      model.SourceNone,
		GeneratedConfig: `{"name":"scratch"}`,
	}

	got, cleanup, err := materialise(c)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanup()

	if got.Source == "" {
		t.Fatal("Source is still empty; the devcontainer CLI would be given no workspace folder")
	}
	if info, err := os.Stat(got.Source); err != nil || !info.IsDir() {
		t.Fatalf("Source %q is not a directory: %v", got.Source, err)
	}
	// The config has to be inside the folder that is handed to the CLI, or the
	// CLI reads the layout around a file that is somewhere else entirely.
	if !strings.HasPrefix(got.ConfigPath, got.Source+string(filepath.Separator)) {
		t.Errorf("ConfigPath %q is not inside Source %q", got.ConfigPath, got.Source)
	}

	cleanup()
	if _, err := os.Stat(got.Source); !os.IsNotExist(err) {
		t.Errorf("cleanup left %s behind", got.Source)
	}
}

// A folder container's source is the project, and nothing may move it.
func TestMaterialiseLeavesAFolderSourceAlone(t *testing.T) {
	dir := t.TempDir()
	c := model.Container{
		Name: "demo", WorkspaceName: "ws",
		SourceKind:      model.SourceFolder,
		Source:          dir,
		GeneratedConfig: `{"name":"demo"}`,
	}

	got, cleanup, err := materialise(c)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanup()

	if got.Source != dir {
		t.Errorf("Source = %q, want the project folder %q", got.Source, dir)
	}
}
