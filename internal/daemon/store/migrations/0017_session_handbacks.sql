-- Turn ends by skill (subject sub_x000000000000032, D11, D12): the last skill
-- a session invoked, and one row per turn (Stop / StopFailure hooks), keyed by
-- the transcript's promptId. Counted: neither failed nor declared. Additive.
CREATE TABLE IF NOT EXISTS session_last_skill (
    session_id TEXT PRIMARY KEY,
    skill      TEXT NOT NULL,
    at         INTEGER NOT NULL             -- epoch ms
);
CREATE TABLE IF NOT EXISTS session_last_declared (
    session_id TEXT PRIMARY KEY,
    at         INTEGER NOT NULL             -- epoch ms of the session's last `session wait` (declared); never cleared by the loop
);
CREATE TABLE IF NOT EXISTS session_turn_ends (
    session_id TEXT NOT NULL,
    turn_id    TEXT NOT NULL,               -- the transcript's promptId
    turn_start INTEGER NOT NULL,            -- epoch ms
    at         INTEGER NOT NULL,            -- epoch ms: the first Stop/StopFailure seen
    skill      TEXT NOT NULL DEFAULT '',    -- the session's last skill then; '' = none
    scope      TEXT NOT NULL DEFAULT '',    -- scope name, for scoped reads
    declared   INTEGER NOT NULL DEFAULT 0,  -- session_last_declared.at >= turn_start, at Stop
    failed     INTEGER NOT NULL DEFAULT 0,  -- a StopFailure for this turn
    PRIMARY KEY (session_id, turn_id)
);
CREATE INDEX IF NOT EXISTS session_turn_ends_at ON session_turn_ends(at);
