-- Subject status vocabulary (D1, D4, D8, D12): the eight maturity statuses
-- inbox, understanding, designing, building, done, parked, dropped, captured,
-- enforced by a CHECK. Old words map by the D1 table: triaged → inbox,
-- researching → understanding, spec → designing, review / in_progress /
-- active → building, blocked / paused → parked; any other value is kept as
-- is, so a value outside the sixteen known ones fails the CHECK and the whole
-- migration is undone (never a guessed value; checked before install).
--
-- SQLite cannot alter a CHECK: the table is rebuilt (create new, copy, drop,
-- rename) in one transaction with foreign keys off (store.go applyOne), like
-- 0015 and 0019; subject_log, subject_phases, captures, subject_cmux_bindings
-- and subject_outputs keep pointing at "subjects" by name. Columns = the live
-- schema (0006 + 0007 + 0008 + 0011).

-- 1. One status log line per subject whose status changes (D12).
INSERT INTO subject_log (id, subject_id, ts, kind, text)
    SELECT 'log_' || lower(hex(randomblob(8))), id,
           CAST(strftime('%s', 'now') AS INTEGER) * 1000, 'status',
           status || ' → ' || (CASE status
               WHEN 'triaged'     THEN 'inbox'
               WHEN 'researching' THEN 'understanding'
               WHEN 'spec'        THEN 'designing'
               WHEN 'review'      THEN 'building'
               WHEN 'in_progress' THEN 'building'
               WHEN 'active'      THEN 'building'
               WHEN 'blocked'     THEN 'parked'
               WHEN 'paused'      THEN 'parked'
               ELSE status END) || ' (migration)'
    FROM subjects
    WHERE status IN ('triaged', 'researching', 'spec', 'review', 'in_progress', 'active', 'blocked', 'paused');

-- 2. The rebuilt table with its CHECK, filled through the D1 table.
CREATE TABLE subjects_0031 (
    id              TEXT PRIMARY KEY,
    theme_id        TEXT NOT NULL,
    title           TEXT NOT NULL,
    slug            TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('inbox', 'understanding', 'designing', 'building', 'done', 'parked', 'dropped', 'captured')),
    impact          INTEGER,
    effort          INTEGER,
    urgency         INTEGER,
    rank            INTEGER,
    deps            TEXT,                    -- JSON array of subject ids
    vault_path      TEXT,
    code_project_id TEXT,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    source          TEXT NOT NULL DEFAULT 'local', -- local|board
    short_id        TEXT,
    board_id        TEXT,
    section         TEXT,
    board_card      TEXT,
    short_title     TEXT,
    FOREIGN KEY (theme_id) REFERENCES themes(id),
    FOREIGN KEY (code_project_id) REFERENCES projects(id)
);
INSERT INTO subjects_0031 (id, theme_id, title, slug, status, impact, effort, urgency, rank, deps,
                           vault_path, code_project_id, created_at, updated_at, source, short_id,
                           board_id, section, board_card, short_title)
    SELECT id, theme_id, title, slug,
           CASE status
               WHEN 'triaged'     THEN 'inbox'
               WHEN 'researching' THEN 'understanding'
               WHEN 'spec'        THEN 'designing'
               WHEN 'review'      THEN 'building'
               WHEN 'in_progress' THEN 'building'
               WHEN 'active'      THEN 'building'
               WHEN 'blocked'     THEN 'parked'
               WHEN 'paused'      THEN 'parked'
               ELSE status END,
           impact, effort, urgency, rank, deps, vault_path, code_project_id, created_at, updated_at,
           source, short_id, board_id, section, board_card, short_title
    FROM subjects;
DROP TABLE subjects;
ALTER TABLE subjects_0031 RENAME TO subjects;
CREATE INDEX idx_subjects_theme  ON subjects(theme_id);
CREATE INDEX idx_subjects_status ON subjects(status);

-- 3. A capture's restore must not write back a word the CHECK now refuses.
UPDATE captures SET from_prev_status = CASE from_prev_status
        WHEN 'triaged'     THEN 'inbox'
        WHEN 'researching' THEN 'understanding'
        WHEN 'spec'        THEN 'designing'
        WHEN 'review'      THEN 'building'
        WHEN 'in_progress' THEN 'building'
        WHEN 'active'      THEN 'building'
        WHEN 'blocked'     THEN 'parked'
        WHEN 'paused'      THEN 'parked'
        ELSE from_prev_status END
    WHERE from_prev_status IN ('triaged', 'researching', 'spec', 'review', 'in_progress', 'active', 'blocked', 'paused');
