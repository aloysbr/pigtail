-- Last worktree a session worked in, from its newest presence marker
-- (a shell hook), read by the CLI with no TTL.
-- The launch cwd of a Claude session is the client root, so the cwd-derived
-- workspace_id is empty for almost every session.
ALTER TABLE peer_sessions ADD COLUMN last_worktree TEXT NOT NULL DEFAULT '';
ALTER TABLE peer_sessions ADD COLUMN last_worktree_at INTEGER;
