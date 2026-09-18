package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/dcgen"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider/k8s"
)

// seedWorkspace gives a test app a usable provider and workspace, since every
// container command resolves one before it does anything else.
func seedWorkspace(t *testing.T, a *app) {
	t.Helper()
	if err := runProviderConfigure(a, "p", "local", k8s.Config{}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	// The first workspace becomes the active one, so nothing needs `workspace
	// use` afterwards.
	if err := runWorkspaceInit(a, "ws", "p"); err != nil {
		t.Fatalf("workspace init: %v", err)
	}
}

func TestParseToolList(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"node", []string{"node"}},
		{"node,yq", []string{"node", "yq"}},
		{" node , yq ", []string{"node", "yq"}},
		{"node,,yq", []string{"node", "yq"}},
	}
	for _, tt := range tests {
		if got := parseToolList(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("parseToolList(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// Under `go test` stdin is /dev/null, which isTerminal rejects — so these
// exercise the scripted path, which is the one that must never block.
func TestCreateWithoutAConfigStillFails(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	err := runContainerCreate(t.Context(), a, "", "demo", t.TempDir(), createOpts{})
	if err == nil {
		t.Fatal("create succeeded on a folder with no devcontainer config")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
	// The error has to teach the way forward; there is no other discovery path.
	if !strings.Contains(err.Error(), "--generate") {
		t.Errorf("error %q does not mention --generate", err)
	}
}

func TestCreateGeneratesAConfig(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	folder := t.TempDir()
	opts := createOpts{generate: true, tools: []string{"yq"}, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "demo", folder, opts); err != nil {
		t.Fatalf("create: %v", err)
	}

	st, err := a.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	c, err := st.GetContainer("ws", "demo")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if !strings.Contains(c.GeneratedConfig, "apt-get-packages") {
		t.Errorf("stored config does not install yq:\n%s", c.GeneratedConfig)
	}
	if c.ConfigPath != "" {
		t.Errorf("ConfigPath = %q, want empty for a generated container", c.ConfigPath)
	}

	// Nothing may be written into the folder: the project owns its own
	// directory, and a generated config lives in the database instead.
	entries, err := os.ReadDir(folder)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("create wrote into the project folder: %v", entries)
	}
}

// The project owns its container definition. Shadowing a configuration that is
// right there is the worst of both behaviours.
func TestGenerateRefusesAFolderThatHasAConfig(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	folder := projectWithConfig(t)

	opts := createOpts{generate: true, noStart: true}
	err := runContainerCreate(t.Context(), a, "", "demo", folder, opts)
	if err == nil {
		t.Fatal("--generate shadowed the project's own configuration")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
}

func TestToolsWithoutGenerateIsRejected(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{tools: []string{"yq"}, noStart: true}
	err := runContainerCreate(t.Context(), a, "", "demo", t.TempDir(), opts)
	if err == nil {
		t.Fatal("--tools was accepted without --generate")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
}

func TestGenerateRejectsAnUnknownTool(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{generate: true, tools: []string{"kubctl"}, noStart: true}
	err := runContainerCreate(t.Context(), a, "", "demo", t.TempDir(), opts)
	if err == nil {
		t.Fatal("an unknown tool was accepted")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
	if !strings.Contains(err.Error(), "kubctl") {
		t.Errorf("error %q does not name the offending tool", err)
	}
}

// projectWithConfig builds a folder that ships its own devcontainer config,
// which is the case --generate must refuse and config show must not invent one
// for.
func projectWithConfig(t *testing.T) string {
	t.Helper()
	folder := t.TempDir()
	if err := os.MkdirAll(filepath.Join(folder, ".devcontainer"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(folder, ".devcontainer", "devcontainer.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return folder
}

func TestContainerToolsListsTheCatalog(t *testing.T) {
	a, out := newTestApp(t)
	if err := runContainerTools(a); err != nil {
		t.Fatalf("tools: %v", err)
	}
	got := out.String()
	for _, want := range []string{"node", "claude-code", "official", "community"} {
		if !strings.Contains(got, want) {
			t.Errorf("tools output is missing %q:\n%s", want, got)
		}
	}
}

// With the configuration in the database rather than on disk, this is the only
// way to see what a container was built from.
func TestConfigShowPrintsTheStoredConfig(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{generate: true, tools: []string{"yq"}, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "demo", t.TempDir(), opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	out.Reset()

	if err := runContainerConfigShow(a, "", "demo"); err != nil {
		t.Fatalf("config show: %v", err)
	}
	if !strings.Contains(out.String(), "apt-get-packages") {
		t.Errorf("config show printed nothing useful:\n%s", out.String())
	}
}

// A project-owned container has no stored configuration, and printing "" would
// look like an empty one rather than a different kind of container.
func TestConfigShowOnAProjectOwnedContainer(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	folder := projectWithConfig(t)
	if err := runContainerCreate(t.Context(), a, "", "owned", folder, createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}

	err := runContainerConfigShow(a, "", "owned")
	if err == nil {
		t.Fatal("config show invented a configuration for a project-owned container")
	}
	if !strings.Contains(err.Error(), ".devcontainer") {
		t.Errorf("error %q does not point at the project's own config", err)
	}
}

// The devcontainer CLI only accepts a path, so a configuration that lives in
// the database has to become a file for the length of one invocation — and
// stop being one afterwards.
func TestMaterialiseWritesAndCleansUp(t *testing.T) {
	c := model.Container{Name: "demo", GeneratedConfig: "{\"name\":\"demo\"}\n"}

	got, cleanup, err := materialise(c)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	if got.ConfigPath == "" {
		t.Fatal("materialise produced no config path")
	}
	if filepath.Base(got.ConfigPath) != "devcontainer.json" ||
		filepath.Base(filepath.Dir(got.ConfigPath)) != ".devcontainer" {
		// The CLI reads the layout around the file and rejects any other
		// filename outright, so the temporary copy mirrors a real project.
		t.Errorf("unexpected layout: %s", got.ConfigPath)
	}
	body, err := os.ReadFile(got.ConfigPath)
	if err != nil {
		t.Fatalf("reading the materialised config: %v", err)
	}
	if string(body) != c.GeneratedConfig {
		t.Errorf("materialised %q, want %q", body, c.GeneratedConfig)
	}

	cleanup()
	if _, err := os.Stat(got.ConfigPath); !os.IsNotExist(err) {
		t.Errorf("cleanup left the file behind: %v", err)
	}
}

func TestMaterialiseLeavesAProjectOwnedContainerAlone(t *testing.T) {
	c := model.Container{Name: "demo", ConfigPath: "/proj/.devcontainer/devcontainer.json"}

	got, cleanup, err := materialise(c)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	defer cleanup() // must be safe to call even when nothing was written
	if got.ConfigPath != c.ConfigPath {
		t.Errorf("ConfigPath = %q, want it untouched", got.ConfigPath)
	}
}

func TestApplyToolDiff(t *testing.T) {
	current := []string{"helm", "node"}

	tests := []struct {
		name string
		spec []string
		want []string
	}{
		{"add", []string{"+yq"}, []string{"helm", "node", "yq"}},
		{"remove", []string{"-helm"}, []string{"node"}},
		{"both", []string{"+yq", "-helm"}, []string{"node", "yq"}},
		{"replace", []string{"python", "yq"}, []string{"python", "yq"}},
		{"remove what is absent", []string{"-python"}, []string{"helm", "node"}},
		{"add what is present", []string{"+node"}, []string{"helm", "node"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applyToolDiff(current, tt.spec)
			if err != nil {
				t.Fatalf("applyToolDiff: %v", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("applyToolDiff(%v, %v) = %v, want %v", current, tt.spec, got, tt.want)
			}
		})
	}
}

// "node,+yq" reads as one intent and means another, so it is refused rather
// than guessed at.
func TestApplyToolDiffRejectsMixedForms(t *testing.T) {
	_, err := applyToolDiff([]string{"node"}, []string{"node", "+yq"})
	if err == nil {
		t.Fatal("a mixed diff and replacement was accepted")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
}

func TestRebuildToolsRewritesTheStoredConfig(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{generate: true, tools: []string{"yq"}, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "demo", t.TempDir(), opts); err != nil {
		t.Fatalf("create: %v", err)
	}

	// The rebuild itself needs an engine; this asserts the rewrite, which is
	// the part that belongs to dev.
	if err := rewriteGeneratedTools(a, "", "demo", []string{"+python"}); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	st, err := a.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	c, err := st.GetContainer("ws", "demo")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	tools, err := dcgen.ToolsOf(c.GeneratedConfig)
	if err != nil {
		t.Fatalf("ToolsOf: %v", err)
	}
	if !slices.Equal(tools, []string{"python", "yq"}) {
		t.Errorf("tools = %v, want [python yq]", tools)
	}
}

// A project's configuration is not dev's to rewrite.
func TestRebuildToolsRefusesAProjectOwnedContainer(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	folder := projectWithConfig(t)
	if err := runContainerCreate(t.Context(), a, "", "owned", folder, createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}

	err := rewriteGeneratedTools(a, "", "owned", []string{"+yq"})
	if err == nil {
		t.Fatal("--tools rewrote a project-owned container")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
}

// Omitting both is the likeliest typo, and guessing either way is expensive:
// an empty sandbox is only noticed when the agent cannot find the code.
func TestCreateWithNeitherFolderNorNoFolderIsRejected(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	err := runContainerCreate(t.Context(), a, "", "scratch", "", createOpts{noStart: true})
	if err == nil {
		t.Fatal("create succeeded with neither --folder nor --no-folder")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
	for _, want := range []string{"--folder", "--no-folder"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

func TestCreateWithBothFolderAndNoFolderIsRejected(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{noFolder: true, noStart: true}
	err := runContainerCreate(t.Context(), a, "", "scratch", t.TempDir(), opts)
	if err == nil {
		t.Fatal("create accepted both --folder and --no-folder")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
}

// A folderless container is always a generated one: there is no project to
// ship a configuration. The document must carry the volume, and the row must
// record that there is no source.
func TestCreateNoFolderStoresAVolumeBackedConfig(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{noFolder: true, tools: []string{"yq"}, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "scratch", "", opts); err != nil {
		t.Fatalf("create: %v", err)
	}

	st, err := a.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	c, err := st.GetContainer("ws", "scratch")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if c.SourceKind != model.SourceNone {
		t.Errorf("SourceKind = %q, want %q", c.SourceKind, model.SourceNone)
	}
	if c.Source != "" {
		t.Errorf("Source = %q, want empty", c.Source)
	}
	if !strings.Contains(c.GeneratedConfig, `"source=dev-ws-scratch,target=/workspaces/scratch,type=volume"`) {
		t.Errorf("stored config does not mount the volume:\n%s", c.GeneratedConfig)
	}
	if !strings.Contains(c.GeneratedConfig, `"workspaceFolder": "/workspaces/scratch"`) {
		t.Errorf("stored config does not pin the workspace folder:\n%s", c.GeneratedConfig)
	}
}

// --no-folder needs no --generate: there is no project configuration for a
// generated one to shadow, which is the only thing --generate guards against.
// A scripted run must not block on the picker either. Under `go test` stdin is
// /dev/null, which isTerminal rejects, so this is the scripted path: per the
// spec it must succeed with a bare Ubuntu image and no tools, not fail the
// way folderSource does for a folder with no config.
func TestCreateNoFolderNeedsNoGenerateFlag(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{noFolder: true, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "scratch", "", opts); err != nil {
		t.Fatalf("create: %v", err)
	}

	st, err := a.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	c, err := st.GetContainer("ws", "scratch")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	// A scripted run with no --tools must not silently install anything.
	if strings.Contains(c.GeneratedConfig, "apt-get-packages") {
		t.Errorf("bare no-folder container installed a tool it was not asked for:\n%s", c.GeneratedConfig)
	}
	// The volume mount must still be there: a bare image is still a
	// folderless one, and materialise still points the provider at the
	// generated document rather than an empty one.
	if !strings.Contains(c.GeneratedConfig, "workspaceMount") {
		t.Errorf("bare no-folder container lost its volume mount:\n%s", c.GeneratedConfig)
	}
}

// rebuild --tools re-renders the whole document. Dropping the mount here would
// be invisible until the next up, which would start a container with an empty
// workspace and the operator's work still in an unreferenced volume.
func TestRebuildToolsKeepsTheVolumeMount(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{noFolder: true, tools: []string{"yq"}, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "scratch", "", opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := rewriteGeneratedTools(a, "", "scratch", []string{"+node"}); err != nil {
		t.Fatalf("rewriteGeneratedTools: %v", err)
	}

	st, err := a.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	c, err := st.GetContainer("ws", "scratch")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if !strings.Contains(c.GeneratedConfig, "workspaceMount") {
		t.Errorf("rebuild --tools dropped the volume mount:\n%s", c.GeneratedConfig)
	}
	if !strings.Contains(c.GeneratedConfig, "node") {
		t.Errorf("rebuild --tools did not add node:\n%s", c.GeneratedConfig)
	}
}

// An empty SOURCE column reads as a bug in the table. A dash says "there is
// none" rather than "something went wrong printing it".
func TestListShowsADashForAFolderlessContainer(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{noFolder: true, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "scratch", "", opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	out.Reset()

	if err := runContainerList(t.Context(), a, "", false); err != nil {
		t.Fatalf("list: %v", err)
	}
	line := ""
	for _, l := range strings.Split(out.String(), "\n") {
		if strings.Contains(l, "scratch") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("scratch is not in the list:\n%s", out.String())
	}
	if !strings.HasSuffix(strings.TrimSpace(line), "-") {
		t.Errorf("SOURCE column is not a dash: %q", line)
	}
}
