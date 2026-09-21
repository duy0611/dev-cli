package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/duy0611/dev-cli/internal/model"
)

// seedRaw builds a database at an older schema and records the migrations that
// produced it as already applied, so Open runs only the ones after them.
//
// Written out rather than replayed from the embedded files: the point is to
// start from the schema as it was, and the files on disk are the schema as it
// is now.
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

// TestMigrationDropsGPGForward covers upgrading a database created before
// gpg_forward was dropped.
//
// The column was NOT NULL with a default, so an INSERT that no longer names it
// still works — which means a broken migration would not fail loudly. It would
// fail on the SELECT instead, on the next command the operator ran.
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

// TestMigrationAddsPersistState covers upgrading a database created before
// containers could persist their agents' state.
//
// The column is NOT NULL with a default, so an INSERT that does not name it
// still works and a broken migration would not fail loudly — it would fail on
// the SELECT, on the next command the operator ran.
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
