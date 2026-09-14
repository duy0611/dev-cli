-- Portable SQL only: no AUTOINCREMENT, no datetime('now') defaults, no SQLite
-- type affinities that Postgres does not share. The cloud provider will replay
-- these same files against Postgres.

CREATE TABLE providers (
  name        TEXT PRIMARY KEY,
  kind        TEXT NOT NULL,
  config      TEXT NOT NULL DEFAULT '{}',
  created_at  TEXT NOT NULL
);

CREATE TABLE workspaces (
  name           TEXT PRIMARY KEY,
  provider_name  TEXT NOT NULL REFERENCES providers(name),
  ssh_forward    INTEGER NOT NULL DEFAULT 0,
  gpg_forward    INTEGER NOT NULL DEFAULT 0,
  created_at     TEXT NOT NULL
);

-- One row per setting rather than a JSON blob, so `workspace set` and
-- `workspace unset` each touch one row and a later UI can list them without
-- parsing anything.
CREATE TABLE workspace_settings (
  workspace_name TEXT NOT NULL REFERENCES workspaces(name) ON DELETE CASCADE,
  key            TEXT NOT NULL,
  spec           TEXT NOT NULL,
  PRIMARY KEY (workspace_name, key)
);

-- Name is unique per workspace, not globally: that is what makes a workspace a
-- real grouping rather than a label.
CREATE TABLE containers (
  name           TEXT NOT NULL,
  workspace_name TEXT NOT NULL REFERENCES workspaces(name) ON DELETE CASCADE,
  source_kind    TEXT NOT NULL,
  source         TEXT NOT NULL,
  config_path    TEXT NOT NULL,
  created_at     TEXT NOT NULL,
  PRIMARY KEY (workspace_name, name)
);

CREATE TABLE app_state (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
