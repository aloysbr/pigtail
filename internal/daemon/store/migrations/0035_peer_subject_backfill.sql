-- peer_sessions.subject_id now holds the
-- subject a session worked on (the relay stamps it on a proven-alive upsert).
-- Backfill the rows the current binding already covers: only where subject_id
-- is '' (never overwrites a non-empty value), the session has a cmux
-- workspace, and that workspace's binding is not newer than the row
-- (bound_at <= updated_at, both epoch millis). Peer rows may hold the
-- workspace id in any case, bindings are stored upper case; every column of
-- the correlated subquery is qualified (both tables have cmux_workspace_id and
-- subject_id). 0035 is also used by 0035_subject_finds.sql:
-- tolerated, _migrations is keyed by file name. Additive data fill.
UPDATE peer_sessions
SET subject_id = (
    SELECT b.subject_id FROM subject_cmux_bindings b
    WHERE b.cmux_workspace_id = UPPER(peer_sessions.cmux_workspace_id)
      AND b.bound_at <= peer_sessions.updated_at)
WHERE peer_sessions.subject_id = ''
  AND peer_sessions.cmux_workspace_id != ''
  AND EXISTS (
    SELECT 1 FROM subject_cmux_bindings b
    WHERE b.cmux_workspace_id = UPPER(peer_sessions.cmux_workspace_id)
      AND b.bound_at <= peer_sessions.updated_at);
