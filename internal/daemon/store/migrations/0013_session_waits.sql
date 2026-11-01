-- What a Claude session waits for (spec "Sidebar pigtail" §4.8, PR 6): at
-- most one automatic wait (typed by a Claude Code hook) and one declared wait
-- (`pigtail session wait`) per session. Persisted, not in memory: a declared
-- decision may wait for hours, across a daemon restart (post-merge reinstall),
-- and its pill must survive it. Cleared by the next prompt, the next busy
-- turn, SessionEnd, or the death of the session. Additive.
CREATE TABLE IF NOT EXISTS session_waits (
    session_id TEXT NOT NULL,
    source     TEXT NOT NULL CHECK (source IN ('auto', 'declared')),
    type       TEXT NOT NULL,
    reason     TEXT NOT NULL DEFAULT '',
    since      INTEGER NOT NULL,           -- epoch ms
    PRIMARY KEY (session_id, source)
);
