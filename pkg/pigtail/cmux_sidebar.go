package pigtail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// EventCmuxSidebarChanged is published when the sidebar loop's wanted state
// of a workspace changes (attention, waiting count, bound subject). Its data
// is {cmux_workspace_id, attention, wait_type, wait_reason, waiting_count,
// servers, subject_id} — never a title. The event carries Scope = the bound
// subject's scope (globex when none), or perso when the wait reason was told by
// a perso session; GET /events filters on it.
const EventCmuxSidebarChanged EventType = "cmux.sidebar_changed"

// EventCmuxCheckNotice (D2, sub_x000000000000037): pigtaild wrote the
// end-of-pass notice of a cmux launch; the Pigtail app re-reads GET
// /cmux/check/notice. Carries no payload.
const EventCmuxCheckNotice EventType = "cmux.check_notice"

// CmuxSidebarSubject is the bound subject as GET /cmux/sidebar shows it, or
// nil for a caller who may not read its scope.
type CmuxSidebarSubject struct {
	ID         string `json:"id"`
	ShortTitle string `json:"short_title"`
	Scope      string `json:"scope"`
	Status     string `json:"status,omitempty"` // the subject's status (D28), never its phase
	Rank       int    `json:"rank,omitempty"`
}

// CmuxSidebarServer is one dev server of a workspace, as its pigtail-srv
// pill shows it. State is "up", "stopped", "dead" or "conflict"; twins sharing a port
// (one process) are one entry named "dev+web".
type CmuxSidebarServer struct {
	Name  string `json:"name"`
	Port  int    `json:"port"`
	State string `json:"state"`
}

// CmuxSidebarWorkspace is the loop's view of one live workspace.
type CmuxSidebarWorkspace struct {
	CmuxWorkspaceID string `json:"cmux_workspace_id"`
	WindowID        string `json:"window_id"`
	Attention       string `json:"attention"` // "waiting" | "dead" | ""
	// WaitType is the winning state, most severe first (permission, blocked,
	// limit, login, question, arbitrage, untyped, respawn, api_error,
	// your_turn, review, done, mac, working, wrapped); empty when nobody told or when attention is dead. WaitReason
	// is the reason of the one session of the winner's colour (empty when
	// several), hidden from a caller who may not read WaitScope.
	WaitType   string `json:"wait_type,omitempty"`
	WaitReason string `json:"wait_reason,omitempty"`
	WaitScope  string `json:"-"`
	// WaitFromSession: WaitReason was told by a session, WaitScope being its
	// scope; false: pigtail's own reason (trust folder, respawn).
	WaitFromSession bool   `json:"-"`
	WaitingCount    int    `json:"waiting_count,omitempty"`
	TitleOwner      string `json:"title_owner,omitempty"` // bound workspaces only; always "pigtail" since D5 — the bound marker
	// Title is the title the sidebar shows (status glyph removed): for a
	// bound workspace the subject's short title pigtail keeps on it (D5).
	// DisplayName is the session's one name (pigtail.SessionDisplayName),
	// filled per caller by GET /cmux/sidebar. Both are empty for a caller
	// who may not read the bound subject, or a cell of an unbound workspace.
	Title       string              `json:"title,omitempty"`
	DisplayName string              `json:"display_name,omitempty"`
	Servers     []CmuxSidebarServer `json:"servers"`
	Subject     *CmuxSidebarSubject `json:"subject"`
	// Cells are the cells of the workspace's live sessions, in the order the
	// description shows them (D1); a caller who may not read a cell's scope
	// does not get it. CellScopes runs parallel to Cells, never serialised.
	Cells      []string `json:"cells,omitempty"`
	CellScopes []string `json:"-"`
}

// CmuxSidebarState is the answer of GET and POST /cmux/sidebar.
type CmuxSidebarState struct {
	Enabled bool `json:"enabled"`
	// ClaudeCodeIdleFix reports the temporary claude_code patch (pigtaild
	// -cmux-claude-code-fix): a native claude_code row stuck on Running is
	// put back to Idle while every session of the workspace is idle.
	ClaudeCodeIdleFix bool       `json:"claude_code_idle_fix"`
	DisabledByPanic   bool       `json:"disabled_by_panic,omitempty"`
	CmuxAvailable     bool       `json:"cmux_available"`
	LastReconcileAt   *time.Time `json:"last_reconcile_at,omitempty"`
	ErrorsLastHour    int        `json:"errors_last_hour"`
	// Windows, when set, restricts the loop to these cmux windows
	// (pigtaild --cmux-sidebar-window); empty means every window.
	Windows []string `json:"windows,omitempty"`
	// Folders reports the folder arrangement (pigtaild --cmux-folders, acting
	// only while the loop is on); FoldersNote says why it is idle or stopped.
	Folders     bool   `json:"folders"`
	FoldersNote string `json:"folders_note,omitempty"`
	// FoldersWindows are the cmux windows the folders arrange, in listing
	// order: every allowed one, each on its own (D8 of sub_x000000000000007;
	// D32 replaces the main window of D2 of sub_x000000000000019); empty while
	// the loop or the folders are off, or no window is listed.
	FoldersWindows []string `json:"folders_windows,omitempty"`
	// FoldersWait is, per window, the folder round owed and not yet applied
	// (D3 of sub_x000000000000019): since when, and why; one entry per window
	// that waits, in FoldersWindows order, the loop's own wait (no window,
	// Window "") first. Empty when nothing is owed, or while the loop or the
	// folders are off. Read-only.
	FoldersWait []CmuxFoldersWait      `json:"folders_wait,omitempty"`
	Warnings    []string               `json:"warnings"`
	Workspaces  []CmuxSidebarWorkspace `json:"workspaces"`
}

// CmuxSidebarToggle is the body of POST /cmux/sidebar.
type CmuxSidebarToggle struct {
	Enabled bool `json:"enabled"`
}

// CmuxSidebar reads the sidebar loop's state (GET /cmux/sidebar).
func (c *Client) CmuxSidebar(ctx context.Context) (*CmuxSidebarState, error) {
	var out CmuxSidebarState
	if _, err := c.do(ctx, "GET", "/cmux/sidebar", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetCmuxSidebar switches the sidebar loop on or off until the daemon
// restarts (POST /cmux/sidebar). Off clears pigtail's pills and gives the
// titles and descriptions it posed back.
func (c *Client) SetCmuxSidebar(ctx context.Context, enabled bool) (*CmuxSidebarState, error) {
	var out CmuxSidebarState
	if _, err := c.do(ctx, "POST", "/cmux/sidebar", CmuxSidebarToggle{Enabled: enabled}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RetitleCmuxWorkspace hands a bound workspace's description back to pigtail
// after a manual edit (POST /cmux-workspaces/:wsid/retitle). The title is
// always pigtail's since D5.
func (c *Client) RetitleCmuxWorkspace(ctx context.Context, workspaceID string) (*SubjectCmuxBinding, error) {
	var out SubjectCmuxBinding
	if _, err := c.do(ctx, "POST", "/cmux-workspaces/"+url.PathEscape(workspaceID)+"/retitle", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CmuxTrustPendingRequest is the body of POST
// /cmux-workspaces/{wsid}/trust-pending: the pid of a Claude that
// `pigtail session spawn` saw stopped on "trust this folder".
type CmuxTrustPendingRequest struct {
	PID int `json:"pid"`
}

// MarkCmuxTrustPending tells the sidebar loop that a spawned session waits
// on Claude's "trust this folder" dialog in a workspace (D14): the loop shows
// "Blocked · trust folder" there until the session registers, its Claude
// exits, or the workspace closes.
func (c *Client) MarkCmuxTrustPending(ctx context.Context, workspaceID string, pid int) error {
	_, err := c.do(ctx, "POST", "/cmux-workspaces/"+url.PathEscape(workspaceID)+"/trust-pending", CmuxTrustPendingRequest{PID: pid}, nil)
	return err
}

// Respawn states, as POST /cmux-workspaces/{wsid}/respawn takes them (D16).
const (
	CmuxRespawnWaiting   = "waiting"   // held by its user: Wait says why
	CmuxRespawnAbandoned = "abandoned" // gave up at the timeout, nothing closed
	CmuxRespawnFailed    = "failed"    // the old session closed, the new one not verified
)

// What a waiting respawn waits on (CmuxRespawnRequest.Wait): a closed list
// the daemon turns into the pill's reason.
const (
	CmuxRespawnWaitDraft      = "draft"      // a draft in the input
	CmuxRespawnWaitBackground = "background" // background work
	CmuxRespawnWaitScreen     = "screen"     // the tab's screen cannot be read
)

// CmuxRespawnWaits is the closed list of CmuxRespawnRequest.Wait.
var CmuxRespawnWaits = []string{CmuxRespawnWaitDraft, CmuxRespawnWaitBackground, CmuxRespawnWaitScreen}

// CmuxRespawnRequest is the body of POST /cmux-workspaces/{wsid}/respawn:
// the state of a `pigtail session respawn` run on its workspace. SurfaceID
// (a UUID) is the run's surface; Wait and PID (the worker, alive while it
// waits) go with State waiting only.
//
// A final state carries its run (D2, subject « abandon respawn visible »):
// RunID (the spawns.jsonl record id), SubjectID, SessionID (the session the
// run was to close, its current id) and Detail (the record's detail). Without
// RunID — an older worker — the daemon only marks the pill.
type CmuxRespawnRequest struct {
	State     string `json:"state"`
	Wait      string `json:"wait,omitempty"`
	SurfaceID string `json:"surface_id,omitempty"`
	PID       int    `json:"pid,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	SubjectID string `json:"subject_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// What the daemon did about a final respawn state carrying a run (D1): each
// field is CmuxRespawnNoticeOK, CmuxRespawnNoticeDuplicate (done already for
// this run), "skipped: <why>" or "failed: <error>"; the wait field is also
// CmuxRespawnNoticeNone for an abandoned run.
const (
	CmuxRespawnNoticeOK        = "ok"
	CmuxRespawnNoticeDuplicate = "duplicate"
	// CmuxRespawnNoticeNone: no wait by design (an abandoned run poses none).
	CmuxRespawnNoticeNone = "none"
)

// CmuxRespawnNotice answers POST /cmux-workspaces/{wsid}/respawn when the
// body carries run_id: the journal line, the session wait, the message — and
// the sidebar pill, apart: a daemon without the sidebar loop (the default)
// still tells the session, and answers Pill "skipped: …".
type CmuxRespawnNotice struct {
	Journal string `json:"journal"`
	Wait    string `json:"wait"`
	Notice  string `json:"notice"`
	Pill    string `json:"pill,omitempty"`
}

// PillProblem is Pill when it is neither ok nor empty (an older daemon), else "".
func (n CmuxRespawnNotice) PillProblem() string {
	if n.Pill == "" || n.Pill == CmuxRespawnNoticeOK {
		return ""
	}
	return n.Pill
}

// Problems lists the notice parts (journal, wait, message) that are neither
// ok nor duplicate, "" when none: what the worker writes into its record. The
// pill is not one of them (PillProblem).
func (n CmuxRespawnNotice) Problems() string {
	var out []string
	for _, f := range []struct {
		k, v string
		none bool
	}{{"journal", n.Journal, false}, {"wait", n.Wait, true}, {"notice", n.Notice, false}} {
		if f.v != CmuxRespawnNoticeOK && f.v != CmuxRespawnNoticeDuplicate && (!f.none || f.v != CmuxRespawnNoticeNone) {
			out = append(out, f.k+" "+f.v)
		}
	}
	return strings.Join(out, "; ")
}

// EndCmuxRespawn posts a final respawn state with its run and returns what
// the daemon did about it; nil (no error) when the daemon answered 204 — one
// older than the notice.
func (c *Client) EndCmuxRespawn(ctx context.Context, workspaceID string, req CmuxRespawnRequest) (*CmuxRespawnNotice, error) {
	raw, err := c.do(ctx, "POST", "/cmux-workspaces/"+url.PathEscape(workspaceID)+"/respawn", req, nil)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var env struct {
		Data CmuxRespawnNotice `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode respawn notice: %w", err)
	}
	return &env.Data, nil
}

// MarkCmuxRespawn tells the sidebar loop the state of a respawn run on its
// workspace (D16): waiting shows "Respawn · <reason>" (orange) while the
// worker runs, abandoned the same orange "Respawn · abandoned", failed a red
// "Blocked · respawn failed"; abandoned drops at the next prompt on the run's
// surface (read in its transcript), failed only at a Claude relaunched there.
// It replaces the workspace's last one.
func (c *Client) MarkCmuxRespawn(ctx context.Context, workspaceID string, req CmuxRespawnRequest) error {
	_, err := c.do(ctx, "POST", "/cmux-workspaces/"+url.PathEscape(workspaceID)+"/respawn", req, nil)
	return err
}

// ClearCmuxRespawn drops the workspace's respawn state (DELETE
// /cmux-workspaces/{wsid}/respawn).
func (c *Client) ClearCmuxRespawn(ctx context.Context, workspaceID string) error {
	_, err := c.do(ctx, "DELETE", "/cmux-workspaces/"+url.PathEscape(workspaceID)+"/respawn", nil, nil)
	return err
}

// CmuxFoldersWait is why a folder round waits (D3 of sub_x000000000000019).
// Reason is one of key (cmux is the key window, or its state is unknown),
// settle (the pause after a round), focus-poll (the focus is read at most
// every 2 s), selected-collapsed (the selected workspace waits to enter a
// collapsed folder), listing-down, read-error, write-error, stopped (a created
// folder read back wrong) and no-window (no allowed window listed). Since is
// the start of the wait; a later reason does not move it. Window is the cmux
// window whose round waits (D32 of sub_x000000000000007), empty for
// no-window: a cause shared by the pass (key, focus-poll, listing-down, a
// store read-error, stopped) is set on every window it holds, a window's own
// (settle, selected-collapsed, its own read-error or write-error) on that
// window only.
type CmuxFoldersWait struct {
	Window string    `json:"window,omitempty"`
	Since  time.Time `json:"since"`
	Reason string    `json:"reason"`
}

// ErrCmuxFoldersIdle is the loop's refusal to toggle the folders (D12): the
// sidebar loop or the folder arrangement is off, or no allowed window was
// listed lately. Nothing was written.
var ErrCmuxFoldersIdle = errors.New("folders idle")

// CmuxFoldersToggle is the answer of POST /cmux/folders/toggle (D12):
// Collapsed is the state the toggle left the folders in, Folders how many it
// moved.
type CmuxFoldersToggle struct {
	Collapsed bool `json:"collapsed"`
	Folders   int  `json:"folders"`
}

// ToggleCmuxFolders folds every folder, or unfolds them all when none is
// open (POST /cmux/folders/toggle, D12). A daemon whose folders are idle
// answers 409 "folders idle".
func (c *Client) ToggleCmuxFolders(ctx context.Context) (*CmuxFoldersToggle, error) {
	var out CmuxFoldersToggle
	if _, err := c.do(ctx, "POST", "/cmux/folders/toggle", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
