-- N-ary scopes (subject « Scopes N-aires », P1/D3/D16). The scope set now lives
-- in the scope table (internal/scope, <config dir>/scopes.yaml), not in SQL:
-- the closed CHECKs of 0010 (themes) and 0014 (journal) become a shape check —
-- a lowercase identifier, never empty. SQLite cannot alter a
-- CHECK: both tables are rebuilt (create new, copy, drop, rename). The runner
-- applies this file in one transaction with foreign keys off (store.go
-- applyOne), so subjects.theme_id keeps pointing at "themes" by name.

CREATE TABLE themes_0015 (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    kind            TEXT NOT NULL,
    vault_link      TEXT NOT NULL,
    code_project_id TEXT,
    archived        INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    scope           TEXT NOT NULL CHECK (length(scope) BETWEEN 1 AND 32
                                         AND scope NOT GLOB '*[^a-z0-9-]*'),
    FOREIGN KEY (code_project_id) REFERENCES projects(id)
);
INSERT INTO themes_0015 (id, name, kind, vault_link, code_project_id, archived, created_at, scope)
    SELECT id, name, kind, vault_link, code_project_id, archived, created_at,
           scope
    FROM themes;
DROP TABLE themes;
ALTER TABLE themes_0015 RENAME TO themes;
CREATE INDEX IF NOT EXISTS idx_themes_scope ON themes(scope);

CREATE TABLE journal_0015 (
    id         TEXT    NOT NULL CHECK (length(id) BETWEEN 1 AND 200),
    scope      TEXT    NOT NULL CHECK (length(scope) BETWEEN 1 AND 32
                                       AND scope NOT GLOB '*[^a-z0-9-]*'),
    ts         INTEGER NOT NULL,
    kind       TEXT    NOT NULL CHECK (kind <> ''),
    subject_id TEXT,
    summary    TEXT    NOT NULL CHECK (length(summary) BETWEEN 1 AND 280
                                       AND instr(summary, char(10)) = 0
                                       AND instr(summary, char(13)) = 0),
    source     TEXT    NOT NULL CHECK (source <> ''),
    PRIMARY KEY (scope, id)
);
INSERT INTO journal_0015 (id, scope, ts, kind, subject_id, summary, source)
    SELECT id, scope, ts, kind, subject_id, summary, source
    FROM journal;
DROP TABLE journal;
ALTER TABLE journal_0015 RENAME TO journal;
CREATE INDEX IF NOT EXISTS idx_journal_ts         ON journal(ts);
CREATE INDEX IF NOT EXISTS idx_journal_scope_ts   ON journal(scope, ts);
CREATE INDEX IF NOT EXISTS idx_journal_subject_ts ON journal(subject_id, ts);

