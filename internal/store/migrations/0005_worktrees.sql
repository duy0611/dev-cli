-- One git worktree checkout, and the container running on it.
--
-- A table rather than columns on containers: most containers are not
-- worktree-backed, and four mostly-empty columns on the table every command
-- reads is the wrong trade for one join in one command.
--
-- The cascade is what makes `container remove` safe to leave alone — it cannot
-- leave a row describing a container that is gone. It needs
-- PRAGMA foreign_keys = ON, which the store sets on every connection.
--
-- repo is the git *common directory*, not the repository root. They differ for
-- a bare repository, and this is the directory the checkout's .git file points
-- into, so it is the one that gets bind-mounted.
--
-- path duplicates containers.source for the same row today. Deliberate: source
-- is what the container is mounted from, path is what git was told to create,
-- and keeping them apart means a container whose source later diverges from its
-- checkout cannot corrupt the removal.
--
-- An empty herdr_workspace rather than a nullable column, matching how the rest
-- of this schema spells "nothing to say" and keeping the scan free of
-- sql.NullString. Empty means Herdr was absent or declined.
--
-- Portable SQL: literal default, TEXT timestamp written by the application.
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
