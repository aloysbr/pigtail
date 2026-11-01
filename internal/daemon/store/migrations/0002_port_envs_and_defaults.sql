-- Adds two optional columns to the projects row so the daemon can persist
-- pigtail.yaml fields introduced in the PortSpec / Defaults work:
--   port_envs  — JSON: { kind → env var name } (only kinds where the user
--                overrode the default; missing kinds fall back to
--                DefaultPortEnv(kind) at read time)
--   defaults   — JSON: { agent, base_ref, branch_prefix } or NULL
--
-- Both are nullable. Pre-existing rows (port_envs IS NULL, defaults IS NULL)
-- decode to nil maps / nil struct — behavior is identical to before the
-- migration.
ALTER TABLE projects ADD COLUMN port_envs TEXT;
ALTER TABLE projects ADD COLUMN defaults  TEXT;
