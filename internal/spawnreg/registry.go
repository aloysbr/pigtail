// Package spawnreg records the sessions `pigtail session spawn` opened, so the
// next spawn can count the ones still live and the launches of the last minutes.
//
// It is a plain append-only file in pigtail's state dir — the same root as
// internal/devserver's markers ($XDG_STATE_HOME/pigtail) — readable with the
// daemon down. Liveness is never stored: the caller passes the set of
// workspaces that have a live Claude right now.
package spawnreg

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type Entry struct {
	TS           time.Time `json:"ts"`
	SubjectID    string    `json:"subject_id"`
	WorkspaceID  string    `json:"workspace_id"` // cmux UUID, upper case
	WorkspaceRef string    `json:"workspace_ref"`
	Cell         string    `json:"cell"`
	// FreshCell is the cell a respawn's fresh session runs on when the
	// account rotation picked another one than Cell (the replaced session's);
	// "" = Cell. The status filters on Cell.
	FreshCell string `json:"fresh_cell,omitempty"`
	Cwd       string `json:"cwd"`
	Prompt    string `json:"prompt"` // the exact first message handed to the session
	Mode      string `json:"permission_mode"`
	// Result: a launch (Kind "" or launch-*): pending (reserve, launch-ref) |
	// armed (launch-armed) | launched (the spawn entry, Kind "") | failed
	// (launch-result); legacy spawn lines: launched | unverified;
	// respawn: armed | respawned | failed | abandoned.
	Result string `json:"result"`
	// RelayFrom is the workspace whose spawned session this one takes over from
	// (`session spawn --relay`, D3·A); "" for a plain spawn or a relay that
	// passed nothing.
	RelayFrom string `json:"relay_from,omitempty"`

	// Respawn records (`pigtail session respawn`): every record a run appends
	// repeats ID, SubjectID, WorkspaceID, Cell, SurfaceID, SurfaceRef,
	// WorkerPID, Mode and Prompt, so the latest one per ID carries every key
	// the status filters read. omitempty keeps spawn lines byte-identical.
	Kind         string `json:"kind,omitempty"`           // "" = spawn, "respawn"
	ID           string `json:"id,omitempty"`             // respawn run id (wk-based, unique)
	SurfaceID    string `json:"surface_id,omitempty"`     // CMUX_SURFACE_ID of the replaced session
	SurfaceRef   string `json:"surface_ref,omitempty"`    // surface:N
	WorkerPID    int    `json:"worker_pid,omitempty"`     // the detached worker
	OldSessionID string `json:"old_session_id,omitempty"` // the session closed
	PID          int    `json:"pid,omitempty"`            // the new Claude pid (respawned)
	NewSessionID string `json:"new_session_id,omitempty"` // the new session's id (respawned)
	Relaunch     string `json:"relaunch,omitempty"`       // "launcher" | "shell" (D1); "" on records written before it
	Model        string `json:"model,omitempty"`          // the fresh session's model alias; "" = the cell's default
	Detail       string `json:"detail,omitempty"`         // reason of a failed/abandoned run, warnings

	// Margin is a launch margin decision (Kind margin, sub_x000000000000011):
	// switched <req>-><final>, forced <cell>, unmeasured <why>,
	// refused <scope|cell> frees <RFC3339|unknown>, refused <cell> launcher-proto.
	// A margin entry is its own line: it opens no workspace and is never a launch.
	Margin string `json:"margin,omitempty"`

	// Launch records (`pigtail session spawn`, sub_x000000000000015): one ID
	// (ln_<hex>) per launch, carried by every record of it — reserve,
	// launch-ref, launch-armed, then launch-result (failed) or the spawn entry
	// (Kind "", launched). Like the respawn records, every record repeats
	// SubjectID from the reserve on, and WorkspaceID/WorkspaceRef once the tab
	// is known, so the latest record per ID is enough for the burst count
	// (Launches) and the duplicate guard.
	OwnerPID int    `json:"owner_pid,omitempty"` // the process that wrote the reserve (WithReservation only)
	Origin   string `json:"origin,omitempty"`    // cli | api; "" = cli
}

// KindRespawn marks a record of `pigtail session respawn`.
const KindRespawn = "respawn"

// KindMargin marks a launch margin decision (internal/launchmargin.Record):
// never a launch (Launches, Pending, Outcome: not a launch kind), never a live
// workspace (Live: not a spawn entry).
const KindMargin = "margin"

// Launch kinds: the records of one spawn launch before its spawn entry.
const (
	KindLaunchReserve = "launch-reserve" // written by WithReservation only
	KindLaunchRef     = "launch-ref"     // the tab exists: WorkspaceID, WorkspaceRef
	KindLaunchArmed   = "launch-armed"   // the detached watcher runs
	KindLaunchResult  = "launch-result"  // the launch ended failed (Detail)
)

// Launch results.
const (
	ResultPending  = "pending"
	ResultArmed    = "armed"
	ResultLaunched = "launched"
	ResultFailed   = "failed"
)

// The launch bounds, owned here so every launcher (CLI, daemon) reads the same.
const (
	// ConfirmTimeout: how long after the tab's birth (launch-ref) a Claude
	// process may take to appear before the launch is failed.
	ConfirmTimeout = 5 * time.Minute
	// PendingWindow: how long after the launch-ref an unconfirmed launch counts
	// as in progress; it always covers ConfirmTimeout.
	PendingWindow = ConfirmTimeout + time.Minute
	// ReserveMargin is added to the longest create before a tab exists.
	ReserveMargin = time.Minute
	// ReserveCap: how long a reserve without launch-ref stays in progress while
	// its owner lives — the worktree create's own budget (15 min, the CLI's
	// newWorkspaceTimeout, pinned by a cmd/pigtail test) plus the margin.
	ReserveCap = 15*time.Minute + ReserveMargin
)

// NewLaunchID returns a fresh launch id, ln_<hex>.
func NewLaunchID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("ln_%x", time.Now().UnixNano())
	}
	return "ln_" + hex.EncodeToString(b[:])
}

// PendingOpts bounds a launch in progress. Zero Window/Cap take PendingWindow
// and ReserveCap; a nil Alive is signal 0.
type PendingOpts struct {
	Window, Cap time.Duration
	Alive       func(pid int) bool
	// ExcludeWorkspaceID: a launch whose launch-ref names this tab is not in
	// progress for Pending (the caller's own tab).
	ExcludeWorkspaceID string
}

func (o PendingOpts) withDefaults() PendingOpts {
	if o.Window == 0 {
		o.Window = PendingWindow
	}
	if o.Cap == 0 {
		o.Cap = ReserveCap
	}
	if o.Alive == nil {
		o.Alive = signalAlive
	}
	return o
}

func signalAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func isLaunchKind(k string) bool {
	switch k {
	case "", KindLaunchReserve, KindLaunchRef, KindLaunchArmed, KindLaunchResult:
		return true
	}
	return false
}

// launch is every record of one launch ID, in file order.
type launch struct {
	id      string
	first   int    // index of its first record
	reserve *Entry // nil for a launch without a reserve (legacy)
	ref     *Entry // the latest launch-ref
	last    Entry
	oldest  Entry // the record with the oldest TS among the spawn entry and the reserve
	hasCnt  bool  // a spawn entry or a reserve: the burst counts it
}

func groupLaunches(entries []Entry) []*launch {
	byID := map[string]*launch{}
	var out []*launch
	for i := range entries {
		e := entries[i]
		if e.ID == "" || !isLaunchKind(e.Kind) {
			continue
		}
		l, ok := byID[e.ID]
		if !ok {
			l = &launch{id: e.ID, first: i}
			byID[e.ID] = l
			out = append(out, l)
		}
		switch e.Kind {
		case KindLaunchReserve:
			if l.reserve == nil {
				r := e
				l.reserve = &r
			}
		case KindLaunchRef:
			r := e
			l.ref = &r
		}
		if e.Kind == "" || e.Kind == KindLaunchReserve {
			if !l.hasCnt || e.TS.Before(l.oldest.TS) {
				l.oldest = e
			}
			l.hasCnt = true
		}
		l.last = e
	}
	return out
}

// open: the latest record is still a reserve, a launch-ref or an armed one.
func (l *launch) open() bool {
	switch l.last.Kind {
	case KindLaunchReserve, KindLaunchRef, KindLaunchArmed:
		return true
	}
	return false
}

// closedAtRead is why an open launch has expired at now ("" = in progress).
func (l *launch) closedAtRead(now time.Time, o PendingOpts) string {
	if l.ref != nil {
		if l.ref.TS.Add(o.Window).After(now) {
			return ""
		}
		return fmt.Sprintf("not confirmed within %s of the tab's birth", o.Window)
	}
	if l.reserve == nil {
		return "no reservation"
	}
	if !o.Alive(l.reserve.OwnerPID) {
		return "owner exited before creating the tab"
	}
	if !l.reserve.TS.Add(o.Cap).After(now) {
		return fmt.Sprintf("reservation cap passed (%s without a tab)", o.Cap)
	}
	return ""
}

func (l *launch) subject() string {
	if l.reserve != nil && l.reserve.SubjectID != "" {
		return l.reserve.SubjectID
	}
	return l.last.SubjectID
}

// view is the reserve completed with the tab of the launch-ref.
func (l *launch) view() Entry {
	e := l.last
	if l.reserve != nil {
		e = *l.reserve
	}
	if l.ref != nil {
		e.WorkspaceID, e.WorkspaceRef = l.ref.WorkspaceID, l.ref.WorkspaceRef
	}
	return e
}

// Pending returns the subject's most recent launch still in progress: its
// latest state pending or armed and, with a launch-ref, that ref younger than
// Window; without one, its owner alive and its reserve younger than Cap. Any
// other open launch is closed failed at read (Outcome says why; nothing is
// written). The Entry is the reserve with the tab of the launch-ref. No
// subject → never pending.
func Pending(entries []Entry, subjectID string, now time.Time, o PendingOpts) (Entry, bool) {
	if subjectID == "" {
		return Entry{}, false
	}
	o = o.withDefaults()
	var best Entry
	found := false
	for _, l := range groupLaunches(entries) {
		if l.subject() != subjectID || !l.open() || l.closedAtRead(now, o) != "" {
			continue
		}
		if o.ExcludeWorkspaceID != "" && l.ref != nil && strings.EqualFold(l.ref.WorkspaceID, o.ExcludeWorkspaceID) {
			continue
		}
		v := l.view()
		if !found || !v.TS.Before(best.TS) {
			best, found = v, true
		}
	}
	return best, found
}

// Outcome returns how launch id ended: its spawn entry (launched), its
// launch-result (failed), or a failed record built at read when it expired
// (Detail = the cause; nothing written). false: unknown, or still in progress.
func Outcome(entries []Entry, id string, now time.Time, o PendingOpts) (Entry, bool) {
	o = o.withDefaults()
	for _, l := range groupLaunches(entries) {
		if l.id != id {
			continue
		}
		if !l.open() {
			return l.last, true
		}
		cause := l.closedAtRead(now, o)
		if cause == "" {
			return Entry{}, false
		}
		e := l.view()
		e.TS, e.Kind, e.Result, e.Detail = now, KindLaunchResult, ResultFailed, cause
		return e, true
	}
	return Entry{}, false
}

type Registry struct{ path string }

func DefaultPath() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "pigtail", "spawns.jsonl")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "state", "pigtail", "spawns.jsonl")
	}
	return filepath.Join(os.TempDir(), "pigtail-state", "spawns.jsonl")
}

func Open(path string) *Registry { return &Registry{path: path} }

// Lock takes an exclusive flock held from the first check of a spawn to its
// append, so two spawns cannot both see "nothing live". The kernel drops it if
// the process dies.
func (r *Registry) Lock() (func(), error) {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return nil, fmt.Errorf("spawn registry: %w", err)
	}
	f, err := os.OpenFile(r.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("spawn registry lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("spawn registry lock: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

func (r *Registry) Append(e Entry) error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return fmt.Errorf("spawn registry: %w", err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("spawn registry: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("spawn registry: %w", err)
	}
	return f.Close()
}

// Entries reads every line; a missing file is an empty registry. A corrupt
// line is an error: a burst count computed on a partial read would be wrong silently.
func (r *Registry) Entries() ([]Entry, error) {
	f, err := os.Open(r.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("spawn registry: %w", err)
	}
	defer func() { _ = f.Close() }()
	var out []Entry
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("spawn registry %s line %d: %w", r.path, n, err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// Live keeps, per workspace, the latest spawn entry whose workspace is live.
// Only spawn entries (Kind "") with a workspace count: respawn, launch and
// margin records neither open nor close a workspace.
func Live(entries []Entry, live map[string]bool) []Entry {
	last := map[string]int{}
	for i, e := range entries {
		if e.Kind != "" || e.WorkspaceID == "" {
			continue
		}
		if live[e.WorkspaceID] {
			last[e.WorkspaceID] = i
		}
	}
	var out []Entry
	for i, e := range entries {
		if j, ok := last[e.WorkspaceID]; ok && j == i {
			out = append(out, e)
		}
	}
	return out
}

// Respawns keeps the latest respawn record per run ID, in the order of each
// run's latest record, restricted to subjectID when it is non-empty.
func Respawns(entries []Entry, subjectID string) []Entry {
	last := map[string]int{}
	for i, e := range entries {
		if e.Kind == KindRespawn && e.ID != "" {
			last[e.ID] = i
		}
	}
	var out []Entry
	for i, e := range entries {
		if j, ok := last[e.ID]; !ok || j != i || e.Kind != KindRespawn {
			continue
		}
		if subjectID != "" && e.SubjectID != subjectID {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Launches is the burst count: one entry per launch whose oldest record (its
// reserve, else its spawn entry) has a ts strictly after since — spawn
// entries (Kind "") and reserves grouped by ID, with or without a subject; a
// record without ID (legacy) is one launch. launch-ref, launch-armed and
// launch-result never count by themselves. Out of the count: a launch whose
// latest record is a launch-result failed without WorkspaceID (no tab ever
// existed), and a reserve without launch-ref closed at read (owner dead, cap
// passed). A zero ts is never recent; a ts ahead of now (the clock moved
// back) is, so the burst detector errs on refusing.
func Launches(entries []Entry, since, now time.Time, o PendingOpts) []Entry {
	o = o.withDefaults()
	type item struct {
		at int
		e  Entry
	}
	var items []item
	for i, e := range entries {
		if e.ID == "" && e.Kind == "" {
			items = append(items, item{i, e})
		}
	}
	for _, l := range groupLaunches(entries) {
		if !l.hasCnt {
			continue
		}
		if l.last.Kind == KindLaunchResult && l.last.WorkspaceID == "" {
			continue
		}
		if l.open() && l.ref == nil && l.reserve != nil && l.closedAtRead(now, o) != "" {
			continue
		}
		items = append(items, item{l.first, l.oldest})
	}
	sort.SliceStable(items, func(a, b int) bool { return items[a].at < items[b].at })
	var out []Entry
	for _, it := range items {
		if it.e.TS.IsZero() || !it.e.TS.After(since) {
			continue
		}
		out = append(out, it.e)
	}
	return out
}
