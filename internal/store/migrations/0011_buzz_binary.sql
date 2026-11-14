-- Host files holding the buzz CLI, one per container architecture, set on the
-- workspace and bind-mounted into its containers when they are created or
-- rebuilt. Absolute host paths, resolved when set; empty means none.
--
-- Literal defaults, NOT NULL, and no SQLite-only syntax, so the file replays
-- against Postgres.
ALTER TABLE workspaces ADD COLUMN buzz_arm64 TEXT NOT NULL DEFAULT '';
ALTER TABLE workspaces ADD COLUMN buzz_amd64 TEXT NOT NULL DEFAULT '';
