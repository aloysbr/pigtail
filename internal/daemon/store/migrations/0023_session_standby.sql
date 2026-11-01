-- /standby (sub_x000000000000041, D4, D5): the session wrote its handoff and
-- waits in the « Standby » folder until the user types to it. compact_sent_at:
-- when pigtail last typed /compact in its pane (NULL = not yet);
-- compact_status_at: the native statusUpdatedAt read right before that send;
-- compact_pid: the claude pid it was sent to (proof read on that pid only);
-- the row outlives SessionEnd (D24: a relaunch resumes the same id);
-- compacted_at: the session left idle after it (the proof); compact_failed_at:
-- given up after compact_tries sends without proof; missing_ticks: successful
-- cmux listings in a row without the session's workspace (purge at 2,
-- Tâche 16, like subject_cmux_bindings). Additive.
CREATE TABLE IF NOT EXISTS session_standby (
    session_id        TEXT PRIMARY KEY,
    since             INTEGER NOT NULL,
    compact_sent_at   INTEGER,
    compact_status_at INTEGER NOT NULL DEFAULT 0,
    compact_pid       INTEGER NOT NULL DEFAULT 0,
    compact_tries     INTEGER NOT NULL DEFAULT 0,
    compacted_at      INTEGER,
    compact_failed_at INTEGER,
    missing_ticks     INTEGER NOT NULL DEFAULT 0
);
