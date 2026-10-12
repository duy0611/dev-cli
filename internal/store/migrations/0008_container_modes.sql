-- How much of the operator's checkout a container reaches, and the worktree
-- table freed from its container.
--
-- mode is fixed at create, for git_guard's reason: what a container is mounted
-- on must not change under a rebuild. Every row that exists predates modes, so
-- the default speaks for them: 'legacy', created under the single-file git
-- guard and still behaving as it did. A folderless row has no checkout for a
-- mode to describe, so it is 'none'. create writes its own value for every new
-- row.
--
-- worktree_repo moves the worktree's common directory onto the container. It
-- used to be looked up through the worktrees row that named the container; now
-- that `container create` runs on any folder, it is read from git at create
-- and has to be stored where the container can find it without that link.
--
-- The worktrees table is rebuilt rather than altered: it loses the foreign key
-- to containers, since a checkout outlives any container on it, and dropping a
-- foreign key is not portable. It keeps one to workspaces, as a backstop for
-- `workspace remove`, which refuses while worktrees exist.
--
-- Literal defaults, NOT NULL, and no SQLite-only syntax, so the file replays
-- against Postgres.
ALTER TABLE containers ADD COLUMN mode TEXT NOT NULL DEFAULT 'legacy';
UPDATE containers SET mode = 'none' WHERE source_kind = 'none';

ALTER TABLE containers ADD COLUMN worktree_repo TEXT NOT NULL DEFAULT '';
UPDATE containers SET worktree_repo =
  (SELECT repo FROM worktrees
    WHERE worktrees.workspace_name = containers.workspace_name
      AND worktrees.container_name = containers.name)
  WHERE EXISTS (SELECT 1 FROM worktrees
    WHERE worktrees.workspace_name = containers.workspace_name
      AND worktrees.container_name = containers.name);

CREATE TABLE worktrees_new (
  workspace_name  TEXT NOT NULL REFERENCES workspaces(name) ON DELETE CASCADE,
  name            TEXT NOT NULL,
  repo            TEXT NOT NULL,
  branch          TEXT NOT NULL,
  path            TEXT NOT NULL,
  herdr_workspace TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  PRIMARY KEY (workspace_name, name)
);
INSERT INTO worktrees_new
  (workspace_name, name, repo, branch, path, herdr_workspace, created_at)
  SELECT workspace_name, container_name, repo, branch, path, herdr_workspace, created_at
  FROM worktrees;
DROP TABLE worktrees;
ALTER TABLE worktrees_new RENAME TO worktrees;
