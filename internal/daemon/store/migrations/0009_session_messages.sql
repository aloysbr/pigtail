-- Adds the session-message relay: peer_sessions (the derived registry of
-- Claude Code sessions a message can be addressed to) and messages (the relay
-- itself, which is also its audit trail). All additive; pre-existing databases
-- gain two empty tables and behave identically otherwise.

CREATE TABLE IF NOT EXISTS peer_sessions (
    session_id        TEXT PRIMARY KEY,
    config_dir        TEXT NOT NULL DEFAULT '',
    cell_id           TEXT NOT NULL DEFAULT '',
    scope             TEXT NOT NULL DEFAULT '',   -- scope name, from the cell table
    cwd               TEXT NOT NULL DEFAULT '',
    name              TEXT NOT NULL DEFAULT '',   -- Claude's native session name
    status            TEXT NOT NULL DEFAULT '',   -- Claude's native status
    pid               INTEGER NOT NULL DEFAULT 0, -- the claude process
    cmux_workspace_id TEXT NOT NULL DEFAULT '',
    cmux_surface_id   TEXT NOT NULL DEFAULT '',
    cmux_title        TEXT NOT NULL DEFAULT '',
    workspace_id      TEXT NOT NULL DEFAULT '',
    workspace_name    TEXT NOT NULL DEFAULT '',
    subject_id        TEXT NOT NULL DEFAULT '',
    lifecycle         TEXT NOT NULL DEFAULT '',
    source            TEXT NOT NULL DEFAULT '',   -- snapshot|hook|self
    updated_at        INTEGER NOT NULL,
    ended_at          INTEGER
);

CREATE TABLE IF NOT EXISTS messages (
    id            TEXT PRIMARY KEY,               -- msg_…
    thread_id     TEXT NOT NULL,                  -- thr_…
    in_reply_to   TEXT,
    from_session  TEXT NOT NULL,
    to_session    TEXT NOT NULL,
    from_label    TEXT NOT NULL DEFAULT '',
    to_label      TEXT NOT NULL DEFAULT '',
    subject_id    TEXT,
    kind          TEXT NOT NULL CHECK (kind IN ('info','request','reply','ack')),
    body          TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    delivered_at  INTEGER,
    delivered_via TEXT,
    acked_at      INTEGER,
    state         TEXT NOT NULL DEFAULT 'pending' -- pending|delivered|acked|expired
);
CREATE INDEX IF NOT EXISTS idx_messages_to_state ON messages(to_session, state);
CREATE INDEX IF NOT EXISTS idx_messages_thread   ON messages(thread_id);
CREATE INDEX IF NOT EXISTS idx_messages_from     ON messages(from_session, to_session, created_at);
