-- 0025 (sub_x000000000000007, D1–D3): temporary focus folders, an overlay on
-- themes.space. A focus counts while ended_at IS NULL AND (ends_at IS NULL OR
-- ends_at > now), judged at read. Additive.
CREATE TABLE IF NOT EXISTS focus_spaces (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 64
                                    AND instr(name, char(10)) = 0 AND instr(name, char(13)) = 0),
    scope      TEXT NOT NULL CHECK (length(scope) BETWEEN 1 AND 32
                                    AND scope NOT GLOB '*[^a-z0-9-]*'),
    created_at INTEGER NOT NULL,
    ends_at    INTEGER,
    ended_at   INTEGER,
    end_reason TEXT NOT NULL DEFAULT '' CHECK (end_reason IN ('', 'deadline', 'manual'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_focus_open_name ON focus_spaces(name) WHERE ended_at IS NULL;
CREATE TABLE IF NOT EXISTS focus_members (
    focus_id  TEXT NOT NULL REFERENCES focus_spaces(id),
    kind      TEXT NOT NULL CHECK (kind IN ('theme', 'subject')),
    member_id TEXT NOT NULL,
    added_at  INTEGER NOT NULL,
    PRIMARY KEY (focus_id, kind, member_id)
);
CREATE INDEX IF NOT EXISTS idx_focus_members_member ON focus_members(kind, member_id);
