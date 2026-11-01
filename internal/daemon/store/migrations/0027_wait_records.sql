-- Enriched waits (sub_x000000000000026, D1, D2, D28, D29): a wait record
-- beside the `declared` row of session_waits, and beside the `auto` row of a
-- Claude Code dialog (permission, question: readable, never answered outside
-- the terminal). It carries a stable id, the typed content (details, JSON)
-- and its history: open → replaced | cleared | answered (answer, JSON).
--
-- D28: the wait belongs to its subject in its scope; session_id is the
-- emitting session. owner is 'subject:<scope>:<subject_id>' for a declared
-- wait of an answerable type (arbitrage, question, review, blocked — the set
-- of waittype.AnswerableTypes) bound to a subject, else 'session:<id>'; the
-- CHECK keeps that rule. At most one open record per owner and source
-- (D29-B: the last declaration on a subject replaces the previous one) and
-- at most one per session and source (the PK of session_waits). A subject's
-- wait outlives the end of its emitter (D29-A). An answer with no live
-- session on the subject is kept (message_id NULL) and goes to the subject's
-- next session (D29-C); delivered_to / delivered_at say where and when it
-- went. snoozed_until hides an open wait from the list until then (judged at
-- read). Closed records are purged after 30 days. session_waits does not
-- change. Additive.
--
-- Modified in place before any real database applied it (the latest was at
-- 0026): no catch-up migration.
CREATE TABLE IF NOT EXISTS wait_records (
    id            TEXT PRIMARY KEY,
    session_id    TEXT NOT NULL,
    source        TEXT NOT NULL DEFAULT 'declared' CHECK (source IN ('declared','auto')),
    scope         TEXT NOT NULL DEFAULT '',
    subject_id    TEXT NOT NULL DEFAULT '',
    type          TEXT NOT NULL,
    reason        TEXT NOT NULL DEFAULT '',
    details       TEXT,
    state         TEXT NOT NULL CHECK (state IN ('open','replaced','cleared','answered')),
    opened_at     INTEGER NOT NULL,
    closed_at     INTEGER,
    snoozed_until INTEGER,
    answer        TEXT,
    owner         TEXT NOT NULL,
    message_id    TEXT,
    delivered_to  TEXT,
    delivered_at  INTEGER,
    CHECK (owner = CASE
        WHEN source = 'declared' AND subject_id <> '' AND type IN ('arbitrage','question','review','blocked')
        THEN 'subject:' || scope || ':' || subject_id
        ELSE 'session:' || session_id END)
);
CREATE UNIQUE INDEX IF NOT EXISTS wait_records_one_open ON wait_records(owner, source) WHERE state = 'open';
CREATE UNIQUE INDEX IF NOT EXISTS wait_records_session ON wait_records(session_id, source) WHERE state = 'open';
CREATE INDEX IF NOT EXISTS wait_records_state ON wait_records(state, opened_at);
CREATE INDEX IF NOT EXISTS wait_records_closed ON wait_records(closed_at) WHERE state <> 'open';
CREATE INDEX IF NOT EXISTS wait_records_kept ON wait_records(subject_id) WHERE state = 'answered' AND message_id IS NULL;
CREATE INDEX IF NOT EXISTS wait_records_delivered ON wait_records(delivered_to) WHERE state = 'answered' AND message_id IS NOT NULL;
