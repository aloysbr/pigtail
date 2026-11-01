-- Adds an optional `intent` column to agent_sessions so each session records
-- the prompt it was spawned with. This lets the triage TUI show per-agent
-- intent excerpts when several agents share one workspace (multi-agent
-- shared-worktree model).
--
-- Nullable. Pre-existing rows (NULL) decode to an empty string — behavior is
-- identical to before the migration.
ALTER TABLE agent_sessions ADD COLUMN intent TEXT;
