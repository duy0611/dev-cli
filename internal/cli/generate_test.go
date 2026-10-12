package cli

import (
	"encoding/json"
	"github.com/duy0611/dev-cli/internal/xpath"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/duy0611/dev-cli/internal/dcgen"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider/k8s"
)

// seedWorkspace gives a test app an active workspace and provider.
func seedWorkspace(t *testing.T, a *app) {
	t.Helper()
	if err := runProviderConfigure(a, "p", "local", k8s.Config{}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if err := runWorkspaceInit(a, "ws", "p", false); err != nil {
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

// Scripted path: go test's stdin is not a terminal, so no picker opens.
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

	entries, err := os.ReadDir(folder)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("create wrote into the project folder: %v", entries)
	}
}

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

// projectWithConfig returns a folder that ships its own devcontainer config.
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

func TestConfigShowPrintsTheStoredConfig(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{generate: true, tools: []string{"yq"}, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "demo", t.TempDir(), opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	out.Reset()

	if err := runContainerConfigShow(t.Context(), a, "", "demo"); err != nil {
		t.Fatalf("config show: %v", err)
	}
	if !strings.Contains(out.String(), "apt-get-packages") {
		t.Errorf("config show printed nothing useful:\n%s", out.String())
	}
}

func TestConfigShowPrintsTheMergedConfig(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)

	folder := projectWithConfig(t)
	if err := runContainerCreate(t.Context(), a, "", "owned", folder, createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	out.Reset()

	if err := runContainerConfigShow(t.Context(), a, "", "owned"); err != nil {
		t.Fatalf("config show: %v", err)
	}
	if !strings.Contains(out.String(), "dev-ws-owned-state") {
		t.Errorf("config show did not print the merged document:\n%s", out.String())
	}
	if !strings.Contains(out.String(), dcgen.StateDir) {
		t.Errorf("merged document names no state mount:\n%s", out.String())
	}
}

func TestConfigShowPrintsTheProjectFileWhenStateIsOff(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)

	folder := projectWithConfig(t)
	opts := createOpts{noStart: true, noPersistState: true}
	if err := runContainerCreate(t.Context(), a, "", "plain", folder, opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	out.Reset()

	if err := runContainerConfigShow(t.Context(), a, "", "plain"); err != nil {
		t.Fatalf("config show: %v", err)
	}
	if strings.Contains(out.String(), dcgen.StateDir) {
		t.Errorf("config show invented a state mount for a container without one:\n%s",
			out.String())
	}
	if out.String() == "" {
		t.Error("config show printed no document at all")
	}
}

func TestConfigShowPrintsOnlyJSONToStdout(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)

	folder := projectWithConfig(t)
	if err := runContainerCreate(t.Context(), a, "", "owned", folder, createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	out.Reset()

	if err := runContainerConfigShow(t.Context(), a, "", "owned"); err != nil {
		t.Fatalf("config show: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON (%v):\n%s", err, out.String())
	}
}

func TestConfigShowReportsAnUnparseableProjectFile(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	folder := t.TempDir()
	writeProjectConfigIn(t, folder, `{"name":}`)
	if err := runContainerCreate(t.Context(), a, "", "broken", folder, createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}

	err := runContainerConfigShow(t.Context(), a, "", "broken")
	if err == nil {
		t.Fatal("config show printed a document for a file that does not parse")
	}
	if !strings.Contains(err.Error(), "devcontainer.json") {
		t.Errorf("error %q does not name the file", err)
	}
}

func TestMaterialiseWritesAndCleansUp(t *testing.T) {
	c := model.Container{Name: "demo", GeneratedConfig: "{\"name\":\"demo\"}\n"}

	got, cleanup, err := materialise(c, true)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	if got.ConfigPath == "" {
		t.Fatal("materialise produced no config path")
	}
	if filepath.Base(got.ConfigPath) != "devcontainer.json" ||
		filepath.Base(filepath.Dir(got.ConfigPath)) != ".devcontainer" {
		// The CLI rejects any filename but devcontainer.json.
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

	got, cleanup, err := materialise(c, true)
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

	// Engine-free: asserts the rewrite, not the rebuild.
	if err := rewriteGeneratedTools(t.Context(), a, "", "demo", []string{"+python"}); err != nil {
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

func TestRebuildToolsRefusesAProjectOwnedContainer(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	folder := projectWithConfig(t)
	if err := runContainerCreate(t.Context(), a, "", "owned", folder, createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}

	err := rewriteGeneratedTools(t.Context(), a, "", "owned", []string{"+yq"})
	if err == nil {
		t.Fatal("--tools rewrote a project-owned container")
	}
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d", got, exitUsage)
	}
}

func TestCreateWithNeitherFlagUsesTheWorkingDirectory(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)
	project := projectWithConfig(t)
	t.Chdir(project)

	if err := runContainerCreate(t.Context(), a, "", "api", "", createOpts{noStart: true}); err != nil {
		t.Fatalf("create with no --folder: %v", err)
	}
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetContainer("ws", "api")
	if err != nil {
		t.Fatal(err)
	}
	want, err := xpath.Resolve(project)
	if err != nil {
		t.Fatal(err)
	}
	if c.Source != want {
		t.Errorf("source = %q, want the working directory %q", c.Source, want)
	}
	if !strings.Contains(out.String(), "(the current directory)") {
		t.Errorf("output %q does not say which folder it used", out)
	}
}

func TestCreateRefusesTheHomeDirectoryAndRoot(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	// A config of its own leaves only the home-directory check to refuse it.
	home := projectWithConfig(t)
	t.Setenv("HOME", home)

	for _, c := range []struct{ label, cwd, folder string }{
		{"home as the working directory", home, ""},
		{"home as --folder", t.TempDir(), home},
		{"/ as --folder", t.TempDir(), "/"},
	} {
		t.Run(c.label, func(t *testing.T) {
			t.Chdir(c.cwd)
			err := runContainerCreate(t.Context(), a, "", "api", c.folder, createOpts{noStart: true})
			if got := exitCodeOf(err); got != exitUsage {
				t.Fatalf("exit code = %d (%v), want %d", got, err, exitUsage)
			}
			if !strings.Contains(err.Error(), "everything under it") {
				t.Errorf("error %q does not say why the folder is refused", err)
			}
		})
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

// Scripted path: a bare image with no tools, and no --generate needed.
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
	if strings.Contains(c.GeneratedConfig, "apt-get-packages") {
		t.Errorf("bare no-folder container installed a tool it was not asked for:\n%s", c.GeneratedConfig)
	}
	if !strings.Contains(c.GeneratedConfig, "workspaceMount") {
		t.Errorf("bare no-folder container lost its volume mount:\n%s", c.GeneratedConfig)
	}
}

// rebuild --tools re-renders the whole document, so a dropped mount goes
// unnoticed until the next up.
func TestRebuildToolsKeepsTheVolumeMount(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{noFolder: true, tools: []string{"yq"}, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "scratch", "", opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := rewriteGeneratedTools(t.Context(), a, "", "scratch", []string{"+node"}); err != nil {
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

func TestListShowsADashForAFolderlessContainer(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{noFolder: true, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "scratch", "", opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	out.Reset()

	if err := runContainerList(t.Context(), a, "", false, defaultListTimeout); err != nil {
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
	// By column: a suffix check would assert on whichever field is last.
	fields := strings.Fields(line)
	const sourceCol = 3
	if len(fields) <= sourceCol || fields[sourceCol] != "-" {
		t.Errorf("SOURCE column is not a dash: %q", line)
	}
}

func TestListShowsWhereTheConfigCameFrom(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)

	if err := runContainerCreate(t.Context(), a, "", "own", projectWithConfig(t), createOpts{noStart: true}); err != nil {
		t.Fatalf("create own: %v", err)
	}
	if err := runContainerCreate(t.Context(), a, "", "gen", t.TempDir(), createOpts{generate: true, noStart: true}); err != nil {
		t.Fatalf("create gen: %v", err)
	}
	if err := runContainerCreate(t.Context(), a, "", "scratch", "", createOpts{noFolder: true, noStart: true}); err != nil {
		t.Fatalf("create scratch: %v", err)
	}
	out.Reset()

	if err := runContainerList(t.Context(), a, "", false, defaultListTimeout); err != nil {
		t.Fatalf("list: %v", err)
	}
	const configCol = 4
	want := map[string]string{"own": "project", "gen": "generated", "scratch": "generated"}
	for _, l := range strings.Split(out.String(), "\n") {
		fields := strings.Fields(l)
		if len(fields) < 2 {
			continue
		}
		w, ok := want[fields[1]]
		if !ok {
			continue
		}
		delete(want, fields[1])
		if len(fields) <= configCol || fields[configCol] != w {
			t.Errorf("CONFIG column for %s is not %s: %q", fields[1], w, l)
		}
	}
	for name := range want {
		t.Errorf("%s is not in the list:\n%s", name, out.String())
	}
}

func TestCreateStoresPersistState(t *testing.T) {
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
	if !c.PersistState {
		t.Error("create did not default to persisting state")
	}
	if !strings.Contains(c.GeneratedConfig, dcgen.StateDir) {
		t.Errorf("the generated document has no state mount:\n%s", c.GeneratedConfig)
	}
}

// rewriteGeneratedTools would silently drop a state mount it does not re-add.
func TestRebuildToolsKeepsTheStateMount(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{noFolder: true, tools: []string{"yq"}, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "scratch", "", opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := rewriteGeneratedTools(t.Context(), a, "", "scratch", []string{"+node"}); err != nil {
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
	for _, want := range []string{dcgen.StateDir, "CLAUDE_CONFIG_DIR", "dev-ws-scratch-state"} {
		if !strings.Contains(c.GeneratedConfig, want) {
			t.Errorf("rebuild --tools dropped %s:\n%s", want, c.GeneratedConfig)
		}
	}
	if !strings.Contains(c.GeneratedConfig, "node") {
		t.Errorf("rebuild --tools did not add node:\n%s", c.GeneratedConfig)
	}
}

func TestRebuildToolsAddsNoStateMountWhenOff(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{noFolder: true, noPersistState: true, tools: []string{"yq"}, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "scratch", "", opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := rewriteGeneratedTools(t.Context(), a, "", "scratch", []string{"+node"}); err != nil {
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
	if strings.Contains(c.GeneratedConfig, dcgen.StateDir) {
		t.Errorf("rebuild --tools added a state mount to a container without one:\n%s",
			c.GeneratedConfig)
	}
}

func TestCreateHonoursNoPersistState(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{noFolder: true, noPersistState: true, noStart: true}
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
	if c.PersistState {
		t.Error("create ignored --no-persist-state")
	}
	if strings.Contains(c.GeneratedConfig, dcgen.StateDir) {
		t.Errorf("the generated document has a state mount:\n%s", c.GeneratedConfig)
	}
}

// A project-owned container has no containerEnv, so the variables come from
// here.
func TestContainerEnvCarriesTheStateVariables(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	c := model.Container{Name: "api", WorkspaceName: "ws", PersistState: true}
	environ, err := a.containerEnv(t.Context(), c)
	if err != nil {
		t.Fatalf("containerEnv: %v", err)
	}

	got := map[string]string{}
	for _, e := range environ {
		got[e.Key] = e.Value
	}
	for key, want := range dcgen.StateEnv() {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
}

func TestContainerEnvOmitsStateVariablesWhenOff(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	environ, err := a.containerEnv(t.Context(), model.Container{Name: "api", WorkspaceName: "ws"})
	if err != nil {
		t.Fatalf("containerEnv: %v", err)
	}
	for _, e := range environ {
		if strings.Contains(e.Value, dcgen.StateDir) {
			t.Errorf("%s points at the state volume for a container without one", e.Key)
		}
	}
}

func TestWorkspaceSettingOverridesAStateVariable(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	if err := runWorkspaceSet(a, "", "CLAUDE_CONFIG_DIR", "literal:/elsewhere"); err != nil {
		t.Fatalf("workspace set: %v", err)
	}

	c := model.Container{Name: "api", WorkspaceName: "ws", PersistState: true}
	environ, err := a.containerEnv(t.Context(), c)
	if err != nil {
		t.Fatalf("containerEnv: %v", err)
	}

	var seen int
	for _, e := range environ {
		if e.Key == "CLAUDE_CONFIG_DIR" {
			seen++
			if e.Value != "/elsewhere" {
				t.Errorf("CLAUDE_CONFIG_DIR = %q, want the workspace's value", e.Value)
			}
		}
	}
	if seen != 1 {
		t.Errorf("CLAUDE_CONFIG_DIR appears %d times, want exactly 1", seen)
	}
}

// writeProjectConfigIn puts a .devcontainer/devcontainer.json in dir.
func writeProjectConfigIn(t *testing.T, dir, body string) {
	t.Helper()
	nested := filepath.Join(dir, ".devcontainer")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "devcontainer.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing the project config: %v", err)
	}
}

func TestComposeWarningForAComposeProject(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfigIn(t, dir, `{"dockerComposeFile":"docker-compose.yml","service":"app"}`)
	path := filepath.Join(dir, ".devcontainer", "devcontainer.json")

	got := composeWarning(path, true)
	if got == "" {
		t.Fatal("no warning for a compose project")
	}
	for _, want := range []string{"docker compose", dcgen.StateDir, "--no-persist-state"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning %q does not mention %q", got, want)
		}
	}
}

func TestComposeWarningSilentForAnImageProject(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfigIn(t, dir, `{"image":"ubuntu"}`)
	path := filepath.Join(dir, ".devcontainer", "devcontainer.json")

	if got := composeWarning(path, true); got != "" {
		t.Errorf("warned about a non-compose project: %q", got)
	}
}

func TestComposeWarningSilentWithoutPersistState(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfigIn(t, dir, `{"dockerComposeFile":"docker-compose.yml"}`)
	path := filepath.Join(dir, ".devcontainer", "devcontainer.json")

	if got := composeWarning(path, false); got != "" {
		t.Errorf("warned for a container that persists no state: %q", got)
	}
}

func TestComposeWarningSilentWithoutAConfig(t *testing.T) {
	if got := composeWarning("", true); got != "" {
		t.Errorf("warned with no config path: %q", got)
	}
	if got := composeWarning("/nonexistent/devcontainer.json", true); got != "" {
		t.Errorf("warned for an unreadable config: %q", got)
	}
}

func TestCreateSucceedsForAComposeProject(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	folder := t.TempDir()
	writeProjectConfigIn(t, folder, `{"dockerComposeFile":"docker-compose.yml","service":"app"}`)

	if err := runContainerCreate(t.Context(), a, "", "demo", folder,
		createOpts{noStart: true}); err != nil {
		t.Fatalf("create refused a compose project: %v", err)
	}
}

// A project file that will not parse must not strand the container; see
// resolve_test.go.
func TestRemoveSucceedsWithAnUnparseableProjectFile(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	folder := t.TempDir()
	writeProjectConfigIn(t, folder, `{"name":}`)

	if err := runContainerCreate(t.Context(), a, "", "c1", folder,
		createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// --force: no engine on PATH, so only the parse-error path is under test.
	if err := runContainerRemove(t.Context(), a, "", "c1", true); err != nil {
		t.Fatalf("a broken project file stranded the container: %v", err)
	}
}

// A wedged engine must not hang the list; past the timeout the row reads "?".
func TestListGivesUpOnASlowEngine(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{noFolder: true, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "scratch", "", opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	out.Reset()

	dir := t.TempDir()
	t.Setenv("PATH", dir)
	// exec, so the cancel kills the sleep, not a shell that holds stdout open.
	stubBin(t, dir, "docker", "exec /bin/sleep 30")

	start := time.Now()
	if err := runContainerList(t.Context(), a, "", false, 200*time.Millisecond); err != nil {
		t.Fatalf("list: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("list took %s; the timeout did not stop it", elapsed)
	}
	fields := []string{}
	for _, l := range strings.Split(out.String(), "\n") {
		if strings.Contains(l, "scratch") {
			fields = strings.Fields(l)
		}
	}
	const statusCol = 2
	if len(fields) <= statusCol || fields[statusCol] != "?" {
		t.Errorf("STATUS is not ?: %q", out.String())
	}
}

func TestListRefusesANonPositiveTimeout(t *testing.T) {
	a, _ := newTestApp(t)
	err := runContainerList(t.Context(), a, "", true, 0)
	if got := exitCodeOf(err); got != exitUsage {
		t.Errorf("exit code = %d, want %d (err: %v)", got, exitUsage, err)
	}
}
