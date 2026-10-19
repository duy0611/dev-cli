-- Whether a watchdog inside the container stops it once nobody is connected.
--
-- idle_stop is fixed at create, like git_guard: a rebuild must not turn the
-- watchdog on or off. The default speaks for rows that already exist — they
-- get the watchdog the next time they start. create writes its own value for
-- every new row.
--
-- Literal default and NOT NULL, so the file replays against Postgres.
ALTER TABLE containers ADD COLUMN idle_stop INTEGER NOT NULL DEFAULT 1;
