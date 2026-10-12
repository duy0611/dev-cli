package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
)

// Provider calls need a --workspace-folder that exists; a folderless container
// borrows the temporary directory its configuration is written into.
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
	if !strings.HasPrefix(got.ConfigPath, got.Source+string(filepath.Separator)) {
		t.Errorf("ConfigPath %q is not inside Source %q", got.ConfigPath, got.Source)
	}

	cleanup()
	if _, err := os.Stat(got.Source); !os.IsNotExist(err) {
		t.Errorf("cleanup left %s behind", got.Source)
	}
}

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

// projectContainer is a container whose project ships devcontainer.json at
// path.
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

// writeProjectConfig writes a devcontainer.json and returns its path.
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

	// The project's own path still reaches the CLI as --config, keeping a
	// relative dockerfile anchored.
	if c.ConfigPath != path {
		t.Errorf("ConfigPath = %q, want the project's own %q", c.ConfigPath, path)
	}

	cleanup()
	if _, err := os.Stat(c.OverrideConfigPath); !os.IsNotExist(err) {
		t.Errorf("cleanup left the override behind: %v", err)
	}
}

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
	if c.Name != "c1" {
		t.Errorf("materialise lost the container on a parse failure: %+v", c)
	}
}

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

func TestMaterialiseBuildsAWorktreeOverride(t *testing.T) {
	path := writeProjectConfig(t, `{"name":"demo"}`)
	c := projectContainer(path)
	c.PersistState = false
	c.WorktreeRepo = "/home/u/src/app/.git"

	got, cleanup, err := materialise(c, true)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanup()

	if got.OverrideConfigPath == "" {
		t.Fatal("materialise built no override for a worktree container")
	}
	body, err := os.ReadFile(got.OverrideConfigPath)
	if err != nil {
		t.Fatalf("reading the override: %v", err)
	}
	for _, want := range []string{c.WorktreeRepo, c.Source} {
		if !strings.Contains(string(body), want) {
			t.Errorf("override does not bind-mount %s: %s", want, body)
		}
	}
}

func TestMaterialiseWorktreeWithStateGetsBothMerges(t *testing.T) {
	path := writeProjectConfig(t, `{"name":"demo"}`)
	c := projectContainer(path)
	c.WorktreeRepo = "/home/u/src/app/.git"

	got, cleanup, err := materialise(c, true)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanup()

	body, err := os.ReadFile(got.OverrideConfigPath)
	if err != nil {
		t.Fatalf("reading the override: %v", err)
	}
	var doc struct {
		Mounts []string `json:"mounts"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(doc.Mounts) != 3 {
		t.Fatalf("mounts = %v, want 3 entries", doc.Mounts)
	}
	seen := map[string]bool{}
	for _, m := range doc.Mounts {
		_, target, _ := strings.Cut(m, "target=")
		target, _, _ = strings.Cut(target, ",")
		if seen[target] {
			t.Errorf("mounts repeat the target %q; docker refuses that run", target)
		}
		seen[target] = true
	}
}

func TestMaterialiseBuildsNoWorktreeOverrideForAGeneratedContainer(t *testing.T) {
	c := projectContainer("")
	c.GeneratedConfig = `{"name":"demo"}`
	c.WorktreeRepo = "/home/u/src/app/.git"

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
