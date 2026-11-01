-- 0030_subject_outputs.sql — sub_x000000000000017: the registry of the
-- artifacts produced for a subject (capture = screenshot, mockup, report,
-- server, PR, file under _outputs/), registered when produced. Kind
-- 'capture' is a screenshot, unrelated to the captures table (backlog
-- ideas). No scope column: it derives from the subject, like captures. A
-- purge sets purged_at and keeps the row. Times are epoch ms.
CREATE TABLE IF NOT EXISTS subject_outputs (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,  -- read cursor (since, exclusive)
    id         TEXT    NOT NULL UNIQUE,            -- out_ + 12 hex of sha256(subject_id \n key)
    subject_id TEXT    NOT NULL,                   -- logical FK to subjects(id)
    session_id TEXT,                               -- producing Claude session
    kind       TEXT    NOT NULL CHECK (kind IN ('capture', 'html', 'pdf', 'mockup', 'report',
                                                'server', 'pr', 'clip', 'link', 'file')),
    path       TEXT,                               -- absolute
    url        TEXT,                               -- with a scheme
    key        TEXT    NOT NULL,                   -- path, else url
    title      TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 60),
    ts         INTEGER NOT NULL,                   -- epoch ms, production time
    step       TEXT,                               -- workflow step
    meta       TEXT,                               -- JSON object, <= 4096 bytes
    purged_at  INTEGER,                            -- epoch ms, set by the purge
    created_at INTEGER NOT NULL,                   -- epoch ms
    CHECK (path IS NOT NULL OR url IS NOT NULL),
    UNIQUE (subject_id, key)
);
CREATE INDEX IF NOT EXISTS idx_subject_outputs_subject_seq ON subject_outputs(subject_id, seq);
