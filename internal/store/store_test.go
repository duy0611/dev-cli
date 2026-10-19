package store

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/duy0611/dev-cli/internal/model"
)

// openTest returns a Store backed by a temp file, not ":memory:", which is
// per-connection.
func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "dev.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seed creates the provider and workspace most other rows reference.
func seed(t *testing.T, s *Store) {
	t.Helper()
	if err := s.PutProvider(model.Provider{Name: "local", Kind: model.KindLocal}); err != nil {
		t.Fatalf("PutProvider: %v", err)
	}
	if err := s.CreateWorkspace(model.Workspace{Name: "ws", ProviderName: "local"}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := s.PutProvider(model.Provider{Name: "local", Kind: model.KindLocal}); err != nil {
		t.Fatalf("PutProvider: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopening replays the migration list; already-applied ones must be
	// skipped rather than re-run against the existing tables.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() { _ = s2.Close() }()

	if _, err := s2.GetProvider("local"); err != nil {
		t.Fatalf("data did not survive reopen: %v", err)
	}
}

func TestProviderPutIsUpsert(t *testing.T) {
	s := openTest(t)

	if err := s.PutProvider(model.Provider{Name: "p", Kind: model.KindLocal}); err != nil {
		t.Fatalf("PutProvider: %v", err)
	}
	if err := s.PutProvider(model.Provider{Name: "p", Kind: model.KindK8s, Config: `{"ctx":"x"}`}); err != nil {
		t.Fatalf("PutProvider (update): %v", err)
	}

	got, err := s.GetProvider("p")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.Kind != model.KindK8s {
		t.Errorf("kind = %q, want %q", got.Kind, model.KindK8s)
	}
	if got.Config != `{"ctx":"x"}` {
		t.Errorf("config = %q, want %q", got.Config, `{"ctx":"x"}`)
	}
	if got.CreatedAt.IsZero() {
		t.Error("created_at did not round-trip")
	}

	list, err := s.ListProviders()
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("ListProviders returned %d rows, want 1 (upsert, not insert)", len(list))
	}
}

func TestGetMissingIsErrNotFound(t *testing.T) {
	s := openTest(t)

	if _, err := s.GetProvider("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetProvider: err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetWorkspace("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetWorkspace: err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetContainer("ws", "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetContainer: err = %v, want ErrNotFound", err)
	}
}

func TestDeleteMissingIsErrNotFound(t *testing.T) {
	s := openTest(t)
	seed(t, s)

	if err := s.DeleteWorkspace("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteWorkspace: err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteContainer("ws", "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteContainer: err = %v, want ErrNotFound", err)
	}
	if err := s.UnsetSetting("ws", "NOPE"); !errors.Is(err, ErrNotFound) {
		t.Errorf("UnsetSetting: err = %v, want ErrNotFound", err)
	}
}

func TestCreateWorkspaceRejectsDuplicate(t *testing.T) {
	s := openTest(t)
	seed(t, s)

	err := s.CreateWorkspace(model.Workspace{Name: "ws", ProviderName: "local"})
	if !errors.Is(err, ErrExists) {
		t.Errorf("CreateWorkspace: err = %v, want ErrExists", err)
	}
}

func TestContainerNameIsUniquePerWorkspaceOnly(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	if err := s.CreateWorkspace(model.Workspace{Name: "other", ProviderName: "local"}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	c := model.Container{
		Name:          "demo",
		WorkspaceName: "ws",
		SourceKind:    model.SourceFolder,
		Source:        "/tmp/demo",
		ConfigPath:    "/tmp/demo/.devcontainer/devcontainer.json",
	}
	if err := s.CreateContainer(c); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}

	// Same name, different workspace: allowed, and the reason workspaces exist.
	c.WorkspaceName = "other"
	if err := s.CreateContainer(c); err != nil {
		t.Fatalf("CreateContainer in a second workspace: %v", err)
	}

	// Same name, same workspace: refused.
	if err := s.CreateContainer(c); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate CreateContainer: err = %v, want ErrExists", err)
	}

	all, err := s.ListContainers("")
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("ListContainers(\"\") returned %d rows, want 2", len(all))
	}

	one, err := s.ListContainers("ws")
	if err != nil {
		t.Fatalf("ListContainers(ws): %v", err)
	}
	if len(one) != 1 {
		t.Errorf("ListContainers(ws) returned %d rows, want 1", len(one))
	}
}

func TestDeleteWorkspaceCascades(t *testing.T) {
	s := openTest(t)
	seed(t, s)

	if err := s.SetSetting("ws", "TOKEN", "keychain:tok"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := s.CreateContainer(model.Container{
		Name: "demo", WorkspaceName: "ws", SourceKind: model.SourceFolder,
		Source: "/tmp/demo", ConfigPath: "/tmp/demo/.devcontainer/devcontainer.json",
	}); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}

	if err := s.DeleteWorkspace("ws"); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}

	settings, err := s.ListSettings("ws")
	if err != nil {
		t.Fatalf("ListSettings: %v", err)
	}
	if len(settings) != 0 {
		t.Errorf("settings survived the cascade: %v", settings)
	}

	containers, err := s.ListContainers("ws")
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(containers) != 0 {
		t.Errorf("container records survived the cascade: %v", containers)
	}
}

func TestSettingsUpsertAndOrder(t *testing.T) {
	s := openTest(t)
	seed(t, s)

	if err := s.SetSetting("ws", "B_KEY", "literal:one"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := s.SetSetting("ws", "A_KEY", "literal:two"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := s.SetSetting("ws", "B_KEY", "literal:changed"); err != nil {
		t.Fatalf("SetSetting (update): %v", err)
	}

	got, err := s.ListSettings("ws")
	if err != nil {
		t.Fatalf("ListSettings: %v", err)
	}
	want := []model.Setting{
		{Key: "A_KEY", Spec: "literal:two"},
		{Key: "B_KEY", Spec: "literal:changed"},
	}
	if len(got) != len(want) {
		t.Fatalf("ListSettings returned %d rows, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("setting %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCloneWorkspaceCopiesSettings(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	for k, v := range map[string]string{"A_KEY": "literal:a", "B_KEY": "op://v/i/f"} {
		if err := s.SetSetting("ws", k, v); err != nil {
			t.Fatalf("SetSetting: %v", err)
		}
	}

	n, err := s.CloneWorkspace("ws", model.Workspace{Name: "copy", ProviderName: "local", SSHForward: true})
	if err != nil {
		t.Fatalf("CloneWorkspace: %v", err)
	}
	if n != 2 {
		t.Errorf("copied %d settings, want 2", n)
	}

	w, err := s.GetWorkspace("copy")
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if w.ProviderName != "local" || !w.SSHForward {
		t.Errorf("clone = %+v, want provider local with ssh forwarding", w)
	}
	got, err := s.ListSettings("copy")
	if err != nil {
		t.Fatalf("ListSettings: %v", err)
	}
	want := []model.Setting{{Key: "A_KEY", Spec: "literal:a"}, {Key: "B_KEY", Spec: "op://v/i/f"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("clone settings = %v, want %v", got, want)
	}

	// The copies are independent rows: changing one side leaves the other.
	if err := s.UnsetSetting("copy", "A_KEY"); err != nil {
		t.Fatalf("UnsetSetting: %v", err)
	}
	if src, _ := s.ListSettings("ws"); len(src) != 2 {
		t.Errorf("source lost a setting to its clone: %v", src)
	}
}

func TestCloneWorkspaceRefusesAndLeavesNothing(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	if err := s.SetSetting("ws", "A_KEY", "literal:a"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	if _, err := s.CloneWorkspace("ws", model.Workspace{Name: "ws", ProviderName: "local"}); !errors.Is(err, ErrExists) {
		t.Errorf("clone onto an existing name: err = %v, want ErrExists", err)
	}
	if _, err := s.CloneWorkspace("nope", model.Workspace{Name: "copy", ProviderName: "local"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("clone of a missing source: err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetWorkspace("copy"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a refused clone left a workspace behind: err = %v", err)
	}
}

func TestActiveWorkspace(t *testing.T) {
	s := openTest(t)
	seed(t, s)

	active, err := s.ActiveWorkspace()
	if err != nil {
		t.Fatalf("ActiveWorkspace: %v", err)
	}
	if active != "" {
		t.Errorf("fresh database has active workspace %q, want empty", active)
	}

	if err := s.SetActiveWorkspace("ws"); err != nil {
		t.Fatalf("SetActiveWorkspace: %v", err)
	}
	active, err = s.ActiveWorkspace()
	if err != nil {
		t.Fatalf("ActiveWorkspace: %v", err)
	}
	if active != "ws" {
		t.Errorf("active workspace = %q, want %q", active, "ws")
	}

	// Pointing at a workspace that does not exist has to fail here, not later.
	if err := s.SetActiveWorkspace("ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetActiveWorkspace(ghost): err = %v, want ErrNotFound", err)
	}

	if err := s.ClearActiveWorkspace(); err != nil {
		t.Fatalf("ClearActiveWorkspace: %v", err)
	}
	active, err = s.ActiveWorkspace()
	if err != nil {
		t.Fatalf("ActiveWorkspace: %v", err)
	}
	if active != "" {
		t.Errorf("active workspace = %q after clear, want empty", active)
	}
}

func TestForeignKeysAreEnforced(t *testing.T) {
	s := openTest(t)

	// No provider named "ghost": without PRAGMA foreign_keys = ON this
	// silently succeeds and the workspace points at nothing.
	err := s.CreateWorkspace(model.Workspace{Name: "ws", ProviderName: "ghost"})
	if err == nil {
		t.Fatal("CreateWorkspace against a missing provider succeeded, want a foreign-key error")
	}
}

func TestDeleteProvider(t *testing.T) {
	s := openTest(t)
	seed(t, s)

	// The foreign key from workspaces is the backstop behind the CLI's own
	// check; if it ever stops being enforced, a provider can be deleted out
	// from under a workspace.
	if err := s.DeleteProvider("local"); err == nil {
		t.Error("deleted a provider a workspace still references")
	}

	if err := s.DeleteWorkspace("ws"); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
	if err := s.DeleteProvider("local"); err != nil {
		t.Errorf("DeleteProvider on an unreferenced provider: %v", err)
	}
	if err := s.DeleteProvider("local"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second DeleteProvider: err = %v, want ErrNotFound", err)
	}
}

func TestWorkspacesUsing(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	if err := s.PutProvider(model.Provider{Name: "other", Kind: model.KindLocal}); err != nil {
		t.Fatalf("PutProvider: %v", err)
	}
	if err := s.CreateWorkspace(model.Workspace{Name: "beta", ProviderName: "local"}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	got, err := s.WorkspacesUsing("local")
	if err != nil {
		t.Fatalf("WorkspacesUsing: %v", err)
	}
	if len(got) != 2 || got[0] != "beta" || got[1] != "ws" {
		t.Errorf("WorkspacesUsing(local) = %v, want [beta ws]", got)
	}

	if got, err := s.WorkspacesUsing("other"); err != nil || len(got) != 0 {
		t.Errorf("WorkspacesUsing(other) = %v, %v; want empty", got, err)
	}
}

// A generated container keeps its configuration in the row, which must
// round-trip exactly.
func TestContainerRoundTripsAGeneratedConfig(t *testing.T) {
	s := openTest(t)
	seed(t, s)

	const cfg = "{\n  \"name\": \"demo\"\n}"
	want := model.Container{
		Name:            "demo",
		WorkspaceName:   "ws",
		SourceKind:      model.SourceFolder,
		Source:          "/tmp/demo",
		ConfigPath:      "",
		GeneratedConfig: cfg,
	}
	if err := s.CreateContainer(want); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}

	got, err := s.GetContainer("ws", "demo")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if got.GeneratedConfig != cfg {
		t.Errorf("GeneratedConfig = %q, want %q", got.GeneratedConfig, cfg)
	}
	if got.ConfigPath != "" {
		t.Errorf("ConfigPath = %q, want empty for a generated container", got.ConfigPath)
	}

	list, err := s.ListContainers("ws")
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(list) != 1 || list[0].GeneratedConfig != cfg {
		t.Errorf("ListContainers lost the generated config: %+v", list)
	}
}

// The configuration lives on the row, so deleting the workspace removes it too.
func TestDeletingAWorkspaceTakesTheGeneratedConfig(t *testing.T) {
	s := openTest(t)
	seed(t, s)

	c := model.Container{
		Name: "demo", WorkspaceName: "ws",
		SourceKind: model.SourceFolder, Source: "/tmp/demo",
		GeneratedConfig: "{\"name\":\"demo\"}\n",
	}
	if err := s.CreateContainer(c); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	if err := s.DeleteWorkspace("ws"); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}

	list, err := s.ListContainers("")
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("the container outlived its workspace: %+v", list)
	}
}

// A folderless container stores an empty source, not a default value.
func TestContainerRoundTripsAnEmptySource(t *testing.T) {
	s := openTest(t)
	seed(t, s)

	want := model.Container{
		Name: "scratch", WorkspaceName: "ws",
		SourceKind:      model.SourceNone,
		Source:          "",
		GeneratedConfig: `{"name":"scratch"}`,
	}
	if err := s.CreateContainer(want); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}

	got, err := s.GetContainer("ws", "scratch")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if got.SourceKind != model.SourceNone {
		t.Errorf("SourceKind = %q, want %q", got.SourceKind, model.SourceNone)
	}
	if got.Source != "" {
		t.Errorf("Source = %q, want empty", got.Source)
	}
}

// The agents.yaml choice must round-trip exactly, since every rebuild reads it.
func TestContainerRoundTripsAgentConfig(t *testing.T) {
	s := openTest(t)
	seed(t, s)

	c := model.Container{
		Name: "api", WorkspaceName: "ws", SourceKind: model.SourceFolder,
		Source: "/src/api", AgentConfig: "/home/me/agents.yaml", AgentConfigPending: true,
	}
	if err := s.CreateContainer(c); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	got, err := s.GetContainer("ws", "api")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if got.AgentConfig != c.AgentConfig || !got.AgentConfigPending {
		t.Errorf("got AgentConfig=%q pending=%v, want %q true",
			got.AgentConfig, got.AgentConfigPending, c.AgentConfig)
	}

	if err := s.SetAgentConfigPending("ws", "api", false); err != nil {
		t.Fatalf("SetAgentConfigPending: %v", err)
	}
	list, err := s.ListContainers("ws")
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(list) != 1 || list[0].AgentConfigPending {
		t.Errorf("pending not cleared: %+v", list)
	}
}

// A row written without either field reads as the migration's default for old
// rows.
func TestAgentConfigDefaultsToProjectFileAndNothingPending(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	if err := s.CreateContainer(model.Container{
		Name: "api", WorkspaceName: "ws", SourceKind: model.SourceNone,
	}); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	got, err := s.GetContainer("ws", "api")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if got.AgentConfig != "" || got.AgentConfigPending {
		t.Errorf("got %q %v, want empty and false", got.AgentConfig, got.AgentConfigPending)
	}
}

func TestSetAgentConfigPendingOnAMissingContainer(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	if err := s.SetAgentConfigPending("ws", "nope", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestContainerRoundTripsTheHardeningColumns(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	c := model.Container{
		Name: "api", WorkspaceName: "ws", SourceKind: model.SourceFolder, Source: "/src/api",
		GitGuard: true, AllowPrivileged: false, ConfigDigest: "sha256:abc",
	}
	if err := s.CreateContainer(c); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	got, err := s.GetContainer("ws", "api")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if !got.GitGuard || got.AllowPrivileged || got.ConfigDigest != "sha256:abc" {
		t.Errorf("got guard=%v allow=%v digest=%q", got.GitGuard, got.AllowPrivileged, got.ConfigDigest)
	}

	if err := s.SetConfigDigest("ws", "api", "sha256:def", "{}"); err != nil {
		t.Fatalf("SetConfigDigest: %v", err)
	}
	list, err := s.ListContainers("ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ConfigDigest != "sha256:def" {
		t.Errorf("after SetConfigDigest: %+v", list)
	}
	if err := s.SetConfigDigest("ws", "nope", "x", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing container: err = %v, want ErrNotFound", err)
	}
}

// A row written without the new columns must behave as a pre-migration row did.
func TestHardeningDefaultsSpeakForExistingRows(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	if _, err := s.db.Exec(
		`INSERT INTO containers (name, workspace_name, source_kind, source, config_path,
		   generated_config, persist_state, agent_config, agent_config_pending, created_at)
		 VALUES ('old', 'ws', 'folder', '/src/old', '', '', 0, '', 0, ?)`, nowString()); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := s.GetContainer("ws", "old")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if got.GitGuard || !got.AllowPrivileged || got.ConfigDigest != "" {
		t.Errorf("got guard=%v allow=%v digest=%q, want false true empty",
			got.GitGuard, got.AllowPrivileged, got.ConfigDigest)
	}
}

// Mode and WorktreeRepo must round-trip, since mounts are derived from them.
func TestContainerRoundTripsModeAndWorktreeRepo(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	if err := s.CreateContainer(model.Container{
		Name: "wt", WorkspaceName: "ws", SourceKind: model.SourceFolder, Source: "/wt/feat",
		Mode: model.ModeIsolated, WorktreeRepo: "/src/app/.git",
	}); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	got, err := s.GetContainer("ws", "wt")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if got.Mode != model.ModeIsolated || got.WorktreeRepo != "/src/app/.git" {
		t.Errorf("got mode=%q worktree_repo=%q, want isolated and the repo", got.Mode, got.WorktreeRepo)
	}
}

// A container built without a mode is written as legacy.
func TestContainerWithoutAModeIsLegacy(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	if err := s.CreateContainer(model.Container{
		Name: "old", WorkspaceName: "ws", SourceKind: model.SourceFolder, Source: "/src/old",
	}); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	got, err := s.GetContainer("ws", "old")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if got.Mode != model.ModeLegacy {
		t.Errorf("mode = %q, want legacy", got.Mode)
	}
}

// Extra folders round-trip in order of their host path, with the stored
// container path, and the primary stays in Source.
func TestContainerRoundTripsFolders(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	folders := []model.Folder{
		{HostPath: "/src/b", ContainerPath: "/workspaces/b"},
		{HostPath: "/wt/a", ContainerPath: "/wt/a", WorktreeRepo: "/src/app/.git"},
	}
	if err := s.CreateContainer(model.Container{
		Name: "multi", WorkspaceName: "ws", SourceKind: model.SourceFolder, Source: "/src/api",
		Mode: model.ModeIsolated, WorkspacePath: "/workspaces/api", Folders: folders,
	}); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	got, err := s.GetContainer("ws", "multi")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if got.Source != "/src/api" || got.WorkspacePath != "/workspaces/api" {
		t.Errorf("source=%q workspace_path=%q", got.Source, got.WorkspacePath)
	}
	want := folders
	if !slices.Equal(got.Folders, want) {
		t.Errorf("folders = %v, want %v", got.Folders, want)
	}
	list, err := s.ListContainers("ws")
	if err != nil || len(list) != 1 || !slices.Equal(list[0].Folders, want) {
		t.Errorf("ListContainers = %v, %v; want the folders loaded", list, err)
	}
}

func TestSetContainerFoldersReplacesTheSet(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	if err := s.CreateContainer(model.Container{
		Name: "multi", WorkspaceName: "ws", SourceKind: model.SourceFolder, Source: "/src/api",
		Mode: model.ModeGuarded, Folders: []model.Folder{{HostPath: "/src/a", ContainerPath: "/workspaces/a"}},
	}); err != nil {
		t.Fatal(err)
	}
	next := []model.Folder{{HostPath: "/src/c", ContainerPath: "/workspaces/c"}}
	if err := s.SetContainerFolders("ws", "multi", next); err != nil {
		t.Fatalf("SetContainerFolders: %v", err)
	}
	got, _ := s.GetContainer("ws", "multi")
	if !slices.Equal(got.Folders, next) {
		t.Errorf("folders = %v, want %v", got.Folders, next)
	}
	if err := s.SetContainerFolders("ws", "nope", next); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetContainerFolders on a missing container = %v, want ErrNotFound", err)
	}
}

func TestDeletingAContainerTakesItsFolders(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	c := model.Container{
		Name: "multi", WorkspaceName: "ws", SourceKind: model.SourceFolder, Source: "/src/api",
		Mode: model.ModeGuarded, Folders: []model.Folder{{HostPath: "/src/a", ContainerPath: "/workspaces/a"}},
	}
	if err := s.CreateContainer(c); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteContainer("ws", "multi"); err != nil {
		t.Fatal(err)
	}
	c.Folders = nil
	if err := s.CreateContainer(c); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetContainer("ws", "multi")
	if len(got.Folders) != 0 {
		t.Errorf("a recreated container inherited folders %v", got.Folders)
	}
}

func TestIdleStopRoundTrips(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	for name, want := range map[string]bool{"off": false, "on": true} {
		c := model.Container{Name: name, WorkspaceName: "ws", SourceKind: model.SourceFolder,
			Source: "/src/" + name, IdleStop: want}
		if err := s.CreateContainer(c); err != nil {
			t.Fatalf("CreateContainer %s: %v", name, err)
		}
		got, err := s.GetContainer("ws", name)
		if err != nil {
			t.Fatalf("GetContainer %s: %v", name, err)
		}
		if got.IdleStop != want {
			t.Errorf("GetContainer %s: IdleStop = %v, want %v", name, got.IdleStop, want)
		}
	}
	list, err := s.ListContainers("ws")
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	for _, c := range list {
		if want := c.Name == "on"; c.IdleStop != want {
			t.Errorf("ListContainers %s: IdleStop = %v, want %v", c.Name, c.IdleStop, want)
		}
	}
}

func TestIdleStopDefaultsOnForExistingRows(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	if _, err := s.db.Exec(
		`INSERT INTO containers (name, workspace_name, source_kind, source, config_path,
		   generated_config, persist_state, agent_config, agent_config_pending, created_at)
		 VALUES ('old', 'ws', 'folder', '/src/old', '', '', 0, '', 0, ?)`, nowString()); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := s.GetContainer("ws", "old")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if !got.IdleStop {
		t.Error("an existing row came back without idle stop")
	}
}
