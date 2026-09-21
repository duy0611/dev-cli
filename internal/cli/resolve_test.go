package cli

import (
	"encoding/json"
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

	got, cleanup, err := materialise(c, true)
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

	got, cleanup, err := materialise(c, true)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanup()

	if got.Source != dir {
		t.Errorf("Source = %q, want the project folder %q", got.Source, dir)
	}
}

// projectContainer is a container whose project ships its own devcontainer.json
// at path, and which persists agent state.
func projectContainer(path string) model.Container {
	return model.Container{
		Name:          "c1",
		WorkspaceName: "ws",
		SourceKind:    model.SourceFolder,
		Source:        "/tmp/project",
		ConfigPath:    path,
		PersistState:  true,
	}
}

// writeProjectConfig puts a devcontainer.json on disk and returns its path.
func writeProjectConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "devcontainer.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the project config: %v", err)
	}
	return path
}

func TestMaterialiseBuildsAnOverrideForAProjectOwnedContainer(t *testing.T) {
	path := writeProjectConfig(t, `{"name":"demo","image":"ubuntu"}`)

	c, cleanup, err := materialise(projectContainer(path), true)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanup()

	if c.OverrideConfigPath == "" {
		t.Fatal("materialise built no override")
	}
	body, err := os.ReadFile(c.OverrideConfigPath)
	if err != nil {
		t.Fatalf("reading the override: %v", err)
	}
	if !strings.Contains(string(body), "dev-ws-c1-state") {
		t.Errorf("override does not name the state volume: %s", body)
	}

	// The project's own path still reaches the CLI as --config, which is what
	// keeps a relative "dockerfile" anchored to the project.
	if c.ConfigPath != path {
		t.Errorf("ConfigPath = %q, want the project's own %q", c.ConfigPath, path)
	}

	// The temp file goes with the invocation; nothing survives release.
	cleanup()
	if _, err := os.Stat(c.OverrideConfigPath); !os.IsNotExist(err) {
		t.Errorf("cleanup left the override behind: %v", err)
	}
}

// A generated document already names the volume in its own mounts. Adding an
// override too would be two mechanisms for one outcome.
func TestMaterialiseBuildsNoOverrideForAGeneratedContainer(t *testing.T) {
	c := projectContainer("")
	c.GeneratedConfig = `{"name":"demo"}`

	got, cleanup, err := materialise(c, true)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanup()

	if got.OverrideConfigPath != "" {
		t.Errorf("OverrideConfigPath = %q, want empty for a generated container",
			got.OverrideConfigPath)
	}
}

func TestMaterialiseBuildsNoOverrideWithoutPersistState(t *testing.T) {
	c := projectContainer(writeProjectConfig(t, `{"name":"demo"}`))
	c.PersistState = false

	got, cleanup, err := materialise(c, true)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanup()

	if got.OverrideConfigPath != "" {
		t.Errorf("OverrideConfigPath = %q, want empty when state is off",
			got.OverrideConfigPath)
	}
}

// k8s ignores a document's mounts, so it is never handed an override.
func TestMaterialiseBuildsNoOverrideWhenTheProviderIgnoresIt(t *testing.T) {
	c := projectContainer(writeProjectConfig(t, `{"name":"demo"}`))

	got, cleanup, err := materialise(c, false)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanup()

	if got.OverrideConfigPath != "" {
		t.Errorf("OverrideConfigPath = %q, want empty for a provider that ignores it",
			got.OverrideConfigPath)
	}
}

// The deferred failure. A project file dev cannot parse must not strand the
// container in the engine: stop, remove, logs and status never read it.
func TestMaterialiseDefersAParseFailure(t *testing.T) {
	path := writeProjectConfig(t, `{"name":}`)

	c, cleanup, err := materialise(projectContainer(path), true)
	defer cleanup()

	if err == nil {
		t.Fatal("materialise accepted a malformed project config")
	}
	if !isOverlayError(err) {
		t.Errorf("error is not deferrable: %v", err)
	}
	// The container is still usable for the commands that do not need the
	// override — Remove finds it by docker label, not by config.
	if c.Name != "c1" {
		t.Errorf("materialise lost the container on a parse failure: %+v", c)
	}
}

// The merge runs per invocation, so an edit to the project's file between two
// commands is picked up by the second. A stored merge would freeze a snapshot.
func TestMaterialiseRereadsTheProjectConfig(t *testing.T) {
	path := writeProjectConfig(t, `{"name":"demo"}`)

	first, cleanupFirst, err := materialise(projectContainer(path), true)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanupFirst()

	if err := os.WriteFile(path, []byte(`{"name":"demo","image":"alpine"}`), 0o600); err != nil {
		t.Fatalf("editing the project config: %v", err)
	}

	second, cleanupSecond, err := materialise(projectContainer(path), true)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanupSecond()

	body, err := os.ReadFile(second.OverrideConfigPath)
	if err != nil {
		t.Fatalf("reading the override: %v", err)
	}
	var parsed struct {
		Image string `json:"image"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Image != "alpine" {
		t.Errorf("second override did not pick up the edit: %s", body)
	}
	_ = first
}
