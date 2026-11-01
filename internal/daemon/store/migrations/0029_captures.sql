-- 0029_captures.sql — A3/C2 (sub_x000000000000010): a capture is a lightweight
-- idea under a project (theme), not yet a subject. It may replace a subject
-- (from_subject_id, D2) and is closed by promotion into a subject. Scope is
-- never stored: it derives from the theme (JOIN), like subjects.
CREATE TABLE IF NOT EXISTS captures (
    id                  TEXT PRIMARY KEY,          -- cap_<16 hex>, minted like sub_
    theme_id            TEXT NOT NULL,
    text                TEXT NOT NULL,
    from_subject_id     TEXT,                      -- D2: the subject it replaces
    promoted_subject_id TEXT,                      -- set by promote
    from_prev_status    TEXT,                      -- status of from_subject_id before capture (undo)
    created_at          INTEGER NOT NULL,
    closed_at           INTEGER,                   -- set by promote
    FOREIGN KEY (theme_id) REFERENCES themes(id),
    FOREIGN KEY (from_subject_id) REFERENCES subjects(id),
    FOREIGN KEY (promoted_subject_id) REFERENCES subjects(id)
);
CREATE INDEX IF NOT EXISTS idx_captures_theme ON captures(theme_id);
