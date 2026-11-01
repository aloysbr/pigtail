package store

import (
	"database/sql"
)

// invalidateEndStampsTx is the single invariant of the session_end stamp (D9,
// 2026-10-22): a stamp (wait_records.closed_by = 'session_end', see
// closedBySessionEnd) is valid only until the session's next wait write. A
// stamp says "the wait the session ended with is still the one to give back
// to a resume"; any later write of the session's waits (a declaration of any
// type, a replace, an answer, a clear of any kind, even one that finds no
// row) makes that no longer true, and judging it path by path let three
// review rounds find a door each.
//
// The ownership predicate is the record's session_id: a record the session
// emitted, whether it closed (cleared, restorable) or stays open because its
// subject owns it (D29-A). RestoreEndedWaits only ever reads the session's own
// latest declared record, so a subject's record emitted by another session
// is never this session's stamp to drop.
//
// Every writer of a session's waits calls it first, inside its transaction;
// the session-end paths call it first too and stamp after (clearTx). The
// restore resets the stamp it consumes itself. TestEndStampDiesOnEveryWritePath
// runs each write path; TestEveryWaitWriteInvalidatesEndStamps fails when a new
// function writes session_waits without calling this helper or being listed in
// endStampExempt.
func invalidateEndStampsTx(tx *sql.Tx, sessionID string) error {
	_, err := tx.Exec(`UPDATE wait_records SET closed_by = NULL WHERE session_id = ? AND closed_by = ?`,
		sessionID, closedBySessionEnd)
	return err
}

// endStampExempt names the functions that write session_waits without
// invalidating the stamps, each with its reason (read by the guard test).
// clearTx is not exempt: it calls the helper, on the session-end paths only
// when a row exists, so a repeated gone clear keeps the stamp its first run set.
var endStampExempt = map[string]string{
	"RestoreEndedWaits": "the restore consumes and resets the stamp itself",
	"ClearHeldWait":     "a held wait is tied to a pid and never restored; the holder's exit may come after the session end and must not drop its stamp",
}
