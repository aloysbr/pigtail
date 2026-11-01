-- Adds two optional columns to workspaces so the watchdog can persist the
-- last runaway dev-server kill for surfacing in `pigtail list` and the TUI:
--   last_runaway_at     — unix millis of the most recent runaway kill
--   last_runaway_reason — short machine-readable reason (e.g. "runaway_cpu")
--
-- Both are nullable. Pre-existing rows (NULL, NULL) decode to a nil time
-- pointer / empty string — behavior is identical to before the migration.
ALTER TABLE workspaces ADD COLUMN last_runaway_at     INTEGER;
ALTER TABLE workspaces ADD COLUMN last_runaway_reason TEXT;
