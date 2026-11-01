-- Explicit link between a subject and a cmux workspace (one workspace carries
-- at most one subject; a subject may have several workspaces). Additive: an
-- existing database gains an empty table and behaves identically otherwise.
-- The title/description columns are the sidebar loop's memory (what the
-- workspace was called before pigtail renamed it, what pigtail last posed,
-- and who owns the name now).
CREATE TABLE IF NOT EXISTS subject_cmux_bindings (
    cmux_workspace_id   TEXT PRIMARY KEY,          -- cmux UUID, upper case; stable across cmux restarts
    subject_id          TEXT NOT NULL,
    source              TEXT NOT NULL CHECK (source IN ('attach','subject-work','ptnew')),
    bound_at            INTEGER NOT NULL,
    prev_title          TEXT,                      -- title before pigtail's first rename
    prev_title_custom   INTEGER NOT NULL DEFAULT 0,
    prev_description    TEXT,
    applied_title       TEXT,                      -- last title pigtail posed
    applied_description TEXT,
    title_owner         TEXT NOT NULL DEFAULT 'pigtail' CHECK (title_owner IN ('pigtail','user')),
    description_owner   TEXT NOT NULL DEFAULT 'pigtail' CHECK (description_owner IN ('pigtail','user')),
    missing_ticks       INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (subject_id) REFERENCES subjects(id)
);
CREATE INDEX IF NOT EXISTS idx_subject_cmux_bindings_subject ON subject_cmux_bindings(subject_id);
