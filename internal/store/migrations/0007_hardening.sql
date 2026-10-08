-- The hardening guards' record on each container: whether host git is shielded
-- from what the container writes under .git, whether the container may ask its
-- engine for access to the host, and the digest of the configuration it was
-- last built from.
--
-- git_guard and allow_privileged are fixed at create: what a container is
-- mounted on and what it may ask of the host must not change under a rebuild
-- (invariant 10's reason). The defaults speak for rows that already exist —
-- created unguarded, so they keep behaving exactly as they did until they are
-- recreated. create writes its own values for every new row.
--
-- config_digest '' means "not yet recorded": every generated container, whose
-- configuration is dev's own, and existing rows until their next rebuild.
-- config_digest_fields is a hash per top-level field and per referenced file of
-- that same configuration, so a rebuild that finds the digest changed can say
-- which fields did. Hashes only: dev keeps no copy of a document it does not own.
--
-- Literal defaults and NOT NULL, so the file replays against Postgres.
ALTER TABLE containers ADD COLUMN git_guard INTEGER NOT NULL DEFAULT 0;
ALTER TABLE containers ADD COLUMN allow_privileged INTEGER NOT NULL DEFAULT 1;
ALTER TABLE containers ADD COLUMN config_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE containers ADD COLUMN config_digest_fields TEXT NOT NULL DEFAULT '';
