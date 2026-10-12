package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/duy0611/dev-cli/internal/model"
)

// seedRaw builds a database at an older schema, marking the migrations that
// produced it as applied. The schema is written out, not replayed from the
// embedded files, which describe the current schema.
func seedRaw(t *testing.T, path, schema string, applied ...string) {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("creating the old schema: %v", err)
	}
	if _, err := db.Exec(
		`CREATE TABLE schema_migrations (
		   version    TEXT PRIMARY KEY,
		   applied_at TEXT NOT NULL
		 )`); err != nil {
		t.Fatal(err)
	}
	for _, name := range applied {
		if _, err := db.Exec(
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			name, time.Now().UTC().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
}

// TestMigrationDropsGPGForward covers upgrading past the dropped gpg_forward
// column.
func TestMigrationDropsGPGForward(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.db")

	// The 0001 schema, as it was before 0003 removed the column.
	pre := `
CREATE TABLE workspaces (
  name          TEXT PRIMARY KEY,
  provider_name TEXT NOT NULL,
  ssh_forward   INTEGER NOT NULL DEFAULT 0,
  gpg_forward   INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL
);
INSERT INTO workspaces (name, provider_name, ssh_forward, gpg_forward, created_at)
VALUES ('legacy', 'local', 1, 1, '2026-09-01T00:00:00Z');

-- Carried at its 0002 shape because a database of that vintage had one, and a
-- later migration that touches it has to run against this seed too.
CREATE TABLE containers (
  name             TEXT NOT NULL,
  workspace_name   TEXT NOT NULL,
  source_kind      TEXT NOT NULL,
  source           TEXT NOT NULL,
  config_path      TEXT NOT NULL,
  generated_config TEXT NOT NULL DEFAULT '',
  created_at       TEXT NOT NULL,
  PRIMARY KEY (workspace_name, name)
);
`
	seedRaw(t, path, pre, "0001_init.sql", "0002_generated_config.sql")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("opening a pre-0003 database: %v", err)
	}
	defer func() { _ = s.Close() }()

	// The row survives the drop, and the flag that is still meaningful keeps
	// its value: dropping a column beside it must not disturb it.
	ws, err := s.GetWorkspace("legacy")
	if err != nil {
		t.Fatalf("reading a migrated workspace: %v", err)
	}
	if !ws.SSHForward {
		t.Error("ssh_forward was lost by the migration")
	}

	// And the column is actually gone, rather than merely unread.
	var n int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM pragma_table_info('workspaces') WHERE name = 'gpg_forward'`,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("gpg_forward is still on the workspaces table")
	}

	// A write path still works against the migrated schema.
	if err := s.CreateWorkspace(model.Workspace{
		Name: "fresh", ProviderName: "local", SSHForward: true,
	}); err != nil {
		t.Fatalf("creating a workspace after the migration: %v", err)
	}
}

// TestMigrationAddsPersistState covers upgrading past the persist_state column.
func TestMigrationAddsPersistState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.db")

	// The schema as it stood after 0003, before this column existed.
	pre := `
CREATE TABLE workspaces (
  name          TEXT PRIMARY KEY,
  provider_name TEXT NOT NULL,
  ssh_forward   INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL
);
INSERT INTO workspaces (name, provider_name, ssh_forward, created_at)
VALUES ('ws', 'local', 0, '2026-09-01T00:00:00Z');

CREATE TABLE containers (
  name             TEXT NOT NULL,
  workspace_name   TEXT NOT NULL,
  source_kind      TEXT NOT NULL,
  source           TEXT NOT NULL,
  config_path      TEXT NOT NULL,
  generated_config TEXT NOT NULL DEFAULT '',
  created_at       TEXT NOT NULL,
  PRIMARY KEY (workspace_name, name)
);
INSERT INTO containers
  (name, workspace_name, source_kind, source, config_path, generated_config, created_at)
VALUES ('legacy', 'ws', 'folder', '/projects/legacy', '', '{}', '2026-09-01T00:00:00Z');
`
	seedRaw(t, path, pre,
		"0001_init.sql", "0002_generated_config.sql", "0003_drop_gpg_forward.sql")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("opening a pre-0004 database: %v", err)
	}
	defer func() { _ = s.Close() }()

	// Off, not on: a container that predates the feature has no volume, and
	// reading it as persisting state would mount one that was never created
	// and remove it on the next `container remove`.
	c, err := s.GetContainer("ws", "legacy")
	if err != nil {
		t.Fatalf("reading a migrated container: %v", err)
	}
	if c.PersistState {
		t.Error("a container created before the column defaulted to persisting state")
	}
	if c.GeneratedConfig != "{}" {
		t.Errorf("generated_config = %q, want the value the row already had", c.GeneratedConfig)
	}
}

// TestMigrationAddsWorktrees covers upgrading past the worktrees table. The
// cascade must still fire, which needs PRAGMA foreign_keys = ON.
func TestMigrationAddsWorktrees(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.db")

	// The schema as it stood after 0004, written out rather than replayed from
	// the embedded files: the point is to start from the schema as it was.
	pre := `
CREATE TABLE providers (
  name TEXT PRIMARY KEY, kind TEXT NOT NULL,
  config TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL
);
CREATE TABLE workspaces (
  name TEXT PRIMARY KEY,
  provider_name TEXT NOT NULL REFERENCES providers(name),
  ssh_forward INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL
);
CREATE TABLE workspace_settings (
  workspace_name TEXT NOT NULL REFERENCES workspaces(name) ON DELETE CASCADE,
  key TEXT NOT NULL, spec TEXT NOT NULL,
  PRIMARY KEY (workspace_name, key)
);
CREATE TABLE containers (
  name TEXT NOT NULL,
  workspace_name TEXT NOT NULL REFERENCES workspaces(name) ON DELETE CASCADE,
  source_kind TEXT NOT NULL, source TEXT NOT NULL, config_path TEXT NOT NULL,
  generated_config TEXT NOT NULL DEFAULT '',
  persist_state INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  PRIMARY KEY (workspace_name, name)
);
CREATE TABLE app_state (key TEXT PRIMARY KEY, value TEXT NOT NULL);`

	seedRaw(t, path, pre,
		"0001_init.sql", "0002_generated_config.sql",
		"0003_drop_gpg_forward.sql", "0004_persist_state.sql")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	seedWorktreeContainer(t, s)
	if err := s.CreateWorktree(testWorktree()); err != nil {
		t.Fatalf("CreateWorktree after migrating: %v", err)
	}
	// 0008 replaced 0005's cascade from containers with one from workspaces;
	// that the later migration still applies over this one is the point here.
	if err := s.DeleteWorkspace("ws"); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
	if _, err := s.GetWorktree("ws", "feat"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the cascade did not fire after migrating: %v", err)
	}
}

// TestMigrationAddsContainerModes covers upgrading past the mode column.
// Existing rows become legacy, except folderless ones, and a worktree's common
// directory moves onto its container.
func TestMigrationAddsContainerModes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.db")

	// The schema as it stood after 0007.
	pre := `
CREATE TABLE providers (
  name TEXT PRIMARY KEY, kind TEXT NOT NULL,
  config TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL
);
INSERT INTO providers (name, kind, config, created_at) VALUES ('local', 'local', '{}', '2026-09-01T00:00:00Z');
CREATE TABLE workspaces (
  name TEXT PRIMARY KEY,
  provider_name TEXT NOT NULL REFERENCES providers(name),
  ssh_forward INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL
);
INSERT INTO workspaces (name, provider_name, ssh_forward, created_at) VALUES ('ws', 'local', 0, '2026-09-01T00:00:00Z');
CREATE TABLE workspace_settings (
  workspace_name TEXT NOT NULL REFERENCES workspaces(name) ON DELETE CASCADE,
  key TEXT NOT NULL, spec TEXT NOT NULL,
  PRIMARY KEY (workspace_name, key)
);
CREATE TABLE containers (
  name TEXT NOT NULL,
  workspace_name TEXT NOT NULL REFERENCES workspaces(name) ON DELETE CASCADE,
  source_kind TEXT NOT NULL, source TEXT NOT NULL, config_path TEXT NOT NULL,
  generated_config TEXT NOT NULL DEFAULT '',
  persist_state INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  agent_config TEXT NOT NULL DEFAULT '',
  agent_config_pending INTEGER NOT NULL DEFAULT 0,
  git_guard INTEGER NOT NULL DEFAULT 0,
  allow_privileged INTEGER NOT NULL DEFAULT 1,
  config_digest TEXT NOT NULL DEFAULT '',
  config_digest_fields TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (workspace_name, name)
);
INSERT INTO containers (name, workspace_name, source_kind, source, config_path, git_guard, created_at)
VALUES ('api', 'ws', 'folder', '/src/api', '', 1, '2026-09-01T00:00:00Z'),
       ('scratch', 'ws', 'none', '', '', 0, '2026-09-01T00:00:00Z'),
       ('feat', 'ws', 'folder', '/wt/feat', '', 1, '2026-09-01T00:00:00Z');
CREATE TABLE app_state (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE worktrees (
  workspace_name  TEXT NOT NULL,
  container_name  TEXT NOT NULL,
  repo            TEXT NOT NULL,
  branch          TEXT NOT NULL,
  path            TEXT NOT NULL,
  herdr_workspace TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  PRIMARY KEY (workspace_name, container_name),
  FOREIGN KEY (workspace_name, container_name)
    REFERENCES containers(workspace_name, name) ON DELETE CASCADE
);
INSERT INTO worktrees (workspace_name, container_name, repo, branch, path, herdr_workspace, created_at)
VALUES ('ws', 'feat', '/src/app/.git', 'feat', '/wt/feat', '7f2a', '2026-09-01T00:00:00Z');`

	seedRaw(t, path, pre,
		"0001_init.sql", "0002_generated_config.sql", "0003_drop_gpg_forward.sql",
		"0004_persist_state.sql", "0005_worktrees.sql", "0006_agent_config.sql",
		"0007_hardening.sql")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("opening a pre-0008 database: %v", err)
	}
	defer func() { _ = s.Close() }()

	for name, want := range map[string]model.Mode{
		"api": model.ModeLegacy, "scratch": model.ModeNone, "feat": model.ModeLegacy,
	} {
		c, err := s.GetContainer("ws", name)
		if err != nil {
			t.Fatalf("GetContainer %s: %v", name, err)
		}
		if c.Mode != want {
			t.Errorf("%s: mode = %q, want %q", name, c.Mode, want)
		}
	}

	feat, err := s.GetContainer("ws", "feat")
	if err != nil {
		t.Fatal(err)
	}
	if feat.WorktreeRepo != "/src/app/.git" {
		t.Errorf("feat: worktree_repo = %q, want the worktree row's repo", feat.WorktreeRepo)
	}
	api, err := s.GetContainer("ws", "api")
	if err != nil {
		t.Fatal(err)
	}
	if api.WorktreeRepo != "" {
		t.Errorf("api: worktree_repo = %q, want empty for a container with no worktree", api.WorktreeRepo)
	}

	// The worktree outlives its container now: removing the container must
	// leave the checkout's record, or a checkout on disk would have nothing
	// that names it.
	if err := s.DeleteContainer("ws", "feat"); err != nil {
		t.Fatal(err)
	}
	w, err := s.GetWorktree("ws", "feat")
	if err != nil {
		t.Fatalf("the worktree row went with its container: %v", err)
	}
	if w.Name != "feat" || w.Repo != "/src/app/.git" || w.HerdrWorkspace != "7f2a" {
		t.Errorf("migrated worktree = %+v, want the old row's values under its name", w)
	}
}
