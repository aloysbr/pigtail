package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// --- Peer sessions --------------------------------------------------------

// A session's subject (sub_x000000000000021): an unended row reads the one
// explicitly bound to its cmux workspace (subject_cmux_bindings) when there is
// one, else the one stamped on the row (p.subject_id: the relay writes it on
// a proven-alive upsert, from the binding or the worktree deduction). The
// binding is joined on read, so a link written after the session registered
// shows at once. An ended row with a stamp reads its stamp: its tab may be
// rebound, unbound or closed since, the session worked on the stamped subject.
// CmuxBound says the workspace is bound to the subject the row reads. cmux
// prints workspace UUIDs in upper case and the binding table stores them so;
// the join upper-cases the session's copy, whatever its source wrote.
const peerSubjectExpr = `(CASE WHEN p.ended_at IS NOT NULL AND p.subject_id != '' THEN p.subject_id
		ELSE COALESCE(b.subject_id, p.subject_id) END)`

const peerColumns = `p.session_id, p.config_dir, p.cell_id, p.scope, p.cwd, p.name, p.status, p.pid,
	p.cmux_workspace_id, p.cmux_surface_id, p.cmux_title, p.workspace_id, p.workspace_name,
	` + peerSubjectExpr + `, COALESCE(s.slug, ''), p.lifecycle, p.source, p.updated_at, p.ended_at,
	p.last_worktree, p.last_worktree_at,
	b.subject_id IS NOT NULL AND b.subject_id = ` + peerSubjectExpr + `, COALESCE(s.short_title, ''), COALESCE(s.title, ''),
	p.subject_id, COALESCE(b.subject_id, '')`

const peerFrom = ` FROM peer_sessions p
	LEFT JOIN subject_cmux_bindings b ON p.cmux_workspace_id != '' AND b.cmux_workspace_id = UPPER(p.cmux_workspace_id)
	LEFT JOIN subjects s ON s.id = ` + peerSubjectExpr

// UpsertPeer inserts a peer session or merges it into the existing row.
//
// Merge, not replace: the registry is fed by several partial sources (a
// SessionStart hook knows the config dir and cmux ids but not yet the pid; a
// cmux snapshot knows the pid but not the native name of a session cmux does
// not host). An empty field never erases a known one, except the subject of a
// session the relay proved alive (SubjectSet: written as given). A row upserted again is
// alive again: ended_at is cleared (a resumed session reuses its id).
func (s *Store) UpsertPeer(p *pigtail.PeerSession) error {
	if p.SessionID == "" {
		return errors.New("store: peer without session id")
	}
	// The single point where a session's scope enters the store (POST /peers
	// and the cmux snapshot both land here): the alias and any case are
	// written canonical; '' (a session outside any cell) stays ''.
	if strings.TrimSpace(p.Scope) != "" {
		p.Scope = string(scope.Canonical(p.Scope))
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now().UTC()
	}
	_, err := s.db.Exec(`
INSERT INTO peer_sessions (session_id, config_dir, cell_id, scope, cwd, name, status, pid,
	cmux_workspace_id, cmux_surface_id, cmux_title, workspace_id, workspace_name, subject_id,
	lifecycle, source, updated_at, ended_at, last_worktree, last_worktree_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
	config_dir        = COALESCE(NULLIF(excluded.config_dir, ''), config_dir),
	cell_id           = COALESCE(NULLIF(excluded.cell_id, ''), cell_id),
	scope             = COALESCE(NULLIF(excluded.scope, ''), scope),
	cwd               = COALESCE(NULLIF(excluded.cwd, ''), cwd),
	name              = COALESCE(NULLIF(excluded.name, ''), name),
	status            = COALESCE(NULLIF(excluded.status, ''), status),
	pid               = CASE WHEN excluded.pid > 0 THEN excluded.pid ELSE pid END,
	cmux_workspace_id = COALESCE(NULLIF(excluded.cmux_workspace_id, ''), cmux_workspace_id),
	cmux_surface_id   = COALESCE(NULLIF(excluded.cmux_surface_id, ''), cmux_surface_id),
	cmux_title        = COALESCE(NULLIF(excluded.cmux_title, ''), cmux_title),
	workspace_id      = COALESCE(NULLIF(excluded.workspace_id, ''), workspace_id),
	workspace_name    = COALESCE(NULLIF(excluded.workspace_name, ''), workspace_name),
	subject_id        = CASE WHEN ? THEN excluded.subject_id ELSE COALESCE(NULLIF(excluded.subject_id, ''), peer_sessions.subject_id) END,
	lifecycle         = COALESCE(NULLIF(excluded.lifecycle, ''), lifecycle),
	source            = COALESCE(NULLIF(excluded.source, ''), source),
	last_worktree     = CASE WHEN excluded.last_worktree <> '' AND (last_worktree_at IS NULL OR excluded.last_worktree_at >= last_worktree_at)
	                         THEN excluded.last_worktree ELSE last_worktree END,
	last_worktree_at  = CASE WHEN excluded.last_worktree <> '' AND (last_worktree_at IS NULL OR excluded.last_worktree_at >= last_worktree_at)
	                         THEN excluded.last_worktree_at ELSE last_worktree_at END,
	updated_at        = excluded.updated_at,
	ended_at          = NULL`,
		p.SessionID, p.ConfigDir, p.CellID, p.Scope, p.Cwd, p.Name, p.Status, p.PID,
		p.CmuxWorkspaceID, p.CmuxSurfaceID, p.CmuxTitle, p.WorkspaceID, p.WorkspaceName, p.SubjectID,
		p.Lifecycle, p.Source, p.UpdatedAt.UnixMilli(),
		p.LastWorktree, nullMillis(p.LastWorktreeAt), p.SubjectSet,
	)
	return err
}

// nullMillis stores an optional time as epoch millis, NULL when unset.
func nullMillis(t *time.Time) sql.NullInt64 {
	if t == nil || t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

// EndPeer marks a peer session ended (SessionEnd) and drops what it waited
// for: an ended session waits for nothing — its subject's wait outlives it
// (D29-A). Unknown ids are a no-op. Its Standby row stays (D24: a relaunch
// resumes the same session id). It is EndPeerClosing without its ids; the
// records close at the caller's clock, like an answer's.
func (s *Store) EndPeer(sessionID string, at time.Time) error {
	_, _, err := s.EndPeerClosing(sessionID, at)
	return err
}

// GetPeer returns one peer session, or ErrNotFound.
func (s *Store) GetPeer(sessionID string) (*pigtail.PeerSession, error) {
	row := s.db.QueryRow(`SELECT `+peerColumns+peerFrom+` WHERE p.session_id = ?`, sessionID)
	return scanPeer(row)
}

// ListPeers returns every peer session not marked ended, most recently
// updated first. Pass includeEnded to get the ended ones too.
func (s *Store) ListPeers(includeEnded bool) ([]pigtail.PeerSession, error) {
	q := `SELECT ` + peerColumns + peerFrom
	if !includeEnded {
		q += ` WHERE p.ended_at IS NULL`
	}
	q += ` ORDER BY p.updated_at DESC`
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pigtail.PeerSession
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func scanPeer(r rowScanner) (*pigtail.PeerSession, error) {
	var (
		p       pigtail.PeerSession
		updated int64
		ended   sql.NullInt64
		lwAt    sql.NullInt64
		bound   int
		stShort string
		stTitle string
	)
	err := r.Scan(&p.SessionID, &p.ConfigDir, &p.CellID, &p.Scope, &p.Cwd, &p.Name, &p.Status, &p.PID,
		&p.CmuxWorkspaceID, &p.CmuxSurfaceID, &p.CmuxTitle, &p.WorkspaceID, &p.WorkspaceName,
		&p.SubjectID, &p.SubjectSlug, &p.Lifecycle, &p.Source, &updated, &ended,
		&p.LastWorktree, &lwAt,
		&bound, &stShort, &stTitle, &p.SubjectStamp, &p.CmuxBindingSubjectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.CmuxBound = bound != 0
	if p.SubjectID != "" {
		p.SubjectShortTitle = pigtail.ShortTitle(pigtail.Subject{ShortTitle: stShort, Title: stTitle})
	}
	p.UpdatedAt = time.UnixMilli(updated).UTC()
	if ended.Valid {
		t := time.UnixMilli(ended.Int64).UTC()
		p.EndedAt = &t
	}
	if lwAt.Valid {
		t := time.UnixMilli(lwAt.Int64).UTC()
		p.LastWorktreeAt = &t
	}
	return &p, nil
}

// SubjectForWorkspace returns the id of the subject one of whose phases runs
// in the given workspace, or ErrNotFound. When several subjects point at the
// same workspace, the most recent phase wins.
func (s *Store) SubjectForWorkspace(workspaceID string) (string, error) {
	var id string
	err := s.db.QueryRow(
		`SELECT subject_id FROM subject_phases WHERE workspace_id = ? ORDER BY created_at DESC LIMIT 1`,
		workspaceID,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

// --- Messages -------------------------------------------------------------

const messageColumns = `id, thread_id, in_reply_to, from_session, to_session, from_label, to_label,
	subject_id, kind, body, created_at, expires_at, delivered_at, delivered_via, acked_at, state`

// CreateMessage inserts a message.
func (s *Store) CreateMessage(m *pigtail.Message) error {
	return insertMessage(s.db, m)
}

// insertMessage inserts m through e: the database, or the transaction of a
// write the message belongs to (AnswerWaitWithMessage).
func insertMessage(e execer, m *pigtail.Message) error {
	if m.State == "" {
		m.State = pigtail.MessageStatePending
	}
	_, err := e.Exec(`INSERT INTO messages (`+messageColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ThreadID, nullStr(m.InReplyTo), m.FromSession, m.ToSession, m.FromLabel, m.ToLabel,
		nullStr(m.SubjectID), m.Kind, m.Body, m.CreatedAt.UnixMilli(), m.ExpiresAt.UnixMilli(),
		nullTime(m.DeliveredAt), nullStr(m.DeliveredVia), nullTime(m.AckedAt), m.State,
	)
	return err
}

// GetMessage returns one message, or ErrNotFound.
func (s *Store) GetMessage(id string) (*pigtail.Message, error) {
	return scanMessage(s.db.QueryRow(`SELECT `+messageColumns+` FROM messages WHERE id = ?`, id))
}

// ListMessages returns the messages matching f, oldest first.
func (s *Store) ListMessages(f pigtail.MessageFilter) ([]pigtail.Message, error) {
	return s.ListMessagesInScope(f, scope.Any)
}

// ListMessagesInScope is ListMessages restricted to the conversations of one
// scope: every session of the message (the system sender aside, and the
// sender of a notice) must be a registered peer of scope sc. A session the registry does not know, or with
// no scope, excludes the message — fail-closed. scope.Any restricts nothing.
// The filter applies before f.Limit, so a limit counts visible messages.
func (s *Store) ListMessagesInScope(f pigtail.MessageFilter, sc scope.Scope) ([]pigtail.Message, error) {
	var (
		where []string
		args  []any
	)
	add := func(cond string, v any) {
		where = append(where, cond)
		args = append(args, v)
	}
	if f.To != "" {
		add(`to_session = ?`, f.To)
	}
	// fromIs matches the sender as a caller of scope sc reads it: within a
	// scoped view, a notice whose sender is not a peer of sc reads as the
	// system sender (handlers redactMessages), so it matches
	// from=SystemSender. Any other id matches the stored sender; a session sc
	// cannot read is refused by the handler before this query (D19).
	fromIs := func(id string) (string, []any) {
		if id == pigtail.SystemSender && sc != scope.Any {
			return `(from_session = ? OR (kind = ? AND NOT EXISTS (SELECT 1 FROM peer_sessions p WHERE p.session_id = messages.from_session AND p.scope = ?)))`,
				[]any{id, pigtail.MessageKindNotice, string(sc)}
		}
		return `from_session = ?`, []any{id}
	}
	if f.From != "" {
		cond, a := fromIs(f.From)
		where = append(where, cond)
		args = append(args, a...)
	}
	if f.Session != "" {
		cond, a := fromIs(f.Session)
		where = append(where, `(to_session = ? OR `+cond+`)`)
		args = append(append(args, f.Session), a...)
	}
	if f.State != "" {
		add(`state = ?`, f.State)
	}
	if f.Thread != "" {
		add(`thread_id = ?`, f.Thread)
	}
	if f.Subject != "" {
		add(`subject_id = ?`, f.Subject)
	}
	if !f.Since.IsZero() {
		add(`created_at >= ?`, f.Since.UnixMilli())
	}
	if sc != scope.Any {
		// The sender side: the system sender or the user (HumanSender, no peer
		// row), a notice (visible from its recipient's scope whatever its
		// sender's, D1 — the handlers redact the sender, relay_scope.go), or
		// a peer of scope sc. The notice
		// branch skips the sender's scope: ListMessages refuses, before this
		// query, a filter on a session the caller cannot read (D19).
		where = append(where,
			`(messages.from_session IN (?, ?) OR messages.kind = ? OR EXISTS (SELECT 1 FROM peer_sessions p WHERE p.session_id = messages.from_session AND p.scope = ?))`,
			`EXISTS (SELECT 1 FROM peer_sessions p WHERE p.session_id = messages.to_session AND p.scope = ?)`)
		args = append(args, pigtail.SystemSender, pigtail.HumanSender, pigtail.MessageKindNotice, string(sc), string(sc))
	}
	q := `SELECT ` + messageColumns + ` FROM messages`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `) //nolint:gosec // G202: fixed fragments only; every value is a bound parameter
	}
	q += ` ORDER BY created_at, id`
	if f.Limit > 0 {
		// Keep the most recent f.Limit, still returned oldest first.
		q = `SELECT * FROM (` + strings.Replace(q, ` ORDER BY created_at, id`, ` ORDER BY created_at DESC, id DESC`, 1) +
			fmt.Sprintf(` LIMIT %d) ORDER BY created_at, id`, f.Limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pigtail.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// notDue is the « still alive » test of a pending message (D28,
// sub_x000000000000041): not expired, or its recipient is in Standby — the
// countdown is suspended there and resumes when the Standby ends
// (resumeExpiryTx). The table is read in the query itself, the one predicate
// the store has (like RecordTurnEnd).
const notDue = `(expires_at > ? OR EXISTS (SELECT 1 FROM session_standby sb WHERE sb.session_id = messages.to_session))`

// CountPending counts a recipient's pending, unexpired messages.
func (s *Store) CountPending(toSession string, now time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE to_session = ? AND state = 'pending' AND `+notDue,
		toSession, now.UnixMilli()).Scan(&n)
	return n, err
}

// CountPendingExceptKind counts a recipient's pending, unexpired messages
// other than those of kind (the idle wrap leaves one-way notices to the
// inbox hook at the done command's prompt, relay_wrap.go).
func (s *Store) CountPendingExceptKind(toSession, kind string, now time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE to_session = ? AND state = 'pending' AND kind <> ? AND `+notDue,
		toSession, kind, now.UnixMilli()).Scan(&n)
	return n, err
}

// CountSentSince counts the messages from one session to another created at
// or after since (the rate limit's window).
func (s *Store) CountSentSince(fromSession, toSession string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE from_session = ? AND to_session = ? AND created_at >= ?`,
		fromSession, toSession, since.UnixMilli()).Scan(&n)
	return n, err
}

// CountThread counts the messages of a thread.
func (s *Store) CountThread(threadID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE thread_id = ?`, threadID).Scan(&n)
	return n, err
}

// ClaimPending atomically moves every pending, unexpired message of a
// recipient to delivered and returns them, oldest first. Two claimers racing
// (the prompt hook and the Stop watcher) never both get the same message: the
// UPDATE only takes rows still pending.
func (s *Store) ClaimPending(toSession, via string, now time.Time) ([]pigtail.Message, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Query(`SELECT id FROM messages WHERE to_session = ? AND state = 'pending' AND `+notDue+` ORDER BY created_at, id`,
		toSession, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []pigtail.Message
	for _, id := range ids {
		res, err := tx.Exec(`UPDATE messages SET state = 'delivered', delivered_at = ?, delivered_via = ? WHERE id = ? AND state = 'pending'`,
			now.UnixMilli(), via, id)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		m, err := scanMessage(tx.QueryRow(`SELECT `+messageColumns+` FROM messages WHERE id = ?`, id))
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, tx.Commit()
}

// AckMessage marks a message acknowledged by its recipient. A pending message
// acked directly counts as delivered too. Returns ErrNotFound for an unknown
// id.
func (s *Store) AckMessage(id string, now time.Time) error {
	res, err := s.db.Exec(`UPDATE messages SET
		acked_at = ?,
		delivered_at = COALESCE(delivered_at, ?),
		delivered_via = COALESCE(delivered_via, 'ack'),
		state = 'acked'
		WHERE id = ? AND state IN ('pending', 'delivered', 'acked')`,
		now.UnixMilli(), now.UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.GetMessage(id); err != nil {
			return err
		}
		return fmt.Errorf("store: message %s cannot be acked in its current state", id)
	}
	return nil
}

// ExpireDue moves every pending message whose expiry has passed to expired and
// returns them, so the caller can tell each sender. A message for a session in
// Standby is not due (D28).
func (s *Store) ExpireDue(now time.Time) ([]pigtail.Message, error) {
	rows, err := s.db.Query(`SELECT `+messageColumns+` FROM messages WHERE state = 'pending' AND expires_at <= ?
		AND NOT EXISTS (SELECT 1 FROM session_standby sb WHERE sb.session_id = messages.to_session)`, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	var due []pigtail.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		due = append(due, *m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []pigtail.Message
	for _, m := range due {
		res, err := s.db.Exec(`UPDATE messages SET state = 'expired' WHERE id = ? AND state = 'pending'`, m.ID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			m.State = pigtail.MessageStateExpired
			out = append(out, m)
		}
	}
	return out, nil
}

// PendingRecipients lists the sessions that have at least one pending,
// unexpired message (to rebuild inbox markers).
func (s *Store) PendingRecipients(now time.Time) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT to_session FROM messages WHERE state = 'pending' AND `+notDue, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// PendingRecipient is a session with pending mail, as the wake sweep walks it.
type PendingRecipient struct {
	Session      string
	OldestAt     time.Time // created_at of its oldest pending, unexpired message
	HumanPending bool      // one of them is the user's own (pigtail.HumanSender): the budget exemption
}

// PendingRecipientsByAge lists the sessions with at least one pending,
// unexpired message, the one waiting longest first (ties by session id), each
// with its oldest message's time and whether the user's own answer is among them.
// PendingRecipients stays for the markers, which need no order.
func (s *Store) PendingRecipientsByAge(now time.Time) ([]PendingRecipient, error) {
	rows, err := s.db.Query(`SELECT to_session, MIN(created_at),
		MAX(CASE WHEN from_session = ? THEN 1 ELSE 0 END)
		FROM messages WHERE state = 'pending' AND `+notDue+`
		GROUP BY to_session ORDER BY MIN(created_at), to_session`, pigtail.HumanSender, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingRecipient
	for rows.Next() {
		var r PendingRecipient
		var oldest int64
		var human int
		if err := rows.Scan(&r.Session, &oldest, &human); err != nil {
			return nil, err
		}
		r.OldestAt, r.HumanPending = time.UnixMilli(oldest).UTC(), human == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

func nullTime(t *time.Time) sql.NullInt64 {
	if t == nil || t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

func scanMessage(r rowScanner) (*pigtail.Message, error) {
	var (
		m                       pigtail.Message
		inReplyTo, subject, via sql.NullString
		created, expires        int64
		delivered, acked        sql.NullInt64
	)
	err := r.Scan(&m.ID, &m.ThreadID, &inReplyTo, &m.FromSession, &m.ToSession, &m.FromLabel, &m.ToLabel,
		&subject, &m.Kind, &m.Body, &created, &expires, &delivered, &via, &acked, &m.State)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	m.InReplyTo, m.SubjectID, m.DeliveredVia = inReplyTo.String, subject.String, via.String
	m.CreatedAt = time.UnixMilli(created).UTC()
	m.ExpiresAt = time.UnixMilli(expires).UTC()
	if delivered.Valid {
		t := time.UnixMilli(delivered.Int64).UTC()
		m.DeliveredAt = &t
	}
	if acked.Valid {
		t := time.UnixMilli(acked.Int64).UTC()
		m.AckedAt = &t
	}
	return &m, nil
}
