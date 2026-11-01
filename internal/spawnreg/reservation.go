package spawnreg

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Tab is a cmux workspace: ID the UUID in upper case, Ref workspace:N.
type Tab struct{ ID, Ref string }

// ReserveRequest is one launch to reserve. Zero Window/Cap take PendingWindow
// and ReserveCap, a nil Alive signal 0, a nil Lock r.Lock: no caller copies a bound.
type ReserveRequest struct {
	SubjectID, Origin string // SubjectID "" = a subjectless spawn; Origin cli|api
	Now               time.Time
	Window            time.Duration // from the launch-ref
	Cap               time.Duration // before the launch-ref, the owner alive up to this cap
	Alive             func(pid int) bool
	// ExcludeWorkspaceID: the caller's own tab; its own launch in progress is not counted.
	ExcludeWorkspaceID string
	// Check runs under the lock on the entries read there (burst, live re-read).
	Check func(entries []Entry) error
	// Tabs lists the cmux tabs, for the before/after diff of a create.
	Tabs func() ([]Tab, error)
	// Handoff takes over a launch left open with a tab (born = TS of the launch-ref).
	Handoff func(id string, tab Tab, born time.Time)
	Lock    func() (func(), error)
}

// PendingError refuses a reservation: the subject has a launch in progress.
// Entry is its reserve, with Origin and the WorkspaceRef of its launch-ref.
type PendingError struct{ Entry Entry }

func (e *PendingError) Error() string {
	where := "a tab not created yet"
	if e.Entry.WorkspaceRef != "" {
		where = e.Entry.WorkspaceRef
	}
	return fmt.Sprintf("launch %s in progress in %s", e.Entry.ID, where)
}

var (
	ErrRegistryUnreadable = errors.New("spawn registry unreadable")
	// ErrCmuxUnreachable: the tabs could not be listed before the create; nothing was created.
	ErrCmuxUnreachable = errors.New("cmux_unreachable")
	// ErrCmuxCreateFailed: the create left no new tab.
	ErrCmuxCreateFailed = errors.New("cmux_create_failed")
)

// Launch is one reserved launch, open until Launched, Fail or the end of
// WithReservation's fn closes it.
type Launch struct {
	ID string

	reg       *Registry
	req       ReserveRequest
	created   bool // Create was called
	attempted bool // the create itself ran
	before    []Tab
	tab       Tab
	resolved  bool
	born      time.Time
	closed    bool
	cause     string // why the create left no tab (Create decided it)
	writeErr  error
}

// Born is the TS of the launch-ref, zero before.
func (l *Launch) Born() time.Time { return l.born }

// Create lists the tabs, runs create once, and resolves the new tab: the one
// create returns with an ID, else the single new ID of the after/before diff.
// A second call is refused without running create: never a second tab. With
// a tab resolved it writes the launch-ref and returns nil, even when create
// failed: the tab exists, the launch goes on.
func (l *Launch) Create(create func() (Tab, error)) (Tab, error) {
	if l.created {
		return Tab{}, errors.New("launch already created once: never a second tab")
	}
	l.created = true
	before, err := l.listTabs()
	if err != nil {
		l.cause = fmt.Sprintf("%s: list tabs before the create: %v", ErrCmuxUnreachable, err)
		return Tab{}, fmt.Errorf("%w: list tabs before the create: %w", ErrCmuxUnreachable, err)
	}
	l.before, l.attempted = before, true
	tab, cerr := create()
	tab.ID = strings.ToUpper(tab.ID)
	if tab.ID == "" {
		t, cause, cerrKind := l.diff(tab.Ref)
		if cause != "" {
			l.cause = cause
			if cerr == nil {
				cerr = errors.New(cause)
			}
			if cerrKind != nil {
				return Tab{}, fmt.Errorf("%w: %w", cerrKind, cerr)
			}
			return Tab{}, fmt.Errorf("%s (%w)", cause, cerr)
		}
		tab = t
	}
	l.bind(tab)
	return tab, nil
}

func (l *Launch) listTabs() ([]Tab, error) {
	if l.req.Tabs == nil {
		return nil, nil
	}
	return l.req.Tabs()
}

// diff finds the one tab born since before; cause "" when found. kind is the
// sentinel of a create that left no tab.
func (l *Launch) diff(ref string) (Tab, string, error) {
	after, err := l.listTabs()
	if err != nil {
		return Tab{}, fmt.Sprintf("tab state unknown: list tabs after the create: %v", err), nil
	}
	old := map[string]bool{}
	for _, t := range l.before {
		old[strings.ToUpper(t.ID)] = true
	}
	var fresh []Tab
	for _, t := range after {
		if id := strings.ToUpper(t.ID); id != "" && !old[id] {
			old[id] = true
			fresh = append(fresh, Tab{ID: id, Ref: t.Ref})
		}
	}
	switch {
	case len(fresh) == 0:
		return Tab{}, ErrCmuxCreateFailed.Error() + ": no new tab after the create", ErrCmuxCreateFailed
	case len(fresh) > 1:
		return Tab{}, fmt.Sprintf("several new tabs, binding not guessed (%d new)", len(fresh)), nil
	case ref != "" && fresh[0].Ref != ref:
		return Tab{}, fmt.Sprintf("ambiguous: the new tab is %s, cmux named %s — binding not guessed", fresh[0].Ref, ref), nil
	}
	return fresh[0], "", nil
}

// bind records the tab: launch-ref with the launch's identity.
func (l *Launch) bind(tab Tab) {
	l.tab, l.resolved = tab, true
	l.born = time.Now().UTC()
	if err := l.reg.Append(Entry{TS: l.born, Kind: KindLaunchRef, ID: l.ID, SubjectID: l.req.SubjectID, Origin: l.req.Origin,
		WorkspaceID: tab.ID, WorkspaceRef: tab.Ref, Result: ResultPending}); err != nil {
		l.writeErr = errors.Join(l.writeErr, err)
	}
}

// Launched closes the launch with its spawn entry (Kind "", same ID).
func (l *Launch) Launched(e Entry) error {
	l.closed = true
	e.Kind, e.ID = "", l.ID
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	if e.Result == "" {
		e.Result = ResultLaunched
	}
	if e.SubjectID == "" {
		e.SubjectID = l.req.SubjectID
	}
	if e.Origin == "" {
		e.Origin = l.req.Origin
	}
	l.fillTab(&e, Tab{})
	return l.reg.Append(e)
}

// Fail closes the launch failed, its tab present (wrong account, wrong directory).
func (l *Launch) Fail(cause string, tab Tab) error {
	l.closed = true
	return l.writeFailed(cause, tab)
}

func (l *Launch) fillTab(e *Entry, tab Tab) {
	if tab.ID == "" {
		tab = l.tab
	}
	if e.WorkspaceID == "" {
		e.WorkspaceID = strings.ToUpper(tab.ID)
	}
	if e.WorkspaceRef == "" {
		e.WorkspaceRef = tab.Ref
	}
}

func (l *Launch) writeFailed(cause string, tab Tab) error {
	e := Entry{TS: time.Now().UTC(), Kind: KindLaunchResult, ID: l.ID, SubjectID: l.req.SubjectID, Origin: l.req.Origin,
		Result: ResultFailed, Detail: cause}
	l.fillTab(&e, tab)
	return l.reg.Append(e)
}

// finish is WithReservation's defer: it closes what fn left open. Never
// failed while a tab exists: an open launch with a tab goes to Handoff.
func (l *Launch) finish(fnErr error, rec any) error {
	if l.closed {
		return l.writeErr
	}
	l.closed = true
	if !l.attempted {
		cause := l.cause
		switch {
		case cause != "":
		case rec != nil:
			cause = fmt.Sprintf("panic: %v", rec)
		case fnErr != nil:
			cause = fnErr.Error()
		default:
			cause = "launch ended before creation"
		}
		return errors.Join(l.writeErr, l.writeFailed(cause, Tab{}))
	}
	if !l.resolved && l.cause == "" { // create panicked: diff now, same rules
		t, cause, _ := l.diff("")
		if cause == "" {
			l.bind(t)
		} else {
			l.cause = cause
		}
	}
	if !l.resolved {
		return errors.Join(l.writeErr, l.writeFailed(l.cause, Tab{}))
	}
	if l.req.Handoff != nil {
		l.req.Handoff(l.ID, l.tab, l.born)
	}
	return l.writeErr
}

// WithReservation reserves one launch and runs fn with it. Under the lock:
// read the entries (ErrRegistryUnreadable), refuse a launch in progress on the
// subject (*PendingError), run Check, append the reserve — the only writer of
// KindLaunchReserve. The lock is released before fn. Whatever fn does —
// error, nil, panic — the launch is closed on the way out (see finish); a
// write error there is joined to the returned error.
func (r *Registry) WithReservation(req ReserveRequest, fn func(*Launch) error) (id string, err error) {
	opts := PendingOpts{Window: req.Window, Cap: req.Cap, Alive: req.Alive, ExcludeWorkspaceID: req.ExcludeWorkspaceID}.withDefaults()
	req.Window, req.Cap, req.Alive = opts.Window, opts.Cap, opts.Alive
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	lock := req.Lock
	if lock == nil {
		lock = r.Lock
	}
	l, err := func() (*Launch, error) {
		unlock, err := lock()
		if err != nil {
			return nil, err
		}
		defer unlock()
		entries, err := r.Entries()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrRegistryUnreadable, err)
		}
		if req.SubjectID != "" {
			if e, ok := Pending(entries, req.SubjectID, now, opts); ok {
				return nil, &PendingError{Entry: e}
			}
		}
		if req.Check != nil {
			if err := req.Check(entries); err != nil {
				return nil, err
			}
		}
		l := &Launch{ID: NewLaunchID(), reg: r, req: req}
		if err := r.Append(Entry{TS: now, Kind: KindLaunchReserve, ID: l.ID, SubjectID: req.SubjectID, Origin: req.Origin,
			OwnerPID: os.Getpid(), Result: ResultPending}); err != nil {
			return nil, err
		}
		return l, nil
	}()
	if err != nil {
		return "", err
	}
	id = l.ID
	defer func() {
		rec := recover()
		if werr := l.finish(err, rec); werr != nil {
			err = errors.Join(err, werr)
		}
		if rec != nil {
			panic(rec)
		}
	}()
	err = fn(l)
	return id, err
}
