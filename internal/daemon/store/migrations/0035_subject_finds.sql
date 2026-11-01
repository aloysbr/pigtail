-- One row per `pigtail subject find`
-- search — the query in clear (T3: this database is local only), the readable
-- candidates it returned ([{subject_id, score}] in rank order, never another
-- scope's), the pane's cmux workspace, and the subject finally picked (attach
-- --find, or subject pick). The journal holds one pointer line per search and
-- per pick, written in the same transaction. Additive.
CREATE TABLE IF NOT EXISTS subject_finds (
    id                TEXT    PRIMARY KEY,                    -- sf_ + 16 hex
    scope             TEXT    NOT NULL CHECK (length(scope) BETWEEN 1 AND 32
                                              AND scope NOT GLOB '*[^a-z0-9-]*'),
    ts                INTEGER NOT NULL,                       -- epoch ms
    query             TEXT    NOT NULL CHECK (length(query) BETWEEN 1 AND 500
                                              AND instr(query, char(10)) = 0
                                              AND instr(query, char(13)) = 0),
    candidates        TEXT    NOT NULL DEFAULT '[]',          -- JSON array
    cmux_workspace_id TEXT    NOT NULL DEFAULT '',
    picked_subject_id TEXT,
    picked_at         INTEGER,                                -- epoch ms
    CHECK ((picked_subject_id IS NULL) = (picked_at IS NULL))
);
CREATE INDEX IF NOT EXISTS idx_subject_finds_ws ON subject_finds(scope, cmux_workspace_id, ts);
