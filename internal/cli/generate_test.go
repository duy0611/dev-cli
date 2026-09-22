package cli

import (
	"encoding/json"
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

// A project-owned container is built from the merged document, not from the
// file on disk, and that document lives only for one invocation — so this is
// the only way to see what the container was actually created from.
func TestConfigShowPrintsTheMergedConfig(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)

	folder := projectWithConfig(t)
	if err := runContainerCreate(t.Context(), a, "", "owned", folder, createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	out.Reset()

	if err := runContainerConfigShow(a, "", "owned"); err != nil {
		t.Fatalf("config show: %v", err)
	}
	// The mount dev added is the whole point: it is the one thing that is in
	// the running container and not in the file the operator committed.
	if !strings.Contains(out.String(), "dev-ws-owned-state") {
		t.Errorf("config show did not print the merged document:\n%s", out.String())
	}
	if !strings.Contains(out.String(), dcgen.StateDir) {
		t.Errorf("merged document names no state mount:\n%s", out.String())
	}
}

// Without state there is no merge, so what the CLI receives really is the
// project's own file. Printing it anyway keeps the command one shape.
func TestConfigShowPrintsTheProjectFileWhenStateIsOff(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)

	folder := projectWithConfig(t)
	opts := createOpts{noStart: true, noPersistState: true}
	if err := runContainerCreate(t.Context(), a, "", "plain", folder, opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	out.Reset()

	if err := runContainerConfigShow(a, "", "plain"); err != nil {
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

// Stdout carries the document and nothing else, so `config show | jq` works.
// The path that says where it came from goes to stderr.
func TestConfigShowPrintsOnlyJSONToStdout(t *testing.T) {
	a, out := newTestApp(t)
	seedWorkspace(t, a)

	folder := projectWithConfig(t)
	if err := runContainerCreate(t.Context(), a, "", "owned", folder, createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	out.Reset()

	if err := runContainerConfigShow(a, "", "owned"); err != nil {
		t.Fatalf("config show: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON (%v):\n%s", err, out.String())
	}
}

// Fatal here, unlike on stop or remove: this command exists to say what the
// container was built from, and a file dev cannot parse has no honest answer
// other than the error.
func TestConfigShowReportsAnUnparseableProjectFile(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	folder := t.TempDir()
	writeProjectConfigIn(t, folder, `{"name":}`)
	if err := runContainerCreate(t.Context(), a, "", "broken", folder, createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}

	err := runContainerConfigShow(a, "", "broken")
	if err == nil {
		t.Fatal("config show printed a document for a file that does not parse")
	}
	if !strings.Contains(err.Error(), "devcontainer.json") {
		t.Errorf("error %q does not name the file", err)
	}
}

// The devcontainer CLI only accepts a path, so a configuration that lives in
// the database has to become a file for the length of one invocation — and
// stop being one afterwards.
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
	// By column rather than by suffix: the row has gained a field since, and a
	// suffix check would silently start asserting on whatever is last.
	fields := strings.Fields(line)
	const sourceCol = 3
	if len(fields) <= sourceCol || fields[sourceCol] != "-" {
		t.Errorf("SOURCE column is not a dash: %q", line)
	}
}

// On by default, and the column is what every later command reads: the
// document is re-rendered from it, the local provider mounts from it, and the
// k8s manifest grows a subPath from it.
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

// The sharpest edge in the change. rewriteGeneratedTools re-renders the whole
// document, so a state mount it does not put back is gone — and the next up
// would start a container with no volume, quietly, leaving the old one behind
// holding the plugins the operator is about to notice missing.
func TestRebuildToolsKeepsTheStateMount(t *testing.T) {
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
	for _, want := range []string{dcgen.StateDir, "CLAUDE_CONFIG_DIR", "dev-ws-scratch-state"} {
		if !strings.Contains(c.GeneratedConfig, want) {
			t.Errorf("rebuild --tools dropped %s:\n%s", want, c.GeneratedConfig)
		}
	}
	if !strings.Contains(c.GeneratedConfig, "node") {
		t.Errorf("rebuild --tools did not add node:\n%s", c.GeneratedConfig)
	}
}

// A container that does not persist state must not acquire a mount from a
// rebuild either: that would create a volume nobody asked for, and Remove only
// deletes one when the column says there is one.
func TestRebuildToolsAddsNoStateMountWhenOff(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{noFolder: true, noPersistState: true, tools: []string{"yq"}, noStart: true}
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
	if strings.Contains(c.GeneratedConfig, dcgen.StateDir) {
		t.Errorf("rebuild --tools added a state mount to a container without one:\n%s",
			c.GeneratedConfig)
	}
}

// --no-persist-state is the way out, and it has to reach the row: a container
// created with it must get no volume, no mount and no variables.
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

// A project that ships its own devcontainer.json gets no "containerEnv" from
// dev — dev does not write into a project folder — so the variables have to
// come from here or that half of the containers would mount the volume and
// leave every agent still writing to its default path inside the container.
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

// Without the column there is no volume, so pointing an agent at /var/dev-state
// would send it writing into the container filesystem at a path nothing mounts
// — losing the state on rebuild exactly as before, but less visibly.
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

// A workspace setting of the same name wins, the way the git identity does:
// these are defaults the operator may have a better answer for.
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
	// Exactly one: both would arrive as --remote-env and the last would win,
	// which is a coin toss rather than a precedence rule.
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

// A compose project cannot carry the state mount, and nothing reports an error
// when it silently does not: the agents write to the container filesystem and
// lose it at the next rebuild.
func TestComposeWarningForAComposeProject(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfigIn(t, dir, `{"dockerComposeFile":"docker-compose.yml","service":"app"}`)
	path := filepath.Join(dir, ".devcontainer", "devcontainer.json")

	got := composeWarning(path, true)
	if got == "" {
		t.Fatal("no warning for a compose project")
	}
	// The way out has to be in the message, in the house style of the
	// kind-change error: an operator who is not told what to do has nothing to
	// try.
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

// Nothing to lose, nothing to warn about.
func TestComposeWarningSilentWithoutPersistState(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfigIn(t, dir, `{"dockerComposeFile":"docker-compose.yml"}`)
	path := filepath.Join(dir, ".devcontainer", "devcontainer.json")

	if got := composeWarning(path, false); got != "" {
		t.Errorf("warned for a container that persists no state: %q", got)
	}
}

// A generated container has no project config, and an unreadable one is the
// business of the commands that need it.
func TestComposeWarningSilentWithoutAConfig(t *testing.T) {
	if got := composeWarning("", true); got != "" {
		t.Errorf("warned with no config path: %q", got)
	}
	if got := composeWarning("/nonexistent/devcontainer.json", true); got != "" {
		t.Errorf("warned for an unreadable config: %q", got)
	}
}

// Still created: the warning is advisory, not a refusal.
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

// The guarantee the deferred parse error exists for: a project file dev cannot
// parse must not strand its container in the engine.
//
// materialise runs for every container command, but stop, remove, logs and
// status find their container through docker label filters and never read a
// config at all. Failing those too would leave a syntax error in a project's
// devcontainer.json holding the container hostage, with dev refusing to clean
// up after itself and the operator sent to docker directly.
//
// The mechanism is asserted in resolve_test.go; this asserts the outcome, and
// it is what fails loudly if a future command forgets which side of the
// deferred/fatal line it belongs on.
func TestRemoveSucceedsWithAnUnparseableProjectFile(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	folder := t.TempDir()
	writeProjectConfigIn(t, folder, `{"name":}`)

	if err := runContainerCreate(t.Context(), a, "", "c1", folder,
		createOpts{noStart: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// --force, because make test runs with no engine on PATH: what is being
	// asserted is that the deferred parse error never reaches this command,
	// not what docker would have said.
	if err := runContainerRemove(t.Context(), a, "", "c1", true); err != nil {
		t.Fatalf("a broken project file stranded the container: %v", err)
	}
}
