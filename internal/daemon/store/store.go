package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// ErrNotFound is returned when a lookup matches no row.
var ErrNotFound = errors.New("store: not found")

// ErrHooksRunning: the workspace's post-create hooks are already recorded as running.
var ErrHooksRunning = errors.New("store: post-create hooks already running")

// Store is the daemon's persistent state, backed by SQLite, with a small
// in-memory cache for hot reads on workspaces.
type Store struct {
	db *sql.DB
	// path is where this database lives, as passed to Open. Kept so the backup
	// path derives from the database that is actually open rather than from a
	// path a caller believes is in use — an in-memory store must not produce
	// snapshots just because the daemon config names a file.
	path string

	mu      sync.RWMutex
	wsCache map[string]*pigtail.Workspace // by ID
	// wsGen counts invalidations: a GetWorkspace caches what it read only if
	// no invalidation happened since it began, else a row read before a
	// concurrent write would be cached after that write's invalidation and
	// served stale for good.
	wsGen uint64

	// afterWorkspaceRead, when set (tests only), runs in GetWorkspace between
	// the database read and the cache write: the window a concurrent write
	// lands in.
	afterWorkspaceRead func(id string)

	// focusSealGate holds focus seals while it answers true (focus D26,
	// SetFocusSealGate in store_focus.go); nil = never held.
	focusSealGate atomic.Pointer[func() bool]
}

// Open opens (or creates) the SQLite database at the given path and applies
// pending migrations. Pass ":memory:" for an ephemeral in-memory store
// (used in tests).
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // simplifies WAL/locking semantics for the daemon
	if err := applyMigrations(db, migrationsFS); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, path: path, wsCache: map[string]*pigtail.Workspace{}}, nil
}

// Ping runs SELECT 1 on the store's single connection, bounded by ctx: a
// connection held by a stuck transaction makes it return the ctx error
// instead of waiting for ever (/health?deep=1).
func (s *Store) Ping(ctx context.Context) error {
	var one int
	return s.db.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}

// Close releases the underlying DB handle.
func (s *Store) Close() error { return s.db.Close() }

func applyMigrations(db *sql.DB, fsys fs.FS) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS _migrations (name TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create _migrations table: %w", err)
	}
	// Table rebuilds (0015) drop a parent table; with foreign keys on, SQLite
	// would delete or refuse. Off is SQLite's default and pigtail never turns
	// it on — said here explicitly, outside any transaction, where it counts.
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign keys for migrations: %w", err)
	}
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE name = ?`, name).Scan(&count); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if count > 0 {
			continue
		}
		body, err := fs.ReadFile(fsys, "migrations/"+name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := applyOne(db, name, string(body)); err != nil {
			return err
		}
	}
	return nil
}

// applyOne runs one migration and records it in a single transaction: a
// failure leaves the database exactly as it was, and the migration unrecorded.
func applyOne(db *sql.DB, name, body string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	if _, err := tx.Exec(body); err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	if _, err := tx.Exec(`INSERT INTO _migrations (name, applied_at) VALUES (?, ?)`, name, time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", name, err)
	}
	return nil
}

// --- Projects -------------------------------------------------------------

// CreateProject inserts a new project row.
func (s *Store) CreateProject(p *pigtail.Project) error {
	portRanges, err := json.Marshal(p.PortRanges)
	if err != nil {
		return err
	}
	var owner, repo, kind sql.NullString
	if p.Remote != nil {
		kind.Valid, kind.String = true, p.Remote.Kind
		owner.Valid, owner.String = true, p.Remote.Owner
		repo.Valid, repo.String = true, p.Remote.Repo
	}
	var portEnvs, defaults sql.NullString
	if len(p.PortEnvs) > 0 {
		b, err := json.Marshal(p.PortEnvs)
		if err != nil {
			return err
		}
		portEnvs.Valid, portEnvs.String = true, string(b)
	}
	if p.Defaults != nil {
		b, err := json.Marshal(p.Defaults)
		if err != nil {
			return err
		}
		defaults.Valid, defaults.String = true, string(b)
	}
	_, err = s.db.Exec(
		`INSERT INTO projects (id, name, root_path, main_worktree, remote_kind, remote_owner, remote_repo, default_profile_id, default_backend, port_ranges, port_envs, defaults)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.RootPath, nullStr(p.MainWorktree), kind, owner, repo, nullStr(p.DefaultProfileID), p.DefaultBackend, string(portRanges), portEnvs, defaults,
	)
	return err
}

// UpdateProjectConfig rewrites the fields a project's `pigtail.yaml` owns, on a
// project that is already registered.
//
// Why this exists (mesuré le 2026-09-04) : registering an already-known project
// was a no-op success. The daemon reads a project's settings from its DATABASE,
// and the config file only ever fed the FIRST registration — so editing
// `pigtail.yaml` afterwards did nothing at all, while `pigtail init` still
// answered "registered". A `base_ref: origin/main` sat in a yaml for two months
// without effect, and worktrees kept branching 27, 47 and 53 commits behind a
// stale local main because the row still said `main`.
//
// Only the columns the config file actually carries are written. Anything the
// daemon owns and the yaml knows nothing about — `default_profile_id`, the
// remote triple — is left untouched, so a re-registration can never blank it.
func (s *Store) UpdateProjectConfig(p *pigtail.Project) error {
	portRanges, err := json.Marshal(p.PortRanges)
	if err != nil {
		return err
	}
	var portEnvs, defaults sql.NullString
	if len(p.PortEnvs) > 0 {
		b, err := json.Marshal(p.PortEnvs)
		if err != nil {
			return err
		}
		portEnvs.Valid, portEnvs.String = true, string(b)
	}
	if p.Defaults != nil {
		b, err := json.Marshal(p.Defaults)
		if err != nil {
			return err
		}
		defaults.Valid, defaults.String = true, string(b)
	}
	res, err := s.db.Exec(
		`UPDATE projects
		    SET name = ?, root_path = ?, main_worktree = ?, default_backend = ?,
		        port_ranges = ?, port_envs = ?, defaults = ?
		  WHERE id = ?`,
		p.Name, p.RootPath, nullStr(p.MainWorktree), p.DefaultBackend,
		string(portRanges), portEnvs, defaults, p.ID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

// GetProject loads a project by ID.
func (s *Store) GetProject(id string) (*pigtail.Project, error) {
	row := s.db.QueryRow(
		`SELECT id, name, root_path, main_worktree, remote_kind, remote_owner, remote_repo, default_profile_id, default_backend, port_ranges, port_envs, defaults
		 FROM projects WHERE id = ?`, id,
	)
	return scanProject(row)
}

// ListProjects returns all registered projects.
func (s *Store) ListProjects() ([]pigtail.Project, error) {
	rows, err := s.db.Query(
		`SELECT id, name, root_path, main_worktree, remote_kind, remote_owner, remote_repo, default_profile_id, default_backend, port_ranges, port_envs, defaults
		 FROM projects ORDER BY name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pigtail.Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// --- Workspaces -----------------------------------------------------------

// CreateWorkspace inserts a new workspace row.
func (s *Store) CreateWorkspace(ws *pigtail.Workspace) error {
	ports, _ := json.Marshal(ws.Ports)
	backendData, _ := json.Marshal(ws.BackendData)
	var snoozed sql.NullInt64
	if ws.SnoozedUntil != nil {
		snoozed.Valid, snoozed.Int64 = true, ws.SnoozedUntil.UnixMilli()
	}
	if ws.CreatedAt.IsZero() {
		ws.CreatedAt = time.Now().UTC()
	}
	if ws.LastActivityAt.IsZero() {
		ws.LastActivityAt = ws.CreatedAt
	}
	var runawayAt sql.NullInt64
	if ws.LastRunawayAt != nil {
		runawayAt.Valid, runawayAt.Int64 = true, ws.LastRunawayAt.UnixMilli()
	}
	_, err := s.db.Exec(
		`INSERT INTO workspaces (id, project_id, intent, state, path, ports, recipe_id, backend_kind, backend_data,
		                         created_at, last_activity_at, snoozed_until, active_delivery_id,
		                         last_runaway_at, last_runaway_reason)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ws.ID, ws.ProjectID, ws.Intent, string(ws.State), ws.Path, string(ports), nullStr(ws.RecipeID),
		ws.BackendKind, string(backendData),
		ws.CreatedAt.UnixMilli(), ws.LastActivityAt.UnixMilli(), snoozed, nullStr(ws.ActiveDeliveryID),
		runawayAt, nullStr(ws.LastRunawayReason),
	)
	if err != nil {
		return err
	}
	s.cacheWorkspace(ws)
	return nil
}

// GetWorkspace loads a workspace by ID (with agent sessions attached).
func (s *Store) GetWorkspace(id string) (*pigtail.Workspace, error) {
	s.mu.RLock()
	if ws, ok := s.wsCache[id]; ok {
		s.mu.RUnlock()
		return ws, nil
	}
	gen := s.wsGen
	s.mu.RUnlock()

	row := s.db.QueryRow(
		`SELECT id, project_id, intent, state, path, ports, recipe_id, backend_kind, backend_data,
		        created_at, last_activity_at, snoozed_until, active_delivery_id,
		        last_runaway_at, last_runaway_reason,
		        hooks_status, hooks_hook, hooks_error, hooks_log, hooks_at
		 FROM workspaces WHERE id = ?`, id,
	)
	ws, err := scanWorkspace(row)
	if err != nil {
		return nil, err
	}
	sessions, err := s.ListAgentSessionsByWorkspace(id)
	if err != nil {
		return nil, err
	}
	ws.AgentSessions = sessions
	if s.afterWorkspaceRead != nil {
		s.afterWorkspaceRead(id)
	}
	s.mu.Lock()
	if s.wsGen == gen {
		s.wsCache[id] = ws
	}
	s.mu.Unlock()
	return ws, nil
}

// ListWorkspaces returns all workspaces optionally filtered by state.
func (s *Store) ListWorkspaces(state pigtail.WorkspaceState) ([]pigtail.Workspace, error) {
	query := `SELECT id, project_id, intent, state, path, ports, recipe_id, backend_kind, backend_data,
		             created_at, last_activity_at, snoozed_until, active_delivery_id,
		             last_runaway_at, last_runaway_reason,
		        hooks_status, hooks_hook, hooks_error, hooks_log, hooks_at
		      FROM workspaces`
	args := []any{}
	if state != "" {
		query += ` WHERE state = ?`
		args = append(args, string(state))
	}
	query += ` ORDER BY last_activity_at DESC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pigtail.Workspace
	for rows.Next() {
		ws, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ws)
	}
	return out, rows.Err()
}

// SetWorkspaceHooks records the post-create hooks outcome of a workspace.
func (s *Store) SetWorkspaceHooks(id string, r pigtail.HooksResult) error {
	if r.At.IsZero() {
		r.At = time.Now().UTC()
	}
	res, err := s.db.Exec(`UPDATE workspaces SET hooks_status = ?, hooks_hook = ?, hooks_error = ?, hooks_log = ?, hooks_at = ? WHERE id = ?`,
		string(r.Status), nullStr(r.Hook), nullStr(r.Error), nullStr(r.Log), r.At.UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.invalidateWorkspace(id)
	return nil
}

// ClaimWorkspaceHooks records a new run of a workspace's post-create hooks as
// running, in one conditional UPDATE: two runs of the same workspace never
// overlap. ErrNotFound when the workspace does not exist.
func (s *Store) ClaimWorkspaceHooks(id, logPath string) error {
	res, err := s.db.Exec(`UPDATE workspaces SET hooks_status = 'running', hooks_hook = NULL, hooks_error = NULL, hooks_log = ?, hooks_at = ?
		WHERE id = ? AND (hooks_status IS NULL OR hooks_status <> 'running')`,
		nullStr(logPath), time.Now().UTC().UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		s.invalidateWorkspace(id)
		return nil
	}
	if _, err := s.GetWorkspace(id); err != nil {
		return err // ErrNotFound
	}
	return ErrHooksRunning
}

// FailRunningHooks turns every "running" record into a failure carrying
// reason: called at daemon start, when no hook of this process can be running.
func (s *Store) FailRunningHooks(reason string) (int, error) {
	rows, err := s.db.Query(`SELECT id FROM workspaces WHERE hooks_status = 'running'`)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	now := time.Now().UTC().UnixMilli()
	for _, id := range ids {
		if _, err := s.db.Exec(`UPDATE workspaces SET hooks_status = 'failed', hooks_error = ?, hooks_at = ? WHERE id = ? AND hooks_status = 'running'`,
			reason, now, id); err != nil {
			return 0, err
		}
		s.invalidateWorkspace(id)
	}
	return len(ids), nil
}

// UpdateWorkspaceState changes the state column and bumps last_activity_at.
func (s *Store) UpdateWorkspaceState(id string, state pigtail.WorkspaceState) error {
	now := time.Now().UTC().UnixMilli()
	_, err := s.db.Exec(
		`UPDATE workspaces SET state = ?, last_activity_at = ? WHERE id = ?`,
		string(state), now, id,
	)
	if err != nil {
		return err
	}
	s.invalidateWorkspace(id)
	return nil
}

// UpdateWorkspacePath sets the path column (and, when backendData is
// non-nil, the backend_data column) and bumps last_activity_at. It does
// NOT change state. Pass a nil backendData to leave backend_data untouched.
// Invalidates the workspace cache.
func (s *Store) UpdateWorkspacePath(id, newPath string, backendData map[string]any) error {
	now := time.Now().UTC().UnixMilli()
	var err error
	if backendData == nil {
		_, err = s.db.Exec(
			`UPDATE workspaces SET path = ?, last_activity_at = ? WHERE id = ?`,
			newPath, now, id,
		)
	} else {
		bd, merr := json.Marshal(backendData)
		if merr != nil {
			return merr
		}
		_, err = s.db.Exec(
			`UPDATE workspaces SET path = ?, backend_data = ?, last_activity_at = ? WHERE id = ?`,
			newPath, string(bd), now, id,
		)
	}
	if err != nil {
		return err
	}
	s.invalidateWorkspace(id)
	return nil
}

// UpdateWorkspacePorts replaces the ports column. Used by
// PATCH /workspaces/{id}/ports (resync, reassign). Invalidates the cache.
func (s *Store) UpdateWorkspacePorts(id string, ports map[string]int) error {
	b, err := json.Marshal(ports)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE workspaces SET ports = ?, last_activity_at = ? WHERE id = ?`,
		string(b), time.Now().UTC().UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.invalidateWorkspace(id)
	return nil
}

// RecordRunaway stamps the workspace with the time and reason of a runaway
// dev-server kill performed by the watchdog.
func (s *Store) RecordRunaway(id, reason string) error {
	now := time.Now().UTC().UnixMilli()
	_, err := s.db.Exec(
		`UPDATE workspaces SET last_runaway_at = ?, last_runaway_reason = ? WHERE id = ?`,
		now, reason, id,
	)
	if err != nil {
		return err
	}
	s.invalidateWorkspace(id)
	return nil
}

// DeleteWorkspace removes a workspace and its agent sessions.
func (s *Store) DeleteWorkspace(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`DELETE FROM agent_sessions WHERE workspace_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM workspaces WHERE id = ?`, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateWorkspace(id)
	return nil
}

// UsedPorts returns the set of ports currently bound to workspaces for
// the given kind (e.g. "dev"). Used by the port allocator on each
// allocation to avoid collisions.
func (s *Store) UsedPorts(kind string) (map[int]bool, error) {
	rows, err := s.db.Query(`SELECT ports FROM workspaces`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var portsJSON string
		if err := rows.Scan(&portsJSON); err != nil {
			return nil, err
		}
		var m map[string]int
		if err := json.Unmarshal([]byte(portsJSON), &m); err != nil {
			continue
		}
		if p, ok := m[kind]; ok {
			out[p] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Static servers each bind one concrete TCP port that is not associated
	// with any workspace "kind". A static server's port collides with an
	// allocation of *any* kind onto the same number, so fold every static
	// server port into the used set regardless of the requested kind.
	ssRows, err := s.db.Query(`SELECT port FROM static_servers`)
	if err != nil {
		return nil, err
	}
	defer ssRows.Close()
	for ssRows.Next() {
		var p int
		if err := ssRows.Scan(&p); err != nil {
			return nil, err
		}
		out[p] = true
	}
	return out, ssRows.Err()
}

// PortRegistrants names the rows registering port for kind — every row, live
// or not, sorted by id — and whether a static server holds the number (any
// kind), the two sources UsedPorts folds together.
func (s *Store) PortRegistrants(kind string, port int) ([]string, bool, error) {
	rows, err := s.db.Query(`SELECT id, ports FROM workspaces ORDER BY id`)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id, portsJSON string
		if err := rows.Scan(&id, &portsJSON); err != nil {
			return nil, false, err
		}
		var m map[string]int
		if json.Unmarshal([]byte(portsJSON), &m) != nil {
			continue
		}
		if p, ok := m[kind]; ok && p == port {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM static_servers WHERE port = ?`, port).Scan(&n); err != nil {
		return nil, false, err
	}
	return ids, n > 0, nil
}

// --- Agent sessions -------------------------------------------------------

// CreateAgentSession inserts a new agent session row.
func (s *Store) CreateAgentSession(sess *pigtail.AgentSession) error {
	if sess.StartedAt.IsZero() {
		sess.StartedAt = time.Now().UTC()
	}
	if sess.LastEventAt.IsZero() {
		sess.LastEventAt = sess.StartedAt
	}
	_, err := s.db.Exec(
		`INSERT INTO agent_sessions (id, workspace_id, agent_kind, intent, profile_id, state, tmux_session_name,
		                              started_at, last_event_at, external_session_id, message_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.WorkspaceID, sess.AgentKind, nullStr(sess.Intent), nullStr(sess.ProfileID), string(sess.State),
		nullStr(sess.TmuxSessionName),
		sess.StartedAt.UnixMilli(), sess.LastEventAt.UnixMilli(),
		nullStr(sess.ExternalSessionID), sess.MessageCount,
	)
	if err != nil {
		return err
	}
	s.invalidateWorkspace(sess.WorkspaceID)
	return nil
}

// ListAgentSessionsByWorkspace returns all sessions tied to one workspace.
func (s *Store) ListAgentSessionsByWorkspace(workspaceID string) ([]pigtail.AgentSession, error) {
	rows, err := s.db.Query(
		`SELECT id, workspace_id, agent_kind, intent, profile_id, state, tmux_session_name,
		        started_at, last_event_at, external_session_id, message_count
		 FROM agent_sessions WHERE workspace_id = ? ORDER BY started_at`, workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pigtail.AgentSession
	for rows.Next() {
		sess, err := scanAgentSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sess)
	}
	return out, rows.Err()
}

// UpdateAgentSessionState changes the state column and bumps last_event_at.
func (s *Store) UpdateAgentSessionState(id string, state pigtail.AgentState) error {
	now := time.Now().UTC().UnixMilli()
	_, err := s.db.Exec(
		`UPDATE agent_sessions SET state = ?, last_event_at = ? WHERE id = ?`,
		string(state), now, id,
	)
	return err
}

// --- cache helpers --------------------------------------------------------

func (s *Store) cacheWorkspace(ws *pigtail.Workspace) {
	s.mu.Lock()
	s.wsCache[ws.ID] = ws
	s.mu.Unlock()
}

func (s *Store) invalidateWorkspace(id string) {
	s.mu.Lock()
	delete(s.wsCache, id)
	s.wsGen++
	s.mu.Unlock()
}

// --- scanners -------------------------------------------------------------

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProject(r rowScanner) (*pigtail.Project, error) {
	var (
		p                     pigtail.Project
		mainWT                sql.NullString
		kind, ow              sql.NullString
		repo, prof            sql.NullString
		portsJSON             string
		portEnvsJSON, defJSON sql.NullString
	)
	err := r.Scan(&p.ID, &p.Name, &p.RootPath, &mainWT, &kind, &ow, &repo, &prof, &p.DefaultBackend, &portsJSON, &portEnvsJSON, &defJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.MainWorktree = mainWT.String
	if kind.Valid || ow.Valid || repo.Valid {
		p.Remote = &pigtail.Remote{Kind: kind.String, Owner: ow.String, Repo: repo.String}
	}
	p.DefaultProfileID = prof.String
	if err := json.Unmarshal([]byte(portsJSON), &p.PortRanges); err != nil {
		return nil, fmt.Errorf("decode port_ranges: %w", err)
	}
	if portEnvsJSON.Valid && portEnvsJSON.String != "" && portEnvsJSON.String != "null" {
		if err := json.Unmarshal([]byte(portEnvsJSON.String), &p.PortEnvs); err != nil {
			return nil, fmt.Errorf("decode port_envs: %w", err)
		}
	}
	if defJSON.Valid && defJSON.String != "" && defJSON.String != "null" {
		var d pigtail.ProjectDefaults
		if err := json.Unmarshal([]byte(defJSON.String), &d); err != nil {
			return nil, fmt.Errorf("decode defaults: %w", err)
		}
		p.Defaults = &d
	}
	return &p, nil
}

func scanWorkspace(r rowScanner) (*pigtail.Workspace, error) {
	var (
		ws                    pigtail.Workspace
		recipe, activeDel     sql.NullString
		portsJSON, beDataJSON sql.NullString
		createdAt, lastAct    int64
		snoozedAt, runawayAt  sql.NullInt64
		runawayReason         sql.NullString
		stateStr              string
		hStatus, hHook        sql.NullString
		hErr, hLog            sql.NullString
		hAt                   sql.NullInt64
	)
	err := r.Scan(&ws.ID, &ws.ProjectID, &ws.Intent, &stateStr, &ws.Path,
		&portsJSON, &recipe, &ws.BackendKind, &beDataJSON,
		&createdAt, &lastAct, &snoozedAt, &activeDel,
		&runawayAt, &runawayReason,
		&hStatus, &hHook, &hErr, &hLog, &hAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	ws.State = pigtail.WorkspaceState(stateStr)
	ws.RecipeID = recipe.String
	ws.ActiveDeliveryID = activeDel.String
	if portsJSON.Valid && portsJSON.String != "" {
		_ = json.Unmarshal([]byte(portsJSON.String), &ws.Ports)
	}
	if beDataJSON.Valid && beDataJSON.String != "" && beDataJSON.String != "null" {
		_ = json.Unmarshal([]byte(beDataJSON.String), &ws.BackendData)
	}
	ws.CreatedAt = time.UnixMilli(createdAt).UTC()
	ws.LastActivityAt = time.UnixMilli(lastAct).UTC()
	if snoozedAt.Valid {
		t := time.UnixMilli(snoozedAt.Int64).UTC()
		ws.SnoozedUntil = &t
	}
	if runawayAt.Valid {
		t := time.UnixMilli(runawayAt.Int64).UTC()
		ws.LastRunawayAt = &t
	}
	ws.LastRunawayReason = runawayReason.String
	if hStatus.Valid {
		ws.Hooks = &pigtail.HooksResult{Status: pigtail.HooksStatus(hStatus.String),
			Hook: hHook.String, Error: hErr.String, Log: hLog.String}
		if hAt.Valid {
			ws.Hooks.At = time.UnixMilli(hAt.Int64).UTC()
		}
	}
	return &ws, nil
}

func scanAgentSession(r rowScanner) (*pigtail.AgentSession, error) {
	var (
		sess              pigtail.AgentSession
		intent            sql.NullString
		profile, tmux     sql.NullString
		ext               sql.NullString
		stateStr          string
		startedAt, lastAt int64
	)
	err := r.Scan(&sess.ID, &sess.WorkspaceID, &sess.AgentKind, &intent, &profile, &stateStr, &tmux,
		&startedAt, &lastAt, &ext, &sess.MessageCount)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	sess.State = pigtail.AgentState(stateStr)
	sess.Intent = intent.String
	sess.ProfileID = profile.String
	sess.TmuxSessionName = tmux.String
	sess.ExternalSessionID = ext.String
	sess.StartedAt = time.UnixMilli(startedAt).UTC()
	sess.LastEventAt = time.UnixMilli(lastAt).UTC()
	return &sess, nil
}

func scanTheme(r rowScanner) (*pigtail.Theme, error) {
	var (
		th        pigtail.Theme
		codeProj  sql.NullString
		archived  int
		carried   int
		createdAt int64
	)
	err := r.Scan(&th.ID, &th.Name, &th.Kind, &th.VaultLink, &codeProj, &archived, &createdAt, &th.Scope, &th.Space, &th.Deadline, &carried)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	th.CodeProjectID = codeProj.String
	th.Archived = archived != 0
	th.Carried = carried != 0
	th.CreatedAt = time.UnixMilli(createdAt).UTC()
	return &th, nil
}

func scanSubject(r rowScanner) (*pigtail.Subject, error) {
	var (
		sub                       pigtail.Subject
		impact, effort, urgency   sql.NullInt64
		rank                      sql.NullInt64
		deps, vaultPath, codeP    sql.NullString
		createdAt, updatedAt      int64
		source                    string
		shortID, boardID, section sql.NullString
		boardCard, shortTitle     sql.NullString
	)
	err := r.Scan(&sub.ID, &sub.ThemeID, &sub.Title, &sub.Slug, &sub.Status,
		&impact, &effort, &urgency, &rank, &deps, &vaultPath, &codeP,
		&createdAt, &updatedAt, &source, &shortID, &boardID, &section, &boardCard, &shortTitle, &sub.Space, &sub.Scope)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	sub.Impact = int(impact.Int64)
	sub.Effort = int(effort.Int64)
	sub.Urgency = int(urgency.Int64)
	sub.Rank = int(rank.Int64)
	sub.VaultPath = vaultPath.String
	sub.CodeProjectID = codeP.String
	if deps.Valid && deps.String != "" {
		_ = json.Unmarshal([]byte(deps.String), &sub.Deps)
	}
	sub.CreatedAt = time.UnixMilli(createdAt).UTC()
	sub.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	sub.Source = source
	sub.ShortID = shortID.String
	sub.BoardID = boardID.String
	sub.Section = section.String
	sub.BoardCard = boardCard.String
	sub.ShortTitle = shortTitle.String
	return &sub, nil
}

func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{Valid: true, String: s}
}

func nullInt(n int) sql.NullInt64 {
	if n == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Valid: true, Int64: int64(n)}
}

// --- Themes ---------------------------------------------------------------

// CreateTheme inserts a new theme row. An empty Scope is derived from the
// vault link through the scope table; a link no root claims is the master's
// (fail-closed). The handler refuses links with neither prefix before
// reaching here; the fallback keeps direct store callers consistent.
func (s *Store) CreateTheme(th *pigtail.Theme) error {
	archived := 0
	if th.Archived {
		archived = 1
	}
	carried := 0
	if th.Carried {
		carried = 1
	}
	if th.Scope == "" {
		th.Scope = string(scope.Master())
		if sc, err := scope.FromVaultLink(th.VaultLink); err == nil {
			th.Scope = string(sc)
		}
	}
	_, err := s.db.Exec(
		`INSERT INTO themes (id, name, kind, vault_link, code_project_id, archived, created_at, scope, space, deadline, carried)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		th.ID, th.Name, th.Kind, th.VaultLink, nullStr(th.CodeProjectID), archived, th.CreatedAt.UnixMilli(), th.Scope, th.Space, th.Deadline, carried,
	)
	return err
}

const themeColumns = `id, name, kind, vault_link, code_project_id, archived, created_at, scope, space, deadline, carried`

// GetTheme returns a theme by id, or ErrNotFound.
func (s *Store) GetTheme(id string) (*pigtail.Theme, error) {
	row := s.db.QueryRow(`SELECT `+themeColumns+` FROM themes WHERE id = ?`, id)
	return scanTheme(row)
}

// ListThemes returns the themes of one scope (scope.Any = every theme), oldest
// first.
func (s *Store) ListThemes(sc scope.Scope) ([]pigtail.Theme, error) {
	rows, err := s.db.Query(
		`SELECT `+themeColumns+` FROM themes WHERE ? = '' OR scope = ? ORDER BY created_at`, string(sc), string(sc))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pigtail.Theme
	for rows.Next() {
		th, err := scanTheme(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *th)
	}
	return out, rows.Err()
}

// UpdateTheme applies a partial update to a theme's name, vault_link, space, deadline and/or carried.
// Only the provided (non-nil) fields are written; id, kind and created_at are
// immutable. Returns ErrNotFound if the theme doesn't exist. This touches only
// the DB row — it never moves vault files.
func (s *Store) UpdateTheme(id string, u pigtail.ThemeUpdate) error {
	// Load-merge then write the full row with a static statement (mirrors
	// UpdateSubject). Avoids dynamic SQL string building (gosec G202).
	cur, err := s.GetTheme(id)
	if err != nil {
		return err
	}
	if u.Name != nil {
		cur.Name = *u.Name
	}
	if u.VaultLink != nil {
		cur.VaultLink = *u.VaultLink
	}
	if u.Space != nil {
		cur.Space = *u.Space
	}
	if u.Deadline != nil {
		cur.Deadline = *u.Deadline
	}
	if u.Carried != nil {
		cur.Carried = *u.Carried
	}
	carried := 0
	if cur.Carried {
		carried = 1
	}
	res, err := s.db.Exec(
		`UPDATE themes SET name = ?, vault_link = ?, space = ?, deadline = ?, carried = ? WHERE id = ?`,
		cur.Name, cur.VaultLink, cur.Space, cur.Deadline, carried, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountSubjectsByTheme returns how many subjects are linked to a theme. Used by
// the delete guard (a theme with linked subjects cannot be removed).
func (s *Store) CountSubjectsByTheme(themeID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM subjects WHERE theme_id = ?`, themeID).Scan(&n)
	return n, err
}

// DeleteTheme removes a theme row. It does NOT cascade: the caller must enforce
// the "no linked subjects" guard first (see CountSubjectsByTheme). Returns
// ErrNotFound if the theme doesn't exist. Vault files are never touched.
func (s *Store) DeleteTheme(id string) error {
	res, err := s.db.Exec(`DELETE FROM themes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Subjects -------------------------------------------------------------

// orphanScopeSQL is the scope of a subject whose theme is gone: the master's,
// fail-closed. Inlined as a literal: table names match [a-z][a-z0-9-]{0,31},
// so the quote cannot be escaped.
func orphanScopeSQL() string { return "'" + string(scope.Master()) + "'" }

// subjectSelect reads subjects joined to their theme's scope. LEFT JOIN +
// COALESCE: a subject whose theme row is gone (foreign keys are not enforced)
// reads as the master's — the fail-closed side, hidden from every other scope.
func subjectSelect() string {
	return `SELECT s.id, s.theme_id, s.title, s.slug, s.status, s.impact, s.effort, s.urgency, s.rank, s.deps, s.vault_path, s.code_project_id, s.created_at, s.updated_at, s.source, s.short_id, s.board_id, s.section, s.board_card, s.short_title, s.space, COALESCE(t.scope, ` + orphanScopeSQL() + `)
 FROM subjects s LEFT JOIN themes t ON t.id = s.theme_id`
}

// CreateSubject inserts a new subject row. Scores/rank of 0 are stored as NULL.
func (s *Store) CreateSubject(sub *pigtail.Subject) error {
	var deps sql.NullString
	if len(sub.Deps) > 0 {
		b, err := json.Marshal(sub.Deps)
		if err != nil {
			return err
		}
		deps.Valid, deps.String = true, string(b)
	}
	if sub.Source == "" {
		sub.Source = pigtail.SourceLocal
	}
	_, err := s.db.Exec(
		`INSERT INTO subjects (id, theme_id, title, slug, status, impact, effort, urgency, rank, deps, vault_path, code_project_id, created_at, updated_at, source, short_id, board_id, section, board_card, short_title)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sub.ID, sub.ThemeID, sub.Title, sub.Slug, sub.Status,
		nullInt(sub.Impact), nullInt(sub.Effort), nullInt(sub.Urgency), nullInt(sub.Rank),
		deps, nullStr(sub.VaultPath), nullStr(sub.CodeProjectID),
		sub.CreatedAt.UnixMilli(), sub.UpdatedAt.UnixMilli(),
		sub.Source, nullStr(sub.ShortID), nullStr(sub.BoardID), nullStr(sub.Section), nullStr(sub.BoardCard),
		nullStr(sub.ShortTitle),
	)
	return err
}

// GetSubject returns a subject by id, or ErrNotFound.
func (s *Store) GetSubject(id string) (*pigtail.Subject, error) {
	row := s.db.QueryRow(subjectSelect()+` WHERE s.id = ?`, id) //nolint:gosec // G202: orphanScopeSQL is a validated identifier
	return scanSubject(row)
}

// SubjectScope returns the scope a subject derives from its theme, or
// ErrNotFound.
func (s *Store) SubjectScope(id string) (scope.Scope, error) {
	var sc string
	err := s.db.QueryRow(
		`SELECT COALESCE(t.scope, `+orphanScopeSQL()+`) FROM subjects s LEFT JOIN themes t ON t.id = s.theme_id WHERE s.id = ?`, id, //nolint:gosec // G202: orphanScopeSQL is a validated identifier
	).Scan(&sc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return scope.Scope(sc), nil
}

// SubjectFilter narrows ListSubjects. Every field is optional ("" = no filter);
// Scope uses scope.Any for "every scope".
type SubjectFilter struct {
	ThemeID string
	Status  string
	Source  string
	Scope   scope.Scope
}

// ListSubjects returns the subjects matching f, grouped by scope (each scope
// has its own rank order), then ordered by rank (unset/0 last), then
// created_at.
func (s *Store) ListSubjects(f SubjectFilter) ([]pigtail.Subject, error) {
	q := subjectSelect() + ` WHERE 1=1` //nolint:gosec // G202: orphanScopeSQL is a validated identifier
	var args []any
	if f.ThemeID != "" {
		q += ` AND s.theme_id = ?`
		args = append(args, f.ThemeID)
	}
	if f.Status != "" {
		q += ` AND s.status = ?`
		args = append(args, f.Status)
	}
	if f.Source != "" {
		q += ` AND s.source = ?`
		args = append(args, f.Source)
	}
	if f.Scope != scope.Any {
		q += ` AND COALESCE(t.scope, ` + orphanScopeSQL() + `) = ?`
		args = append(args, string(f.Scope))
	}
	q += ` ORDER BY COALESCE(t.scope, ` + orphanScopeSQL() + `), CASE WHEN s.rank IS NULL OR s.rank = 0 THEN 1 ELSE 0 END, s.rank, s.created_at`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pigtail.Subject
	for rows.Next() {
		sub, err := scanSubject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sub)
	}
	return out, rows.Err()
}

// UpdateSubject rewrites all mutable columns of an existing subject. Callers
// read-modify-write the whole struct (the CLI maps `set k=v` onto this). A
// status change writes one status log line "<old> → <new>" at UpdatedAt in
// the same transaction (D5); the same status writes none.
func (s *Store) UpdateSubject(sub *pigtail.Subject) error {
	var deps sql.NullString
	if len(sub.Deps) > 0 {
		b, err := json.Marshal(sub.Deps)
		if err != nil {
			return err
		}
		deps.Valid, deps.String = true, string(b)
	}
	source := sub.Source
	if source == "" {
		source = pigtail.SourceLocal
	}
	// One transaction: the update, its status log line (D5) and the D8 space
	// clear land together.
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var prev, oldTheme, oldSpace string
	err = tx.QueryRow(`SELECT status, theme_id, space FROM subjects WHERE id = ?`, sub.ID).Scan(&prev, &oldTheme, &oldSpace)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	res, err := tx.Exec(
		`UPDATE subjects SET theme_id=?, title=?, slug=?, status=?, impact=?, effort=?, urgency=?, rank=?, deps=?, vault_path=?, code_project_id=?, updated_at=?, source=?, short_id=?, board_id=?, section=?, board_card=?, short_title=?
		 WHERE id=?`,
		sub.ThemeID, sub.Title, sub.Slug, sub.Status,
		nullInt(sub.Impact), nullInt(sub.Effort), nullInt(sub.Urgency), nullInt(sub.Rank),
		deps, nullStr(sub.VaultPath), nullStr(sub.CodeProjectID), sub.UpdatedAt.UnixMilli(),
		source, nullStr(sub.ShortID), nullStr(sub.BoardID), nullStr(sub.Section), nullStr(sub.BoardCard),
		nullStr(sub.ShortTitle), sub.ID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	// Rewriting the same status writes nothing (D5).
	if prev != sub.Status {
		if _, err := tx.Exec(`INSERT INTO subject_log (id, subject_id, ts, kind, text) VALUES (?, ?, ?, ?, ?)`,
			newStoreID(pigtail.PrefixLog), sub.ID, sub.UpdatedAt.UnixMilli(), pigtail.LogStatus, prev+" → "+sub.Status); err != nil {
			return err
		}
	}
	// D8: a subject that changes theme loses the space it was dragged into.
	if oldTheme != sub.ThemeID && oldSpace != "" {
		if _, err := tx.Exec(`UPDATE subjects SET space = '' WHERE id = ?`, sub.ID); err != nil {
			return err
		}
		cur, err := scanSubject(tx.QueryRow(subjectSelect()+` WHERE s.id = ?`, sub.ID)) //nolint:gosec // G202: orphanScopeSQL is a validated identifier
		if err != nil {
			return err
		}
		if err := traceSubjectSpace(tx, cur, pigtail.JournalKindSubjectSpaceCleared,
			"space cleared (theme changed): "+oldSpace+" → (theme)", sub.UpdatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RankedSubject is an open subject of the ranked queue with the dependency ids
// that still block it (empty when none, never nil).
type RankedSubject struct {
	pigtail.Subject
	BlockedBy []string
}

// RankedSubjects returns the open subjects of one scope in rank order (rank
// ascending, unset ranks last, ties by created_at), each with its blocking
// dependencies by the rule of NextSubject. Closed subjects are left out.
func (s *Store) RankedSubjects(sc scope.Scope) ([]RankedSubject, error) {
	candidates, err := s.ListSubjects(SubjectFilter{Scope: sc}) // already rank-ordered
	if err != nil {
		return nil, err
	}
	// Build a status index for dependency checks.
	status := make(map[string]string, len(candidates))
	for i := range candidates {
		status[candidates[i].ID] = candidates[i].Status
	}
	out := make([]RankedSubject, 0, len(candidates))
	for i := range candidates {
		c := candidates[i]
		if pigtail.IsClosedStatus(c.Status) {
			continue
		}
		out = append(out, RankedSubject{Subject: c, BlockedBy: unsatisfiedDeps(c.Deps, status)})
	}
	return out, nil
}

// NextSubject returns the highest-ranked actionable subject of one scope whose
// blocking dependencies are all satisfied (done or captured; parked blocks).
// Actionable = status not done/parked/captured.
// Ranks are per scope, so the other scope is never a candidate. Returns
// ErrNotFound when nothing qualifies.
func (s *Store) NextSubject(sc scope.Scope) (*pigtail.Subject, error) {
	ranked, err := s.RankedSubjects(sc)
	if err != nil {
		return nil, err
	}
	for i := range ranked {
		if len(ranked[i].BlockedBy) == 0 {
			sub := ranked[i].Subject
			return &sub, nil
		}
	}
	return nil, ErrNotFound
}

// unsatisfiedDeps returns the dependency ids, in order, that block: an unknown
// id or a status pigtail.SatisfiesDeps refuses. Never nil.
func unsatisfiedDeps(deps []string, status map[string]string) []string {
	out := []string{}
	for _, id := range deps {
		if !pigtail.SatisfiesDeps(status[id]) {
			out = append(out, id)
		}
	}
	return out
}

// SubjectBlockedBy returns the dependencies of sub that do not satisfy it
// (D30), by the same rule as NextSubject; [] when none, never nil.
func (s *Store) SubjectBlockedBy(sub *pigtail.Subject) ([]string, error) {
	status := make(map[string]string, len(sub.Deps))
	for _, id := range sub.Deps {
		dep, err := s.GetSubject(id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		status[id] = dep.Status
	}
	return unsatisfiedDeps(sub.Deps, status), nil
}

// --- Subject phases -------------------------------------------------------

// CreatePhase inserts a new phase row.
func (s *Store) CreatePhase(p *pigtail.SubjectPhase) error {
	_, err := s.db.Exec(
		`INSERT INTO subject_phases (id, subject_id, kind, state, workspace_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		p.ID, p.SubjectID, p.Kind, p.State, nullStr(p.WorkspaceID), p.CreatedAt.UnixMilli(),
	)
	return err
}

// UpdatePhase sets a phase's state and its linked workspace. workspaceID is
// written verbatim: pass the new id to link, the current id to keep it, or ""
// to clear the link.
func (s *Store) UpdatePhase(id, state, workspaceID string) error {
	res, err := s.db.Exec(
		`UPDATE subject_phases SET state = ?, workspace_id = ? WHERE id = ?`,
		state, nullStr(workspaceID), id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetPhase returns a phase by id, or ErrNotFound.
func (s *Store) GetPhase(id string) (*pigtail.SubjectPhase, error) {
	row := s.db.QueryRow(
		`SELECT id, subject_id, kind, state, workspace_id, created_at FROM subject_phases WHERE id = ?`, id)
	return scanPhase(row)
}

// ListPhasesBySubject returns a subject's phases, oldest first.
func (s *Store) ListPhasesBySubject(subjectID string) ([]pigtail.SubjectPhase, error) {
	rows, err := s.db.Query(
		`SELECT id, subject_id, kind, state, workspace_id, created_at
		 FROM subject_phases WHERE subject_id = ? ORDER BY created_at`, subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pigtail.SubjectPhase
	for rows.Next() {
		p, err := scanPhase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func scanPhase(r rowScanner) (*pigtail.SubjectPhase, error) {
	var (
		p         pigtail.SubjectPhase
		ws        sql.NullString
		createdAt int64
	)
	err := r.Scan(&p.ID, &p.SubjectID, &p.Kind, &p.State, &ws, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.WorkspaceID = ws.String
	p.CreatedAt = time.UnixMilli(createdAt).UTC()
	return &p, nil
}

// isReusableState reports whether a workspace in this state is a candidate for
// reuse — i.e. its work is still in flight. Terminal states (shipped/archived)
// mean the work is done; reusing such a worktree would resurrect finished work,
// so a fresh one should be created instead. Conservative on purpose: only the
// clearly-done states are terminal; everything else (planning, building,
// review, delivery, needs_cleanup, …) stays reusable.
func isReusableState(state pigtail.WorkspaceState) bool {
	switch state {
	case pigtail.StateShipped, pigtail.StateArchived:
		return false
	default:
		return true
	}
}

// WorkspaceForSubject returns the first non-terminal workspace linked to any
// phase of the subject (first phase carrying a workspace_id whose workspace row
// still exists AND is in a reusable, non-terminal state), or ErrNotFound.
//
// Phases pointing to a missing workspace row (dangling link) or to a terminal
// workspace (shipped/archived — work done, reuse would duplicate it) are
// skipped so a later phase may still surface an active worktree. Disk-existence
// is left to the caller — the store stays pure.
func (s *Store) WorkspaceForSubject(subjectID string) (*pigtail.Workspace, error) {
	phases, err := s.ListPhasesBySubject(subjectID)
	if err != nil {
		return nil, err
	}
	for _, p := range phases {
		if p.WorkspaceID == "" {
			continue
		}
		ws, err := s.GetWorkspace(p.WorkspaceID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		if !isReusableState(ws.State) {
			continue
		}
		return ws, nil
	}
	return nil, ErrNotFound
}

// --- Subject log ----------------------------------------------------------

// AppendLog inserts a log entry.
func (s *Store) AppendLog(e *pigtail.SubjectLogEntry) error {
	_, err := s.db.Exec(
		`INSERT INTO subject_log (id, subject_id, ts, kind, text) VALUES (?, ?, ?, ?, ?)`,
		e.ID, e.SubjectID, e.Timestamp.UnixMilli(), e.Kind, e.Text,
	)
	return err
}

// AppendLogOnce inserts a log entry unless its id already exists (the first
// writer wins); created reports whether this call inserted it.
func (s *Store) AppendLogOnce(e *pigtail.SubjectLogEntry) (created bool, err error) {
	res, err := s.db.Exec(
		`INSERT OR IGNORE INTO subject_log (id, subject_id, ts, kind, text) VALUES (?, ?, ?, ?, ?)`,
		e.ID, e.SubjectID, e.Timestamp.UnixMilli(), e.Kind, e.Text,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// LatestLogOfKind returns the subject's newest log entry of exactly this kind,
// nil when there is none.
func (s *Store) LatestLogOfKind(subjectID, kind string) (*pigtail.SubjectLogEntry, error) {
	var e pigtail.SubjectLogEntry
	var ts int64
	err := s.db.QueryRow(
		`SELECT id, subject_id, ts, kind, text FROM subject_log WHERE subject_id = ? AND kind = ? ORDER BY ts DESC, rowid DESC LIMIT 1`,
		subjectID, kind,
	).Scan(&e.ID, &e.SubjectID, &ts, &e.Kind, &e.Text)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.Timestamp = time.UnixMilli(ts).UTC()
	return &e, nil
}

// LogExists tells whether a log entry with this id exists (any subject).
func (s *Store) LogExists(id string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM subject_log WHERE id = ?`, id).Scan(&n)
	return n > 0, err
}

// ListLog returns a subject's log entries newest-first. A zero `since` returns
// all entries; otherwise only entries with ts >= since.
func (s *Store) ListLog(subjectID string, since time.Time) ([]pigtail.SubjectLogEntry, error) {
	q := `SELECT id, subject_id, ts, kind, text FROM subject_log WHERE subject_id = ?`
	args := []any{subjectID}
	if !since.IsZero() {
		q += ` AND ts >= ?`
		args = append(args, since.UnixMilli())
	}
	q += ` ORDER BY ts DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pigtail.SubjectLogEntry
	for rows.Next() {
		e, err := scanLogEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func scanLogEntry(r rowScanner) (*pigtail.SubjectLogEntry, error) {
	var (
		e  pigtail.SubjectLogEntry
		ts int64
	)
	err := r.Scan(&e.ID, &e.SubjectID, &ts, &e.Kind, &e.Text)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	e.Timestamp = time.UnixMilli(ts).UTC()
	return &e, nil
}

// satisfiedStatusArgs returns the statuses pigtail.SatisfiesDeps accepts, as
// SQL parameters and their placeholders: the one rule (D30) is never written
// as a SQL literal.
func satisfiedStatusArgs() (placeholders string, args []any) {
	var ph []string
	for _, st := range pigtail.SubjectStatuses {
		if pigtail.SatisfiesDeps(st) {
			ph = append(ph, "?")
			args = append(args, st)
		}
	}
	return strings.Join(ph, ","), args
}

// SubjectDepPath returns the shortest path of dependencies from `from` to
// `to` (both ends included), nil when there is none. from == to returns nil: a
// self dependency is refused before any path check. The walk crosses every
// scope and never revisits a node of its own path (bounded at depth 64); an
// unknown id can end a path but has no deps to follow.
func (s *Store) SubjectDepPath(from, to string) ([]string, error) {
	if from == to {
		return nil, nil
	}
	var path string
	err := s.db.QueryRow(`
WITH RECURSIVE walk(id, path, depth) AS (
  SELECT ?1, ','||?1||',', 0
  UNION ALL
  SELECT j.value, w.path||j.value||',', w.depth+1
  FROM walk w JOIN subjects s ON s.id = w.id, json_each(CASE WHEN json_valid(s.deps) THEN s.deps ELSE '[]' END) j
  WHERE w.depth < 64 AND instr(w.path, ','||j.value||',') = 0
)
SELECT path FROM walk WHERE id = ?2 ORDER BY depth LIMIT 1`, from, to).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.Trim(path, ","), ","), nil
}

// SubjectDepChains returns, sorted by id, deduplicated, never nil and never
// listing sub itself:
//   - blockedByAll: every subject that blocks sub transitively — the closure
//     of deps walked through unsatisfied subjects of sub's scope only. An
//     unknown id or a subject of another scope counts when unsatisfied and is
//     not walked.
//   - blocks: the subjects of sub's scope, not satisfied by status, whose deps
//     name sub directly; empty when sub itself satisfies deps.
//   - blocksAll: the same, transitively through unsatisfied subjects.
func (s *Store) SubjectDepChains(sub *pigtail.Subject) (blockedByAll, blocks, blocksAll []string, err error) {
	ph, sat := satisfiedStatusArgs()
	seed, err := json.Marshal(append([]string{}, sub.Deps...))
	if err != nil {
		return nil, nil, nil, err
	}
	scopeExpr := `COALESCE(t.scope, ` + orphanScopeSQL() + `)`

	// Blockers: seeded by sub's own deps; a node is expanded only when it is a
	// known, unsatisfied subject of sub's scope.
	qBy := `
WITH RECURSIVE walk(id, path, depth) AS (
  SELECT j.value, ','||?1||','||j.value||',', 1 FROM json_each(?2) j
  UNION ALL
  SELECT j.value, w.path||j.value||',', w.depth+1
  FROM walk w
  JOIN subjects s ON s.id = w.id
  LEFT JOIN themes t ON t.id = s.theme_id, json_each(CASE WHEN json_valid(s.deps) THEN s.deps ELSE '[]' END) j
  WHERE w.depth < 64 AND instr(w.path, ','||j.value||',') = 0
    AND s.status NOT IN (` + ph + `) AND ` + scopeExpr + ` = ?3
)
SELECT DISTINCT w.id FROM walk w LEFT JOIN subjects s ON s.id = w.id
WHERE w.id <> ?1 AND (s.id IS NULL OR s.status NOT IN (` + ph + `))
ORDER BY w.id`
	args := []any{sub.ID, string(seed), sub.Scope}
	args = append(args, sat...)
	args = append(args, sat...)
	if blockedByAll, err = s.queryIDs(qBy, args...); err != nil { //nolint:gosec // G202: placeholders and the orphan scope are generated, never user input
		return nil, nil, nil, err
	}

	blocks, blocksAll = []string{}, []string{}
	if pigtail.SatisfiesDeps(sub.Status) {
		return blockedByAll, blocks, blocksAll, nil
	}
	// Dependents: seeded by the subjects naming sub; a node is kept (and
	// expanded) only when it is unsatisfied and in sub's scope.
	qUp := `
WITH RECURSIVE walk(id, path, depth) AS (
  SELECT s.id, ','||?1||','||s.id||',', 1
  FROM subjects s LEFT JOIN themes t ON t.id = s.theme_id, json_each(CASE WHEN json_valid(s.deps) THEN s.deps ELSE '[]' END) j
  WHERE j.value = ?1 AND s.id <> ?1 AND s.status NOT IN (` + ph + `) AND ` + scopeExpr + ` = ?2
  UNION ALL
  SELECT s.id, w.path||s.id||',', w.depth+1
  FROM walk w, subjects s LEFT JOIN themes t ON t.id = s.theme_id, json_each(CASE WHEN json_valid(s.deps) THEN s.deps ELSE '[]' END) j
  WHERE w.depth < 64 AND j.value = w.id AND instr(w.path, ','||s.id||',') = 0
    AND s.status NOT IN (` + ph + `) AND ` + scopeExpr + ` = ?2
)
SELECT DISTINCT id FROM walk %s ORDER BY id`
	upArgs := []any{sub.ID, sub.Scope}
	upArgs = append(upArgs, sat...)
	upArgs = append(upArgs, sat...)
	if blocksAll, err = s.queryIDs(fmt.Sprintf(qUp, ""), upArgs...); err != nil { //nolint:gosec // G202: see above
		return nil, nil, nil, err
	}
	if blocks, err = s.queryIDs(fmt.Sprintf(qUp, "WHERE depth = 1"), upArgs...); err != nil { //nolint:gosec // G202: see above
		return nil, nil, nil, err
	}
	return blockedByAll, blocks, blocksAll, nil
}

// queryIDs runs a query returning one id column and collects it; never nil.
func (s *Store) queryIDs(q string, args ...any) ([]string, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
