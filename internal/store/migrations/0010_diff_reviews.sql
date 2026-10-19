-- A container's review state: the draft being written and the last round sent.
--
-- One row per container, written on first use. draft, sent and snapshot are
-- JSON produced by internal/review and kept opaque here. sent_at is '' before
-- the first send rather than a sentinel date: the application writes the time,
-- and an empty value is what a zero time round-trips as.
--
-- Literal defaults, NOT NULL, and no SQLite-only syntax, so the file replays
-- against Postgres.
CREATE TABLE diff_reviews (
  workspace_name TEXT NOT NULL DEFAULT '',
  container_name TEXT NOT NULL DEFAULT '',
  draft          TEXT NOT NULL DEFAULT '',
  sent           TEXT NOT NULL DEFAULT '',
  snapshot       TEXT NOT NULL DEFAULT '',
  sent_at        TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (workspace_name, container_name),
  FOREIGN KEY (workspace_name, container_name)
    REFERENCES containers(workspace_name, name) ON DELETE CASCADE
);
