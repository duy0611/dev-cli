-- Which agents.yaml a container applies, and whether applying it is still owed.
--
-- agent_config stores the operator's choice, never the file's contents: ''
-- means the project's own .devcontainer/agents.yaml when it has one, 'none'
-- means create was told --no-agent-config, anything else is the physical path
-- --agent-config named. Stored because rebuild re-reads the file and has to
-- read the same one without the flag being repeated.
--
-- agent_config_pending is work dev owes the container, not the container's
-- state: set when there is a file to apply and cleared once an apply finishes,
-- so `create --no-start` hands the job to the first start and a failed apply is
-- retried by the next one.
--
-- '' and 0 are the right answers for rows that already exist: nothing was ever
-- applied to them, and nothing will be until they are rebuilt.
--
-- Literal defaults and NOT NULL, so the file replays against Postgres.
ALTER TABLE containers ADD COLUMN agent_config TEXT NOT NULL DEFAULT '';
ALTER TABLE containers ADD COLUMN agent_config_pending INTEGER NOT NULL DEFAULT 0;
