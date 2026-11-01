package focuspri

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// fake holds every dep's answer and counts every call.
type fake struct {
	mu sync.Mutex

	focus    []pigtail.Focus
	sealErr  error
	focusErr error
	pinned   map[string]bool
	wsList   []pigtail.Workspace
	wsErr    error
	wsSubj   map[string]string
	wsSubjEr error
	peers    []pigtail.PeerSession
	peersErr error
	themes   map[string]string
	themeErr error

	now   time.Time
	logs  []string
	calls map[string]int
}

func newFake() *fake {
	return &fake{
		now:    time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC),
		calls:  map[string]int{},
		wsSubj: map[string]string{},
		themes: map[string]string{},
	}
}

func (f *fake) deps() Deps {
	count := func(n string) { f.calls[n]++ }
	return Deps{
		ReadFocus: func() ([]pigtail.Focus, error, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			count("focus")
			return f.focus, f.sealErr, f.focusErr
		},
		Pinned: func() map[string]bool {
			f.mu.Lock()
			defer f.mu.Unlock()
			count("pinned")
			return f.pinned
		},
		Workspaces: func() ([]pigtail.Workspace, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			count("ws")
			return f.wsList, f.wsErr
		},
		SubjectForWorkspace: func(id string) (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			count("wssubject")
			return f.wsSubj[id], f.wsSubjEr
		},
		Peers: func() ([]pigtail.PeerSession, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			count("peers")
			return f.peers, f.peersErr
		},
		ThemeOfSubject: func(id string) (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			count("theme")
			return f.themes[id], f.themeErr
		},
		Now: func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now },
		Logf: func(format string, a ...any) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.logs = append(f.logs, fmt.Sprintf(format, a...))
		},
	}
}

func (f *fake) advance(d time.Duration) { f.mu.Lock(); f.now = f.now.Add(d); f.mu.Unlock() }
func (f *fake) logCount() int           { f.mu.Lock(); defer f.mu.Unlock(); return len(f.logs) }
func (f *fake) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += c
	}
	return n
}

func focusOf(kind, id string) []pigtail.Focus {
	return []pigtail.Focus{{ID: "fcs_1", Name: "n", Members: []pigtail.FocusMember{{Kind: kind, ID: id}}}}
}

func resolver(f *fake) *Resolver {
	r := New(f.deps(), 5*time.Second, 30*time.Second)
	r.Refresh()
	return r
}

func TestResolveSubjectFocus(t *testing.T) {
	f := newFake()
	f.focus = focusOf(pigtail.FocusMemberSubject, "sub1")
	f.wsList = []pigtail.Workspace{{ID: "w1", Path: "/r/w1"}}
	f.wsSubj["w1"] = "sub1"
	r := resolver(f)
	if !r.IsFocus(Caller{Cwd: "/r/w1/pkg"}) {
		t.Fatal("cwd in the subject's workspace must be focus")
	}
}

func TestResolveThemeFocusFallback(t *testing.T) {
	f := newFake()
	f.focus = focusOf(pigtail.FocusMemberTheme, "th1")
	f.wsList = []pigtail.Workspace{{ID: "w1", Path: "/r/w1"}}
	f.wsSubj["w1"] = "sub1"
	f.themes["sub1"] = "th1"
	r := resolver(f)
	if !r.IsFocus(Caller{Cwd: "/r/w1"}) {
		t.Fatal("theme focus must apply when the subject has none")
	}
}

func TestResolveByCwd(t *testing.T) {
	f := newFake()
	f.focus = focusOf(pigtail.FocusMemberSubject, "inner-sub")
	f.wsList = []pigtail.Workspace{{ID: "outer", Path: "/r"}, {ID: "inner", Path: "/r/wt"}}
	f.wsSubj["outer"] = "outer-sub"
	f.wsSubj["inner"] = "inner-sub"
	r := resolver(f)
	if !r.IsFocus(Caller{Cwd: "/r/wt/x"}) {
		t.Fatal("the deepest workspace must win")
	}
	if r.IsFocus(Caller{Cwd: "/r/other"}) {
		t.Fatal("outer workspace's subject has no focus")
	}
}

func TestResolveBySession(t *testing.T) {
	f := newFake()
	f.focus = focusOf(pigtail.FocusMemberSubject, "sub1")
	// cwd inside a workspace with no subject: the session decides.
	f.wsList = []pigtail.Workspace{{ID: "w1", Path: "/r/w1"}}
	f.peers = []pigtail.PeerSession{{SessionID: "s1", SubjectID: "sub1"}}
	r := resolver(f)
	if !r.IsFocus(Caller{SessionID: "s1"}) {
		t.Fatal("session must resolve through its peer")
	}
	if !r.IsFocus(Caller{SessionID: "s1", Cwd: "/r/w1"}) {
		t.Fatal("session fallback when the cwd workspace has no subject")
	}
}

func TestResolveBySessionThemeFallback(t *testing.T) {
	f := newFake()
	f.focus = focusOf(pigtail.FocusMemberTheme, "th1")
	f.peers = []pigtail.PeerSession{{SessionID: "s1", SubjectID: "lonely"}}
	f.themes["lonely"] = "th1"
	r := resolver(f)
	if !r.IsFocus(Caller{SessionID: "s1"}) {
		t.Fatal("a subject reached only through a peer must get its theme")
	}
}

func TestResolvePeerWithoutSubjectUsesItsWorkspace(t *testing.T) {
	f := newFake()
	f.focus = focusOf(pigtail.FocusMemberSubject, "sub1")
	f.wsList = []pigtail.Workspace{{ID: "w1", Path: "/r/w1"}}
	f.wsSubj["w1"] = "sub1"
	f.peers = []pigtail.PeerSession{{SessionID: "s1", Cwd: "/r/w1/deep"}, {SessionID: "s2", WorkspaceID: "w1"}}
	r := resolver(f)
	if !r.IsFocus(Caller{SessionID: "s1"}) || !r.IsFocus(Caller{SessionID: "s2"}) {
		t.Fatal("a peer with no subject resolves through its workspace or cwd")
	}
}

func TestResolvePinnedWorkspace(t *testing.T) {
	f := newFake()
	f.pinned = map[string]bool{"AAAA-1111": true}
	r := resolver(f)
	if !r.IsFocus(Caller{CmuxWorkspaceID: "aaaa-1111"}) {
		t.Fatal("a pinned workspace is focus, whatever the case")
	}
	if r.IsFocus(Caller{CmuxWorkspaceID: "bbbb"}) {
		t.Fatal("another workspace is not")
	}
}

func TestResolveUnknownCallerIsNotFocus(t *testing.T) {
	f := newFake()
	f.focus = focusOf(pigtail.FocusMemberSubject, "sub1")
	f.wsList = []pigtail.Workspace{{ID: "w1", Path: "/r/w1"}}
	f.wsSubj["w1"] = "sub1"
	r := resolver(f)
	before := f.logCount()
	for _, c := range []Caller{{}, {Cwd: "/nowhere"}, {SessionID: "unknown"}, {CmuxWorkspaceID: "not-a-uuid"}} {
		if r.IsFocus(c) {
			t.Fatalf("%+v must not be focus", c)
		}
	}
	if f.logCount() != before || before != 0 {
		t.Fatalf("logs = %v", f.logs)
	}
}

func TestRefreshDependencyErrorLogsOnce(t *testing.T) {
	boom := errors.New("boom")
	cases := map[string]func(*fake){
		"focus":     func(f *fake) { f.focusErr = boom },
		"workspace": func(f *fake) { f.wsErr = boom },
		"peers":     func(f *fake) { f.peersErr = boom },
		"wssubject": func(f *fake) { f.wsList = []pigtail.Workspace{{ID: "w", Path: "/w"}}; f.wsSubjEr = boom },
		"theme": func(f *fake) {
			f.wsList = []pigtail.Workspace{{ID: "w", Path: "/w"}}
			f.wsSubj["w"] = "s"
			f.themeErr = boom
		},
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			set(f)
			r := New(f.deps(), 5*time.Second, 30*time.Second)
			r.Refresh()
			if r.IsFocus(Caller{Cwd: "/w", SessionID: "s", CmuxWorkspaceID: "x"}) {
				t.Fatal("failing deps must read as non-focus")
			}
			if f.logCount() != 1 || !strings.Contains(f.logs[0], "focuspri: refresh:") || !strings.Contains(f.logs[0], "boom") {
				t.Fatalf("logs = %v", f.logs)
			}
			r.Refresh() // same failure: silent
			f.mu.Lock()
			f.focusErr, f.wsErr, f.peersErr, f.wsSubjEr, f.themeErr = errors.New("other"), errors.New("other"), errors.New("other"), errors.New("other"), errors.New("other")
			f.mu.Unlock()
			r.Refresh() // another error: still silent
			if f.logCount() != 1 {
				t.Fatalf("logs = %v", f.logs)
			}
			f.mu.Lock()
			f.focusErr, f.wsErr, f.peersErr, f.wsSubjEr, f.themeErr = nil, nil, nil, nil, nil
			f.mu.Unlock()
			r.Refresh()
			if f.logCount() != 2 || !strings.Contains(f.logs[1], "recovered") {
				t.Fatalf("logs = %v", f.logs)
			}
			r.Refresh()
			if f.logCount() != 2 {
				t.Fatalf("a good refresh after recovery is silent: %v", f.logs)
			}
		})
	}
}

func TestRefreshManyFailingEntriesOneLine(t *testing.T) {
	f := newFake()
	f.wsList = []pigtail.Workspace{{ID: "a", Path: "/a"}, {ID: "b", Path: "/b"}, {ID: "c", Path: "/c"}}
	f.wsSubjEr = errors.New("db locked")
	resolver(f)
	if f.logCount() != 1 || !strings.Contains(f.logs[0], "3 errors") {
		t.Fatalf("logs = %v", f.logs)
	}
}

func TestRefreshUnboundWorkspaceIsSilent(t *testing.T) {
	f := newFake()
	f.wsList = []pigtail.Workspace{{ID: "w", Path: "/w"}}
	f.peers = []pigtail.PeerSession{{SessionID: "s", SubjectID: "gone"}}
	// SubjectForWorkspace and ThemeOfSubject answer ("", nil): absence.
	resolver(f)
	if f.logCount() != 0 {
		t.Fatalf("logs = %v", f.logs)
	}
}

func TestRefreshSealErrorFoldsIntoSummary(t *testing.T) {
	f := newFake()
	f.sealErr = errors.New("focus: seal: disk")
	f.focus = focusOf(pigtail.FocusMemberSubject, "sub1")
	f.wsList = []pigtail.Workspace{{ID: "w", Path: "/w"}}
	f.wsSubj["w"] = "sub1"
	r := resolver(f)
	if f.logCount() != 1 || !strings.Contains(f.logs[0], "1 errors") {
		t.Fatalf("logs = %v", f.logs)
	}
	if !r.IsFocus(Caller{Cwd: "/w"}) {
		t.Fatal("the returned list is still used")
	}
	r.Refresh()
	if f.logCount() != 1 {
		t.Fatalf("a persisting seal error logs once: %v", f.logs)
	}
	f.mu.Lock()
	f.sealErr = nil
	f.mu.Unlock()
	r.Refresh()
	if f.logCount() != 2 || !strings.Contains(f.logs[1], "recovered") {
		t.Fatalf("logs = %v", f.logs)
	}
}

func TestResolvePinnedNilIsSilent(t *testing.T) {
	f := newFake()
	f.pinned = nil
	r := resolver(f)
	if r.IsFocus(Caller{CmuxWorkspaceID: "x"}) || f.logCount() != 0 {
		t.Fatalf("logs = %v", f.logs)
	}
}

func TestResolveNoSnapshotYet(t *testing.T) {
	f := newFake()
	f.pinned = map[string]bool{"X": true}
	r := New(f.deps(), 5*time.Second, 30*time.Second)
	if r.IsFocus(Caller{CmuxWorkspaceID: "x"}) || f.logCount() != 0 {
		t.Fatal("no snapshot: non-focus, silently")
	}
}

func TestSnapshotExpires(t *testing.T) {
	f := newFake()
	f.pinned = map[string]bool{"X": true}
	r := resolver(f)
	if !r.IsFocus(Caller{CmuxWorkspaceID: "x"}) {
		t.Fatal("setup")
	}
	f.advance(31 * time.Second)
	if r.IsFocus(Caller{CmuxWorkspaceID: "x"}) || f.logCount() != 0 {
		t.Fatalf("expired snapshot: non-focus, silently (logs %v)", f.logs)
	}
}

func TestRefreshErrorKeepsPreviousSnapshot(t *testing.T) {
	f := newFake()
	f.pinned = map[string]bool{"X": true}
	r := resolver(f)
	f.mu.Lock()
	f.wsErr = errors.New("boom")
	f.pinned = nil
	f.mu.Unlock()
	f.advance(5 * time.Second)
	r.Refresh()
	if !r.IsFocus(Caller{CmuxWorkspaceID: "x"}) {
		t.Fatal("a failed read keeps the previous snapshot")
	}
}

func TestRunRefreshesBeforeFirstTick(t *testing.T) {
	f := newFake()
	f.pinned = map[string]bool{"X": true}
	d := f.deps()
	started := make(chan struct{})
	var once sync.Once
	orig := d.Peers
	d.Peers = func() ([]pigtail.PeerSession, error) { once.Do(func() { close(started) }); return orig() }
	r := New(d, time.Hour, 30*time.Second) // the ticker never fires
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	// Cancel as soon as the first refresh began; Run returns after finishing it.
	<-started
	cancel()
	<-done
	if !r.IsFocus(Caller{CmuxWorkspaceID: "x"}) {
		t.Fatal("the snapshot must exist before the first tick")
	}
}

func TestIsFocusMakesNoStoreCall(t *testing.T) {
	f := newFake()
	f.focus = focusOf(pigtail.FocusMemberSubject, "sub1")
	f.pinned = map[string]bool{"PIN": true}
	f.wsList = []pigtail.Workspace{{ID: "w1", Path: "/r/w1"}}
	f.wsSubj["w1"] = "sub1"
	f.peers = []pigtail.PeerSession{{SessionID: "s1", SubjectID: "sub1"}}
	r := resolver(f)
	before := f.total()
	for i := 0; i < 100; i++ {
		r.IsFocus(Caller{CmuxWorkspaceID: "pin"})
		r.IsFocus(Caller{Cwd: "/r/w1/x"})
		r.IsFocus(Caller{SessionID: "s1"})
		r.IsFocus(Caller{SessionID: "nobody", Cwd: "/nowhere"})
	}
	if f.total() != before {
		t.Fatalf("IsFocus called a dep: %v", f.calls)
	}
}
