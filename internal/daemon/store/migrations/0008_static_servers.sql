-- Adds static_servers: named static HTTP servers that serve an arbitrary
-- directory of files, NOT tied to any git worktree. The persisted row holds
-- only metadata; the running state is derived live (no PID/running column).
-- `command` is reserved for a future override and stays empty by default
-- (empty = built-in Go file server). All additive; pre-existing databases gain
-- one empty table and behave identically otherwise.

CREATE TABLE IF NOT EXISTS static_servers (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    dir        TEXT NOT NULL,        -- absolute directory to serve
    port       INTEGER NOT NULL,
    command    TEXT,                 -- empty = built-in Go file server (default); reserved for future override
    created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_static_servers_name ON static_servers(name);
