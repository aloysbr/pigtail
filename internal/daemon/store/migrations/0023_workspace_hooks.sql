-- Post-create hooks outcome per workspace (subject « hooks post-create
-- détachés », D2, 2026-10-13). NULL hooks_status = unknown: a workspace
-- created before this migration, or a project without hooks — never read as
-- a failure. Additive.
ALTER TABLE workspaces ADD COLUMN hooks_status TEXT
    CHECK (hooks_status IS NULL OR hooks_status IN ('running', 'ok', 'failed'));
ALTER TABLE workspaces ADD COLUMN hooks_hook  TEXT;
ALTER TABLE workspaces ADD COLUMN hooks_error TEXT;
ALTER TABLE workspaces ADD COLUMN hooks_log   TEXT;
ALTER TABLE workspaces ADD COLUMN hooks_at    INTEGER;
