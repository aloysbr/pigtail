-- System journal (subject "Journal système", D1): an index of pointers, one
-- row per event, transversal to subjects. Sister of subject_log, which stays
-- the subject's lifecycle and is not touched. A row never holds content: one
-- summary line and a link to the source. The producer supplies the id; the
-- primary key (scope, id) makes a re-post a no-op within a scope and never
-- tells a caller that an id exists in the other one. Additive.
CREATE TABLE IF NOT EXISTS journal (
    id         TEXT    NOT NULL CHECK (length(id) BETWEEN 1 AND 200),
    scope      TEXT    NOT NULL CHECK (length(scope) BETWEEN 1 AND 32 AND scope NOT GLOB '*[^a-z0-9-]*'),
    ts         INTEGER NOT NULL,            -- epoch ms
    kind       TEXT    NOT NULL CHECK (kind <> ''),
    subject_id TEXT,                        -- NULL: an event with no subject (Granola)
    summary    TEXT    NOT NULL CHECK (length(summary) BETWEEN 1 AND 280
                                       AND instr(summary, char(10)) = 0
                                       AND instr(summary, char(13)) = 0),
    source     TEXT    NOT NULL CHECK (source <> ''),
    PRIMARY KEY (scope, id)
);
CREATE INDEX IF NOT EXISTS idx_journal_ts         ON journal(ts);
CREATE INDEX IF NOT EXISTS idx_journal_scope_ts   ON journal(scope, ts);
CREATE INDEX IF NOT EXISTS idx_journal_subject_ts ON journal(subject_id, ts);
