-- A cmux workspace declared off-topic (subject « Liaison session → sujet », D1):
-- the session there works on no subject, by choice. One row per workspace,
-- dropped when the workspace closes (missing_ticks, like subject_cmux_bindings)
-- or when a subject is bound to it. Additive.
CREATE TABLE IF NOT EXISTS workspace_off_topic (
    cmux_workspace_id TEXT PRIMARY KEY,
    scope             TEXT NOT NULL CHECK (length(scope) BETWEEN 1 AND 32
                                           AND scope NOT GLOB '*[^a-z0-9-]*'),
    session_id        TEXT NOT NULL DEFAULT '',
    reason            TEXT NOT NULL DEFAULT '' CHECK (length(reason) <= 200
                                                     AND instr(reason, char(10)) = 0
                                                     AND instr(reason, char(13)) = 0),
    declared_at       INTEGER NOT NULL,
    missing_ticks     INTEGER NOT NULL DEFAULT 0
);
