-- Idle pause: pigtaild stopped the claude
-- process of an idle session without closing it. state: stopping (SIGTERM sent,
-- exit not yet seen) | paused. cmux_workspace_id: the cmux workspace UUID
-- (upper case) the sidebar pill is drawn on. sid_path: the launcher's
-- <key>.sid pigtail wrote ('' = no launcher). entered_standby: the pause put
-- the session in Standby (undone on failure). The row goes when the same session registers alive again, or another
-- session takes its surface. signaled_at: set right before the SIGTERM
-- (NULL = nothing was sent). Additive.
CREATE TABLE IF NOT EXISTS session_pause (
    session_id         TEXT PRIMARY KEY,
    pid                INTEGER NOT NULL,
    surface_id         TEXT NOT NULL,
    cmux_workspace_id  TEXT NOT NULL DEFAULT '',
    sid_path           TEXT NOT NULL DEFAULT '',
    state              TEXT NOT NULL CHECK (state IN ('stopping', 'paused')),
    entered_standby    INTEGER NOT NULL DEFAULT 0,
    since              INTEGER NOT NULL,
    signaled_at        INTEGER,
    paused_at          INTEGER
);
