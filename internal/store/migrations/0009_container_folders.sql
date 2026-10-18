-- The folders a container has beyond its primary, and where an isolated
-- primary's volume is mounted.
--
-- The primary stays in containers.source, so every reader of it is unchanged.
-- container_path is stored rather than derived from host_path, so renaming the
-- folder on the host cannot move it inside the container. worktree_repo is a
-- guarded worktree folder's common directory, read from git at create and fixed
-- then, as containers.worktree_repo is: the checkout's .git file is writable
-- from inside, and whatever it names is mounted.
--
-- workspace_path is set only for isolated rows: every other mode's primary is
-- placed by the devcontainer CLI. The rows that exist were mounted at
-- /workspaces/<container name>, and keep it, so fetch, remove and rebuild find
-- the volume where it already is.
--
-- Literal defaults, NOT NULL, and no SQLite-only syntax, so the file replays
-- against Postgres.
CREATE TABLE container_folders (
  workspace_name TEXT NOT NULL,
  container_name TEXT NOT NULL,
  host_path      TEXT NOT NULL,
  container_path TEXT NOT NULL,
  worktree_repo  TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (workspace_name, container_name, host_path),
  FOREIGN KEY (workspace_name, container_name)
    REFERENCES containers(workspace_name, name) ON DELETE CASCADE
);

ALTER TABLE containers ADD COLUMN workspace_path TEXT NOT NULL DEFAULT '';
UPDATE containers SET workspace_path = '/workspaces/' || name
  WHERE mode = 'isolated';
