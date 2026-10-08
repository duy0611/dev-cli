package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/duy0611/dev-cli/internal/dcgen"
	"github.com/duy0611/dev-cli/internal/gitwt"
	"github.com/duy0611/dev-cli/internal/model"
)

// createSource makes a generated, folderless container to clone from.
//
// generate is set explicitly: a scripted --no-folder create without it renders
// no tools at all, which would leave every comparison below equal and empty.
func createSource(t *testing.T, a *app, name string, opts createOpts) model.Container {
	t.Helper()
	opts.noFolder, opts.noStart, opts.generate = true, true, true
	if err := runContainerCreate(t.Context(), a, "", name, "", opts); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return getContainer(t, a, name)
}

func getContainer(t *testing.T, a *app, name string) model.Container {
	t.Helper()
	st, err := a.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	c, err := st.GetContainer("ws", name)
	if err != nil {
		t.Fatalf("GetContainer %s: %v", name, err)
	}
	return c
}

func toolsOf(t *testing.T, c model.Container) []string {
	t.Helper()
	tools, err := dcgen.ToolsOf(c.GeneratedConfig)
	if err != nil {
		t.Fatalf("ToolsOf %s: %v", c.Name, err)
	}
	return tools
}

func TestCreateFromCopiesTheSourceTools(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	src := createSource(t, a, "api", createOpts{tools: []string{"go", "yq"}})

	opts := createOpts{from: "api", noFolder: true, noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "scratch", "", opts); err != nil {
		t.Fatalf("create --from: %v", err)
	}
	c := getContainer(t, a, "scratch")

	want := toolsOf(t, src)
	if len(want) == 0 {
		t.Fatal("the source installs no tools, so this test proves nothing")
	}
	if got := toolsOf(t, c); !slices.Equal(got, want) {
		t.Errorf("tools = %v, want the source's %v", got, want)
	}
	// The document is rendered for the new container, never copied: a copy
	// would mount the source's workspace and state volumes into this one.
	for _, want := range []string{"dev-ws-scratch", "dev-ws-scratch-state"} {
		if !strings.Contains(c.GeneratedConfig, want) {
			t.Errorf("document does not name %s:\n%s", want, c.GeneratedConfig)
		}
	}
	if strings.Contains(c.GeneratedConfig, "dev-ws-api") {
		t.Errorf("document names the source's volumes:\n%s", c.GeneratedConfig)
	}
}

func TestCreateFromIntoAFolder(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	src := createSource(t, a, "api", createOpts{tools: []string{"go"}})

	opts := createOpts{from: "api", noStart: true}
	if err := runContainerCreate(t.Context(), a, "", "other", t.TempDir(), opts); err != nil {
		t.Fatalf("create --from --folder: %v", err)
	}
	c := getContainer(t, a, "other")
	if got, want := toolsOf(t, c), toolsOf(t, src); !slices.Equal(got, want) {
		t.Errorf("tools = %v, want %v", got, want)
	}
	if c.SourceKind != model.SourceFolder {
		t.Errorf("SourceKind = %q, want folder", c.SourceKind)
	}
}

func TestCreateFromAppliesAToolDiff(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	createSource(t, a, "api", createOpts{tools: []string{"go", "yq"}})

	opts := createOpts{from: "api", noFolder: true, noStart: true, tools: []string{"+node", "-go"}}
	if err := runContainerCreate(t.Context(), a, "", "scratch", "", opts); err != nil {
		t.Fatalf("create --from --tools: %v", err)
	}
	got := toolsOf(t, getContainer(t, a, "scratch"))
	if !slices.Contains(got, "node") || !slices.Contains(got, "yq") || slices.Contains(got, "go") {
		t.Errorf("tools = %v, want yq and node without go", got)
	}
}

// applyToolDiff treats a plain list as a replacement, which would silently
// discard everything --from exists to carry.
func TestCreateFromRejectsAPlainToolList(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	createSource(t, a, "api", createOpts{tools: []string{"go"}})

	opts := createOpts{from: "api", noFolder: true, noStart: true, tools: []string{"node"}}
	err := runContainerCreate(t.Context(), a, "", "scratch", "", opts)
	if got := exitCodeOf(err); got != exitUsage {
		t.Fatalf("exit = %d (%v), want %d", got, err, exitUsage)
	}
}

func TestCreateFromAMissingSource(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)

	opts := createOpts{from: "nope", noFolder: true, noStart: true}
	err := runContainerCreate(t.Context(), a, "", "scratch", "", opts)
	if got := exitCodeOf(err); got != exitNotFound {
		t.Fatalf("exit = %d (%v), want %d", got, err, exitNotFound)
	}
}

func TestCreateFromAProjectOwnedSource(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	if err := runContainerCreate(t.Context(), a, "", "api", projectWithConfig(t),
		createOpts{noStart: true}); err != nil {
		t.Fatalf("create source: %v", err)
	}

	opts := createOpts{from: "api", noFolder: true, noStart: true}
	err := runContainerCreate(t.Context(), a, "", "scratch", "", opts)
	if got := exitCodeOf(err); got != exitUsage {
		t.Fatalf("exit = %d (%v), want %d", got, err, exitUsage)
	}
	st, _ := a.store()
	if _, err := st.GetContainer("ws", "scratch"); err == nil {
		t.Error("a refused --from still wrote a row")
	}
}

func TestCreateFromInheritsPersistAndAgentConfig(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	file := filepath.Join(t.TempDir(), "agents.yaml")
	writeFile(t, file, "version: 1\n")
	resolved, _ := filepath.EvalSymlinks(file)

	createSource(t, a, "off", createOpts{noPersistState: true, noAgentConfig: true})
	createSource(t, a, "path", createOpts{agentConfig: file})
	createSource(t, a, "dflt", createOpts{})

	tests := []struct {
		name        string
		opts        createOpts
		persist     bool
		agentConfig string
	}{
		{"from-off", createOpts{from: "off"}, false, agentConfigNone},
		{"from-path", createOpts{from: "path"}, true, resolved},
		{"from-dflt", createOpts{from: "dflt"}, true, ""},
		{"override-persist", createOpts{from: "path", noPersistState: true}, false, resolved},
		{"override-none", createOpts{from: "path", noAgentConfig: true}, true, agentConfigNone},
		{"override-path", createOpts{from: "off", agentConfig: file}, false, resolved},
	}
	for _, tt := range tests {
		opts := tt.opts
		opts.noFolder, opts.noStart = true, true
		if err := runContainerCreate(t.Context(), a, "", tt.name, "", opts); err != nil {
			t.Errorf("%s: create: %v", tt.name, err)
			continue
		}
		c := getContainer(t, a, tt.name)
		if c.PersistState != tt.persist {
			t.Errorf("%s: PersistState = %v, want %v", tt.name, c.PersistState, tt.persist)
		}
		if c.AgentConfig != tt.agentConfig {
			t.Errorf("%s: AgentConfig = %q, want %q", tt.name, c.AgentConfig, tt.agentConfig)
		}
	}
}

func TestCreateFromRefusesAFolderWithAConfig(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	createSource(t, a, "api", createOpts{tools: []string{"go"}})

	opts := createOpts{from: "api", noStart: true}
	err := runContainerCreate(t.Context(), a, "", "other", projectWithConfig(t), opts)
	if got := exitCodeOf(err); got != exitUsage {
		t.Fatalf("exit = %d (%v), want %d", got, err, exitUsage)
	}
}

func TestCreateFromLeavesTheSourceAlone(t *testing.T) {
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	before := createSource(t, a, "api", createOpts{tools: []string{"go"}})

	opts := createOpts{from: "api", noFolder: true, noStart: true, tools: []string{"+node"}}
	if err := runContainerCreate(t.Context(), a, "", "scratch", "", opts); err != nil {
		t.Fatalf("create --from: %v", err)
	}
	after := getContainer(t, a, "api")
	if after.GeneratedConfig != before.GeneratedConfig || after.PersistState != before.PersistState ||
		after.AgentConfig != before.AgentConfig {
		t.Errorf("source changed:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestWorktreeCreateFromCopiesTools(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	src := createSource(t, a, "api", createOpts{tools: []string{"go"}})
	repo := initRepo(t)

	opts := baseOpts(repo, filepath.Join(t.TempDir(), "wt"))
	opts.create = createOpts{from: "api", noStart: true}
	if err := runWorktreeCreate(t.Context(), a, "", "feat", opts); err != nil {
		t.Fatalf("worktree create --from: %v", err)
	}
	c := getContainer(t, a, "feat")
	if got, want := toolsOf(t, c), toolsOf(t, src); !slices.Equal(got, want) {
		t.Errorf("tools = %v, want %v", got, want)
	}
	st, _ := a.store()
	w, err := st.GetWorktree("ws", "feat")
	if err != nil {
		t.Fatalf("worktree row: %v", err)
	}
	for _, want := range []string{w.Repo, w.Path} {
		if !strings.Contains(c.GeneratedConfig, want) {
			t.Errorf("document does not bind %s:\n%s", want, c.GeneratedConfig)
		}
	}
}

// Validated before git runs, so a mistyped source leaves no checkout and no
// branch behind to clean up.
func TestWorktreeCreateFromAMissingSourceMakesNoCheckout(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)
	path := filepath.Join(t.TempDir(), "wt")

	opts := baseOpts(repo, path)
	opts.create = createOpts{from: "nope", noStart: true}
	err := runWorktreeCreate(t.Context(), a, "", "feat", opts)
	if got := exitCodeOf(err); got != exitNotFound {
		t.Fatalf("exit = %d (%v), want %d", got, err, exitNotFound)
	}
	if _, err := os.Stat(path); err == nil {
		t.Errorf("a refused --from left a checkout at %s", path)
	}
	if gitwt.BranchExists(t.Context(), repo, "feat") {
		t.Error("a refused --from created the branch")
	}
}

func TestWorktreeCreateFromAWorktreeSourceDropsItsBinds(t *testing.T) {
	requireGit(t)
	a, _ := newTestApp(t)
	seedWorkspace(t, a)
	repo := initRepo(t)

	first := baseOpts(repo, filepath.Join(t.TempDir(), "wt1"))
	if err := runWorktreeCreate(t.Context(), a, "", "one", first); err != nil {
		t.Fatalf("first worktree: %v", err)
	}
	st, _ := a.store()
	w1, err := st.GetWorktree("ws", "one")
	if err != nil {
		t.Fatal(err)
	}

	second := baseOpts(repo, filepath.Join(t.TempDir(), "wt2"))
	second.branch = "other"
	second.create = createOpts{from: "one", noStart: true}
	if err := runWorktreeCreate(t.Context(), a, "", "two", second); err != nil {
		t.Fatalf("second worktree: %v", err)
	}
	c := getContainer(t, a, "two")
	if strings.Contains(c.GeneratedConfig, w1.Path) {
		t.Errorf("document binds the source's checkout %s:\n%s", w1.Path, c.GeneratedConfig)
	}
}
