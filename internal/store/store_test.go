package store

import (
	"errors"
	"path/filepath"
	"testing"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/model"
)

// openTest returns a Store backed by a temp file. Deliberately not
// ":memory:": that database is per-connection, and the pool would hand a
// migration to one connection and a query to another.
func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "dev.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seed creates a provider and a workspace, the two rows almost everything else
// references.
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
