-- Message kind 'notice' (subject « msg notice cross-scope », D1): a one-way
-- service notice, the only kind that may cross scopes. SQLite cannot alter a
-- CHECK: the table is rebuilt (create new, copy, drop, rename), in one
-- transaction with foreign keys off (store.go applyOne), like 0015.

CREATE TABLE messages_0018 (
    id            TEXT PRIMARY KEY,
    thread_id     TEXT NOT NULL,
    in_reply_to   TEXT,
    from_session  TEXT NOT NULL,
    to_session    TEXT NOT NULL,
    from_label    TEXT NOT NULL DEFAULT '',
    to_label      TEXT NOT NULL DEFAULT '',
    subject_id    TEXT,
    kind          TEXT NOT NULL CHECK (kind IN ('info','request','reply','ack','notice')),
    body          TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    delivered_at  INTEGER,
    delivered_via TEXT,
    acked_at      INTEGER,
    state         TEXT NOT NULL DEFAULT 'pending'
);
INSERT INTO messages_0018 (id, thread_id, in_reply_to, from_session, to_session, from_label, to_label,
                           subject_id, kind, body, created_at, expires_at, delivered_at, delivered_via, acked_at, state)
    SELECT id, thread_id, in_reply_to, from_session, to_session, from_label, to_label,
           subject_id, kind, body, created_at, expires_at, delivered_at, delivered_via, acked_at, state
    FROM messages;
DROP TABLE messages;
ALTER TABLE messages_0018 RENAME TO messages;
CREATE INDEX IF NOT EXISTS idx_messages_to_state ON messages(to_session, state);
CREATE INDEX IF NOT EXISTS idx_messages_thread   ON messages(thread_id);
CREATE INDEX IF NOT EXISTS idx_messages_from     ON messages(from_session, to_session, created_at);
