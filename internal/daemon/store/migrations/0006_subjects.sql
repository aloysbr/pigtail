-- Adds the durable subject layer: themes (handles onto vault Projects/Areas),
-- subjects (the prioritisable unit), subject_phases (links a phase to a
-- workspace), and subject_log (feeds reporting). All additive; pre-existing
-- databases gain four empty tables and behave identically otherwise.

CREATE TABLE IF NOT EXISTS themes (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    kind            TEXT NOT NULL,           -- project|area
    vault_link      TEXT NOT NULL,
    code_project_id TEXT,
    archived        INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    FOREIGN KEY (code_project_id) REFERENCES projects(id)
);

CREATE TABLE IF NOT EXISTS subjects (
    id              TEXT PRIMARY KEY,
    theme_id        TEXT NOT NULL,
    title           TEXT NOT NULL,
    slug            TEXT NOT NULL,
    status          TEXT NOT NULL,
    impact          INTEGER,
    effort          INTEGER,
    urgency         INTEGER,
    rank            INTEGER,
    deps            TEXT,                    -- JSON array of subject ids
    vault_path      TEXT,
    code_project_id TEXT,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    FOREIGN KEY (theme_id) REFERENCES themes(id),
    FOREIGN KEY (code_project_id) REFERENCES projects(id)
);
CREATE INDEX IF NOT EXISTS idx_subjects_theme  ON subjects(theme_id);
CREATE INDEX IF NOT EXISTS idx_subjects_status ON subjects(status);

CREATE TABLE IF NOT EXISTS subject_phases (
    id           TEXT PRIMARY KEY,
    subject_id   TEXT NOT NULL,
    kind         TEXT NOT NULL,             -- research|spec|implement|custom
    state        TEXT NOT NULL,             -- pending|dispatched|active|done
    workspace_id TEXT,
    created_at   INTEGER NOT NULL,
    FOREIGN KEY (subject_id)   REFERENCES subjects(id),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id)
);
CREATE INDEX IF NOT EXISTS idx_subject_phases_subject ON subject_phases(subject_id);

CREATE TABLE IF NOT EXISTS subject_log (
    id         TEXT PRIMARY KEY,
    subject_id TEXT NOT NULL,
    ts         INTEGER NOT NULL,
    kind       TEXT NOT NULL,               -- idea|note|progress|status|phase|delivery|handoff
    text       TEXT NOT NULL,
    FOREIGN KEY (subject_id) REFERENCES subjects(id)
);
CREATE INDEX IF NOT EXISTS idx_subject_log_subject ON subject_log(subject_id);
CREATE INDEX IF NOT EXISTS idx_subject_log_ts      ON subject_log(ts);
