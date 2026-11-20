-- Saved model tiers for agent teams: which model a tier name (small, medium, large)
-- stands for, per agent tool. Plain strings; the team package validates them.
--
-- No SQLite-only syntax, so the file replays against Postgres.
CREATE TABLE team_tiers (
    tool  TEXT NOT NULL,
    tier  TEXT NOT NULL,
    model TEXT NOT NULL,
    PRIMARY KEY (tool, tier)
);
