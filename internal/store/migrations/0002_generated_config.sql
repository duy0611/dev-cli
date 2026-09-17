-- The devcontainer configuration dev generated for a container, empty when the
-- project ships its own. Stored rather than written to disk so it cascades away
-- with the workspace and travels to Postgres with the rest of the schema.
--
-- A literal default rather than an expression, and NOT NULL, so the same file
-- replays against Postgres unchanged.
ALTER TABLE containers ADD COLUMN generated_config TEXT NOT NULL DEFAULT '';
