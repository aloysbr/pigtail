-- 0030_wait_records_closed_by.sql — why a wait record was closed as cleared.
-- A cmux restart ends every Claude session (SessionEnd hook, or the sidebar
-- loop judging it gone) and clears its waits; cmux then resumes the same
-- session id and nothing restored them, so the idle close typed its command
-- into sessions a declared wait should have held. closed_by = 'session_end'
-- marks a record closed by the end of its session, so a re-registration under
-- the same id can reopen it. NULL = closed by the user's prompt, --clear, a turn,
-- an answer or a redeclare. Additive.
ALTER TABLE wait_records ADD COLUMN closed_by TEXT;
