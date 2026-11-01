package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/sessbudget"
	"github.com/aloysbr/pigtail/internal/daemon/sessbudget/sessbudgettest"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Two recipients, each idle on its own surface, each the active session there.
const (
	surf  = "3A23D5DD-59EE-471D-9270-32F0FB06E099"
	surf2 = "5B1E0C44-7A2D-4F3B-9C8E-1D2F3A4B5C6D"
)

type fakeWake struct {
	// natives is keyed by pid: each recipient reads its own sessions/<pid>.json,
	// with its own SessionID — step 2 requires Native.SessionID == s, so one
	// shared native would refuse every recipient but one before cmux.
	natives   map[int]claudesession.Native
	alive     bool
	frozen    map[string]bool
	listing   string // one cmux listing holding every recipient
	listErr   error
	focused   string
	focusErr  error
	screen    string
	screenErr map[string]error  // by surface id: read-screen fails for that pane only
	onScreen  func()            // runs inside ReadScreen (D26 recheck)
	sent      []string          // surface ids the wake line was typed into
	compacts  []string          // surface ids /compact was typed into (Tâche 4)
	wraps     []string          // surface ids the done command was typed into (idle wrap)
	continues []string          // surface ids "continue" was typed into (auto-retry)
	onSend    func(line string) // runs inside SendLine, before the line is recorded
	dead      map[int]bool      // pids PidAlive and SessionAlive report dead
	sendErr   error

	// The idle pause's seams (relay_pause_test.go).
	environ    map[int]string // ProcessEnviron by pid ("" = none set: an empty environment)
	environErr error
	background func(cfg, session string, pid int, screen string) ([]string, error)
	signals    []int // pids signalled, in order
	signalErr  error
	onSignal   func(pid int) // runs inside Signal, before the pid is marked dead
	onAlive    func(pid int) // runs inside SessionAlive
	onFocus    func()        // runs inside Focused
	stayAlive  bool          // Signal leaves the pid alive (a slow exit)

	// The auto-retry's seams (relay_retry_test.go).
	apiErrors   map[string]claudesession.APIError // by session: the transcript's last API error
	prompts     map[string][]claudesession.Prompt // by session: the typed prompts the transcript holds
	promptsErr  error
	reachable   bool
	reachCalls  int
	budgetNo    string // non-empty: BudgetAcquire refuses with this reason
	budgetCalls int    // BudgetAcquire asks
	released    int    // BudgetRelease calls

	listCalls, atSendCalls, focusCalls, screenCalls int
	cmuxCalls                                       int // every cmux seam
}

func (fw *fakeWake) resetCounts() {
	fw.listCalls, fw.atSendCalls, fw.focusCalls, fw.screenCalls, fw.cmuxCalls = 0, 0, 0, 0, 0
}

func (fw *fakeWake) deps() *WakeDeps {
	return &WakeDeps{
		ReadNative: func(_ string, pid int) (claudesession.Native, bool) {
			n, ok := fw.natives[pid]
			return n, ok && n.SessionID != ""
		},
		SessionAlive: func(_ string, pid int, _ string) bool {
			if fw.onAlive != nil {
				fw.onAlive(pid)
			}
			return fw.alive && !fw.dead[pid]
		},
		PidAlive:           func(pid int) bool { return !fw.dead[pid] },
		Frozen:             func() map[string]bool { return fw.frozen },
		CmuxSessions:       func() ([]byte, error) { fw.cmuxCalls++; fw.listCalls++; return []byte(fw.listing), fw.listErr },
		CmuxSessionsAtSend: func() ([]byte, error) { fw.cmuxCalls++; fw.atSendCalls++; return []byte(fw.listing), fw.listErr },
		Focused: func() (string, error) {
			fw.cmuxCalls++
			fw.focusCalls++
			if fw.onFocus != nil {
				fw.onFocus()
			}
			return fw.focused, fw.focusErr
		},
		ReadScreen: func(surface string) (string, error) {
			fw.cmuxCalls++
			fw.screenCalls++
			if fw.onScreen != nil {
				fw.onScreen()
			}
			return fw.screen, fw.screenErr[surface]
		},
		SendLine: func(s, line string) error {
			fw.cmuxCalls++
			if fw.onSend != nil {
				fw.onSend(line)
			}
			switch line {
			case testDoneCommand:
				fw.wraps = append(fw.wraps, s)
			case relay.WakeLine:
				fw.sent = append(fw.sent, s)
			case relay.CompactLine:
				fw.compacts = append(fw.compacts, s)
			case relay.ContinueLine:
				fw.continues = append(fw.continues, s)
			default:
				panic(line) // pigtaild types nothing else
			}
			return fw.sendErr
		},
		Background: func(cfg, session string, pid int, screen string) ([]string, error) {
			if fw.background != nil {
				return fw.background(cfg, session, pid, screen)
			}
			return nil, nil
		},
		ProcessEnviron: func(pid int) (string, error) { return fw.environ[pid], fw.environErr },
		Signal: func(pid int) error {
			if fw.onSignal != nil {
				fw.onSignal(pid)
			}
			if fw.signalErr != nil {
				return fw.signalErr
			}
			fw.signals = append(fw.signals, pid)
			if !fw.stayAlive {
				fw.dead[pid] = true
			}
			return nil
		},
		WaitGone:     func(pid int, _ time.Duration) bool { return fw.dead[pid] },
		APIReachable: func() bool { fw.reachCalls++; return fw.reachable },
		LastAPIError: func(_, session string) (claudesession.APIError, bool) {
			e, ok := fw.apiErrors[session]
			return e, ok
		},
		HumanPromptsSince: func(_, session string, since time.Time) ([]claudesession.Prompt, error) {
			var out []claudesession.Prompt
			for _, p := range fw.prompts[session] {
				if p.At.After(since) {
					out = append(out, p)
				}
			}
			return out, fw.promptsErr
		},
	}
}

// wakeLog captures the daemon log lines of the relay.
type wakeLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *wakeLog) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *wakeLog) count(sub string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, s := range l.lines {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

func (l *wakeLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func screenFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "claudesession", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func cmuxEntry(session, surface string, pid int, active string) string {
	return fmt.Sprintf(`{"session_id":%q,"surface_id":%q,"pid":%d,"agent":"claude","active_for_surface":%v,"active_surface_session_id":%q}`,
		session, surface, pid, active == session, active)
}

func cmuxListingOf(entries ...string) string {
	return `{"sessions":[` + strings.Join(entries, ",") + `]}`
}

func alphaPeer() pigtail.PeerSession {
	return pigtail.PeerSession{SessionID: "s-alpha", Name: "alpha", Scope: "gx", Status: "idle", PID: 7, ConfigDir: "/cfg", CmuxSurfaceID: surf}
}

func gammaPeer() pigtail.PeerSession {
	return pigtail.PeerSession{SessionID: "s-gamma", Name: "gamma", Scope: "gx", Status: "idle", PID: 8, ConfigDir: "/cfg", CmuxSurfaceID: surf2}
}

// newWakeFixture: a relay with fake wake seams; s-alpha (pid 7, surf) idle,
// interactive, alive, the active session of its surface, its input empty and
// unfocused. Peers are registered by the test (wakePeers), messages by sendTo.
func newWakeFixture(t *testing.T) (*relayFixture, *fakeWake, *wakeLog) {
	t.Helper()
	f := newRelayFixture(t)
	lg := &wakeLog{}
	f.rl.svc.Logf = lg.logf
	fw := &fakeWake{
		natives: map[int]claudesession.Native{7: {SessionID: "s-alpha", Status: "idle", Kind: "interactive", StatusUpdatedAt: 1000}},
		alive:   true,
		dead:    map[int]bool{},
		listing: cmuxListingOf(cmuxEntry("s-alpha", surf, 7, "s-alpha")),
		screen:  screenFixture(t, "screen-idle-empty.txt"),
	}
	f.rl.cfg.Wake = fw.deps()
	f.rl.cfg.DoneCommand = testDoneCommand // the idle wrap on; TestWrapIdleOffWithoutDoneCommand turns it off
	f.rl.startedAt = f.now.Add(-time.Hour)
	return f, fw, lg
}

// testDoneCommand stands for the user's done command (settings.yaml
// session.done_command) in the relay's tests.
const testDoneCommand = "/finish"

// wakePeers registers the sender s-beta and the given recipients.
func (f *relayFixture) wakePeers(t *testing.T, ps ...pigtail.PeerSession) {
	t.Helper()
	f.peers(t, append([]pigtail.PeerSession{{SessionID: "s-beta", Name: "beta", Scope: "gx"}}, ps...)...)
}

// sendTo queues one message from s-beta, then zeroes the fake's counters:
// the send-time check (task 5) is not what a sweep test measures.
func (f *relayFixture) sendTo(t *testing.T, fw *fakeWake, to string) pigtail.SendMessageResponse {
	t.Helper()
	rec, out := f.send(t, pigtail.SendMessageRequest{From: "s-beta", To: to, Body: "hello"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("send to %s = %d %s", to, rec.Code, rec.Body.String())
	}
	fw.resetCounts()
	return out
}

func twoWakeable(t *testing.T, f *relayFixture, fw *fakeWake) {
	t.Helper()
	// peers s-alpha (PID 7, surf) and s-gamma (PID 8, surf2), ConfigDir "/cfg";
	// one pending message from s-beta to each.
	fw.natives = map[int]claudesession.Native{
		7: {SessionID: "s-alpha", Status: "idle", Kind: "interactive", StatusUpdatedAt: 1000},
		8: {SessionID: "s-gamma", Status: "idle", Kind: "interactive", StatusUpdatedAt: 1000},
	}
	fw.listing = cmuxListingOf(cmuxEntry("s-alpha", surf, 7, "s-alpha"), cmuxEntry("s-gamma", surf2, 8, "s-gamma"))
	f.wakePeers(t, alphaPeer(), gammaPeer())
	f.sendTo(t, fw, "s-alpha")
	f.sendTo(t, fw, "s-gamma")
}

func claimVia(t *testing.T, f *relayFixture, session, via string) []pigtail.Message {
	t.Helper()
	rec := doJSON(t, f.r, "POST", "/messages/claim", pigtail.ClaimRequest{Session: session, Via: via})
	if rec.Code != http.StatusOK {
		t.Fatalf("claim = %d %s", rec.Code, rec.Body.String())
	}
	var cr pigtail.ClaimResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &cr)
	return cr.Messages
}

// B1, D35: one typing, never a second without a turn; success is the claim.
func TestWakeTypesOnceThenClaimSucceeds(t *testing.T) {
	t.Parallel()
	f, fw, lg := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	f.sendTo(t, fw, "s-alpha")
	f.rl.sweepIdle()
	if !reflect.DeepEqual(fw.sent, []string{surf}) {
		t.Fatalf("sent = %v, want [surf]", fw.sent)
	}
	f.rl.sweepIdle()
	if len(fw.sent) != 1 {
		t.Fatalf("typed again without a turn: %v", fw.sent)
	}
	if lg.count("wake: typed in "+surf+" for s-alpha (1 pending)") != 1 {
		t.Fatalf("typing log missing:\n%s", lg)
	}
	ms := claimVia(t, f, "s-alpha", "prompt")
	if len(ms) != 1 || ms[0].DeliveredVia != "prompt" {
		t.Fatalf("claimed %+v", ms)
	}
	if lg.count("wake: s-alpha claimed 1 via prompt") != 1 {
		t.Fatalf("success line missing:\n%s", lg)
	}
}

// B2: a draft holds the typing; an empty input lets it through.
func TestWakeHeldByDraftThenTypes(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	f.sendTo(t, fw, "s-alpha")
	fw.screen = screenFixture(t, "screen-draft.txt")
	f.rl.sweepIdle()
	if len(fw.sent) != 0 {
		t.Fatalf("typed over a draft: %v", fw.sent)
	}
	fw.screen = screenFixture(t, "screen-idle-empty.txt")
	f.rl.sweepIdle()
	if len(fw.sent) != 1 {
		t.Fatalf("sent = %v, want one typing", fw.sent)
	}
}

// B3, D33: never into the pane that has the keyboard focus.
func TestWakeHeldByFocusThenTypes(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	f.sendTo(t, fw, "s-alpha")
	fw.focused = surf
	f.rl.sweepIdle()
	if len(fw.sent) != 0 {
		t.Fatalf("typed into the focused pane: %v", fw.sent)
	}
	fw.focused = surf2
	f.rl.sweepIdle()
	if len(fw.sent) != 1 {
		t.Fatalf("sent = %v, want one typing", fw.sent)
	}
}

func declareWait(t *testing.T, f *relayFixture, session, typ string) {
	t.Helper()
	rec := doJSON(t, f.r, "POST", "/peers/"+session+"/wait", pigtail.SessionWaitRequest{Type: typ})
	if rec.Code != http.StatusOK {
		t.Fatalf("declare %s = %d %s", typ, rec.Code, rec.Body.String())
	}
}

// B4: a declared wait holds the typing, before any cmux call.
func TestWakeHeldByDeclaredWait(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	declareWait(t, f, "s-alpha", "review")
	f.sendTo(t, fw, "s-alpha")
	f.rl.sweepIdle()
	if len(fw.sent) != 0 || fw.cmuxCalls != 0 {
		t.Fatalf("sent %v, %d cmux calls; want none", fw.sent, fw.cmuxCalls)
	}
}

// B4, second half: the user answers, the UserPromptSubmit hook claims the
// message with his reply — no typing to credit (D35).
func TestWakeHeldByDeclaredWaitThenClaimedByPrompt(t *testing.T) {
	t.Parallel()
	f, fw, lg := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	declareWait(t, f, "s-alpha", "review")
	f.sendTo(t, fw, "s-alpha")
	f.rl.sweepIdle()
	if len(fw.sent) != 0 {
		t.Fatalf("typed under a declared wait: %v", fw.sent)
	}
	ms := claimVia(t, f, "s-alpha", "prompt")
	if len(ms) != 1 || ms[0].DeliveredVia != "prompt" {
		t.Fatalf("claimed %+v", ms)
	}
	if n, _ := f.st.CountPending("s-alpha", f.now); n != 0 {
		t.Fatalf("pending after the claim = %d", n)
	}
	if len(fw.sent) != 0 || lg.count("claimed") != 0 {
		t.Fatalf("sent %v; log:\n%s", fw.sent, lg)
	}
}

// B5: a failure, a maintenance freeze, a non-interactive session.
func TestWakeHeldByFailureFreezeOrKind(t *testing.T) {
	t.Parallel()
	for name, edit := range map[string]func(*relayFixture, *fakeWake){
		"limit": func(f *relayFixture, _ *fakeWake) {
			if err := f.st.SetSessionWait("s-alpha", pigtail.SessionWait{Type: "limit", Source: pigtail.WaitSourceAuto, Since: f.now}); err != nil {
				panic(err)
			}
		},
		"freeze": func(_ *relayFixture, fw *fakeWake) { fw.frozen = map[string]bool{"s-alpha": true} },
		"kind": func(_ *relayFixture, fw *fakeWake) {
			n := fw.natives[7]
			n.Kind = "sdk-cli"
			fw.natives[7] = n
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, fw, _ := newWakeFixture(t)
			f.wakePeers(t, alphaPeer())
			edit(f, fw)
			f.sendTo(t, fw, "s-alpha")
			f.rl.sweepIdle()
			if len(fw.sent) != 0 || fw.cmuxCalls != 0 {
				t.Fatalf("sent %v, %d cmux calls; want none", fw.sent, fw.cmuxCalls)
			}
		})
	}
}

// B6: no surface in the registry → no cmux call; another session active on
// the surface → no typing.
func TestWakeHeldBySurface(t *testing.T) {
	t.Parallel()
	t.Run("no surface", func(t *testing.T) {
		f, fw, _ := newWakeFixture(t)
		p := alphaPeer()
		p.CmuxSurfaceID = ""
		f.wakePeers(t, p)
		f.sendTo(t, fw, "s-alpha")
		f.rl.sweepIdle()
		if len(fw.sent) != 0 || fw.cmuxCalls != 0 {
			t.Fatalf("sent %v, %d cmux calls; want none", fw.sent, fw.cmuxCalls)
		}
	})
	t.Run("another session active", func(t *testing.T) {
		f, fw, _ := newWakeFixture(t)
		f.wakePeers(t, alphaPeer())
		fw.listing = cmuxListingOf(cmuxEntry("s-alpha", surf, 7, "s-other"))
		f.sendTo(t, fw, "s-alpha")
		f.rl.sweepIdle()
		if len(fw.sent) != 0 {
			t.Fatalf("typed over another session: %v", fw.sent)
		}
	})
}

// B7, D31: shell (a background shell shown) counts as idle.
func TestWakeShellStatus(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	n := fw.natives[7]
	n.Status = "shell"
	fw.natives[7] = n
	f.wakePeers(t, alphaPeer())
	f.sendTo(t, fw, "s-alpha")
	f.rl.sweepIdle()
	if len(fw.sent) != 1 {
		t.Fatalf("sent = %v, want one typing", fw.sent)
	}
}

// B8: a watched session is the watcher's job.
func TestWakeHeldByWatcher(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	f.sendTo(t, fw, "s-alpha")
	f.rl.register("s-alpha")
	f.rl.sweepIdle()
	if len(fw.sent) != 0 || fw.cmuxCalls != 0 {
		t.Fatalf("sent %v, %d cmux calls; want none", fw.sent, fw.cmuxCalls)
	}
}

// B10: right after a restart no watcher is known yet. The grace holds the
// first seconds; past it, a watcher that re-registered holds the session.
func TestWakeStartupGrace(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	f.sendTo(t, fw, "s-alpha")
	f.rl.startedAt = f.now.Add(-10 * time.Second)
	f.rl.sweepIdle()
	if len(fw.sent) != 0 || fw.cmuxCalls != 0 {
		t.Fatalf("in the grace: sent %v, %d cmux calls; want none", fw.sent, fw.cmuxCalls)
	}
	f.advance(25 * time.Second)
	f.rl.register("s-alpha")
	f.rl.sweepIdle()
	if len(fw.sent) != 0 || fw.cmuxCalls != 0 {
		t.Fatalf("watcher back: sent %v, %d cmux calls; want none", fw.sent, fw.cmuxCalls)
	}
}

// §7, D34: a failed cmux call is logged each time and retried next sweep.
func TestWakeCmuxFailureIsLoggedAndRetried(t *testing.T) {
	t.Parallel()
	t.Run("listing", func(t *testing.T) {
		f, fw, lg := newWakeFixture(t)
		f.wakePeers(t, alphaPeer())
		f.sendTo(t, fw, "s-alpha")
		fw.listErr = errors.New("cmux sessions: signal: killed")
		f.rl.sweepIdle()
		if len(fw.sent) != 0 || lg.count("wake: s-alpha: cmux sessions list: cmux sessions: signal: killed") != 1 {
			t.Fatalf("sent %v; log:\n%s", fw.sent, lg)
		}
		fw.listErr = nil
		f.rl.sweepIdle()
		if len(fw.sent) != 1 {
			t.Fatalf("not retried: %v", fw.sent)
		}
	})
	t.Run("send", func(t *testing.T) {
		f, fw, lg := newWakeFixture(t)
		f.wakePeers(t, alphaPeer())
		f.sendTo(t, fw, "s-alpha")
		fw.sendErr = errors.New("cmux send: signal: killed")
		f.rl.sweepIdle()
		if lg.count("wake: s-alpha: cmux send: cmux send: signal: killed") != 1 {
			t.Fatalf("send failure not logged:\n%s", lg)
		}
		f.rl.wakeMu.Lock()
		_, typed := f.rl.typed["s-alpha"]
		f.rl.wakeMu.Unlock()
		if typed {
			t.Fatal("a failed send must not count as typed")
		}
		fw.sendErr = nil
		f.rl.sweepIdle()
		if len(fw.sent) != 2 {
			t.Fatalf("not retried: %v", fw.sent)
		}
	})
}

// §7, step 7: the focus is one answer for the whole sweep; its failure stops it.
func TestWakeIdentifyFailureStopsTheSweep(t *testing.T) {
	t.Parallel()
	f, fw, lg := newWakeFixture(t)
	twoWakeable(t, f, fw)
	fw.focusErr = errors.New("cmux identify: signal: killed")
	f.rl.sweepIdle()
	if lg.count(": cmux identify: cmux identify: signal: killed") != 1 || fw.focusCalls != 1 || fw.screenCalls != 0 || len(fw.sent) != 0 {
		t.Fatalf("focus %d, screen %d, sent %v; log:\n%s", fw.focusCalls, fw.screenCalls, fw.sent, lg)
	}
	fw.focusErr = nil
	f.rl.sweepIdle()
	got := append([]string(nil), fw.sent...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{surf, surf2}) {
		t.Fatalf("sent = %v, want both", fw.sent)
	}
}

// §7, step 8: one pane's read-screen failure skips that pane only.
func TestWakeReadScreenFailureSkipsToNext(t *testing.T) {
	t.Parallel()
	f, fw, lg := newWakeFixture(t)
	twoWakeable(t, f, fw)
	fw.screenErr = map[string]error{surf: errors.New("cmux read-screen: signal: killed")}
	f.rl.sweepIdle()
	if lg.count("wake: s-alpha: cmux read-screen: cmux read-screen: signal: killed") != 1 ||
		!reflect.DeepEqual(fw.sent, []string{surf2}) || fw.screenCalls != 2 {
		t.Fatalf("sent %v, screen %d; log:\n%s", fw.sent, fw.screenCalls, lg)
	}
	fw.screenErr = nil
	f.rl.sweepIdle()
	if !reflect.DeepEqual(fw.sent, []string{surf2, surf}) {
		t.Fatalf("sent = %v, want surf typed at the next sweep", fw.sent)
	}
}

// Review Focus 1, §4: typed but the session never left idle → no second
// typing, one log line; a turn later, still pending → a new typing.
func TestWakeNoRetypeWhileStillIdle(t *testing.T) {
	t.Parallel()
	f, fw, lg := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	f.sendTo(t, fw, "s-alpha")
	for i := 0; i < 4; i++ {
		f.rl.sweepIdle()
	}
	if len(fw.sent) != 1 || lg.count("never left idle") != 1 {
		t.Fatalf("sent %v; log:\n%s", fw.sent, lg)
	}
	n := fw.natives[7]
	n.StatusUpdatedAt = 2000
	fw.natives[7] = n
	f.rl.sweepIdle()
	if len(fw.sent) != 2 {
		t.Fatalf("a turn later: sent %v, want a second typing", fw.sent)
	}
}

// One listing and one identify per sweep, whatever the recipients.
func TestWakeOneListingPerSweep(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	twoWakeable(t, f, fw)
	f.rl.sweepIdle()
	if fw.listCalls != 1 || fw.focusCalls != 1 || len(fw.sent) != 2 {
		t.Fatalf("list %d, focus %d, sent %v", fw.listCalls, fw.focusCalls, fw.sent)
	}
}

// Review Focus 5: one log line per change of reason, not one per sweep.
func TestWakeLogsReasonOnChangeOnly(t *testing.T) {
	t.Parallel()
	f, fw, lg := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	f.sendTo(t, fw, "s-alpha")
	fw.screen = screenFixture(t, "screen-draft.txt")
	for i := 0; i < 3; i++ {
		f.rl.sweepIdle()
	}
	if lg.count("wake: s-alpha held: draft in the input") != 1 || lg.count(" held: ") != 1 {
		t.Fatalf("log:\n%s", lg)
	}
	fw.focused = surf
	f.rl.sweepIdle()
	if lg.count("wake: s-alpha held: pane has keyboard focus") != 1 || lg.count(" held: ") != 2 {
		t.Fatalf("log:\n%s", lg)
	}
}

// D26: a watcher that arrives during the cmux checks wins.
func TestWakeRechecksBeforeTyping(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	f.sendTo(t, fw, "s-alpha")
	fw.onScreen = func() { f.rl.register("s-alpha") }
	f.rl.sweepIdle()
	if len(fw.sent) != 0 {
		t.Fatalf("typed although a watcher registered: %v", fw.sent)
	}
}

func TestNoWakeWithoutDeps(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.wakePeers(t, alphaPeer())
	if rec, _ := f.send(t, pigtail.SendMessageRequest{From: "s-beta", To: "s-alpha", Body: "hello"}); rec.Code != http.StatusCreated {
		t.Fatalf("send = %d", rec.Code)
	}
	f.rl.sweepIdle() // must neither panic nor call anything
}

// --- delivery "wake" at send time (task 5) ------------------------------------

// B9, daemon side: a wakeable recipient answers "wake".
func TestDeliveryWakeWhenVerified(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	if out := f.sendTo(t, fw, "s-alpha"); out.Delivery != pigtail.DeliveryWake {
		t.Fatalf("delivery = %q, want wake", out.Delivery)
	}
}

// B10 at send time: in the start-up grace no sweep types, so the send does
// not promise a wake — the delivery keeps its pre-wake value, with no cmux
// call. Once the grace is over, the same send answers "wake".
func TestDeliveryNoWakeInStartupGrace(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	f.rl.startedAt = f.now.Add(-10 * time.Second)
	fw.resetCounts()
	rec, out := f.send(t, pigtail.SendMessageRequest{From: "s-beta", To: "s-alpha", Body: "hello"})
	if rec.Code != http.StatusCreated || out.Delivery != pigtail.DeliveryNone || fw.cmuxCalls != 0 {
		t.Fatalf("in the grace: %d, delivery %q, %d cmux calls; want 201, none, 0",
			rec.Code, out.Delivery, fw.cmuxCalls)
	}
	f.advance(25 * time.Second)
	if out := f.sendTo(t, fw, "s-alpha"); out.Delivery != pigtail.DeliveryWake {
		t.Fatalf("after the grace: delivery %q, want wake", out.Delivery)
	}
}

// A shell status (snapshot and native) was "unknown"; verified, it is "wake".
func TestDeliveryWakeShellStatus(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	n := fw.natives[7]
	n.Status = "shell"
	fw.natives[7] = n
	p := alphaPeer()
	p.Status = "shell"
	f.wakePeers(t, p)
	if out := f.sendTo(t, fw, "s-alpha"); out.Delivery != pigtail.DeliveryWake {
		t.Fatalf("delivery = %q, want wake", out.Delivery)
	}
}

// B6: another session active on the surface → "none" as before.
func TestDeliveryStaysNoneWhenUnverified(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	fw.listing = cmuxListingOf(cmuxEntry("s-alpha", surf, 7, "s-other"))
	f.wakePeers(t, alphaPeer())
	if out := f.sendTo(t, fw, "s-alpha"); out.Delivery != pigtail.DeliveryNone {
		t.Fatalf("delivery = %q, want none", out.Delivery)
	}
}

// B4: a declared wait → "none".
func TestDeliveryStaysNoneWithDeclaredWait(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	declareWait(t, f, "s-alpha", "review")
	if out := f.sendTo(t, fw, "s-alpha"); out.Delivery != pigtail.DeliveryNone {
		t.Fatalf("delivery = %q, want none", out.Delivery)
	}
}

// Review Focus 4: no pid, no config dir, no surface → "none", no cmux call.
func TestDeliveryWakeNeedsNoCmuxForUnverifiable(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, pigtail.PeerSession{SessionID: "s-alpha", Name: "alpha", Scope: "gx", Status: "idle"})
	rec, out := f.send(t, pigtail.SendMessageRequest{From: "s-beta", To: "s-alpha", Body: "hello"})
	if rec.Code != http.StatusCreated || out.Delivery != pigtail.DeliveryNone || fw.cmuxCalls != 0 {
		t.Fatalf("code %d, delivery %q, %d cmux calls", rec.Code, out.Delivery, fw.cmuxCalls)
	}
}

// One send-time listing per broadcast, never the sweep's.
func TestBroadcastWakeOneListing(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	fw.natives = map[int]claudesession.Native{
		7: {SessionID: "s-alpha", Status: "idle", Kind: "interactive", StatusUpdatedAt: 1000},
		8: {SessionID: "s-gamma", Status: "idle", Kind: "interactive", StatusUpdatedAt: 1000},
	}
	fw.listing = cmuxListingOf(cmuxEntry("s-alpha", surf, 7, "s-alpha"), cmuxEntry("s-gamma", surf2, 8, "s-gamma"))
	f.wakePeers(t, alphaPeer(), gammaPeer())
	rec := doJSON(t, f.r, "POST", "/messages/broadcast", pigtail.BroadcastRequest{From: "s-beta", Body: "maintenance at noon"})
	if rec.Code != http.StatusOK {
		t.Fatalf("broadcast = %d %s", rec.Code, rec.Body.String())
	}
	var out pigtail.BroadcastResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Results) != 2 {
		t.Fatalf("results = %+v", out.Results)
	}
	for _, r := range out.Results {
		if r.Delivery != pigtail.DeliveryWake {
			t.Fatalf("%s: delivery = %q, want wake", r.To.SessionID, r.Delivery)
		}
	}
	if fw.atSendCalls != 1 || fw.listCalls != 0 {
		t.Fatalf("send-time listings %d, sweep listings %d; want 1 and 0", fw.atSendCalls, fw.listCalls)
	}
}

// D37: the send-time listing is the real 3 s bound; a wedged cmux costs
// msg send at most that, answers "none", and queues the message once.
func TestDeliverySendListingBounded(t *testing.T) {
	stub := filepath.Join(t.TempDir(), "cmux")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(cmux.EnvBundledCLI, stub)
	f, fw, _ := newWakeFixture(t)
	f.rl.cfg.Wake.CmuxSessionsAtSend = cmux.SessionsJSONListingBounded
	f.rl.cfg.Wake.CmuxSessions = func() ([]byte, error) {
		t.Error("send must not use the sweep listing")
		return nil, errors.New("sweep listing")
	}
	_ = fw
	f.wakePeers(t, alphaPeer())
	start := time.Now()
	rec, out := f.send(t, pigtail.SendMessageRequest{From: "s-beta", To: "s-alpha", Body: "hello"})
	if took := time.Since(start); took >= 4*time.Second {
		t.Fatalf("send took %s: the send-time listing must stop at 3 s", took)
	}
	if rec.Code != http.StatusCreated || out.Delivery != pigtail.DeliveryNone {
		t.Fatalf("code %d, delivery %q; want 201 none", rec.Code, out.Delivery)
	}
	if n, _ := f.st.CountPending("s-alpha", f.now); n != 1 {
		t.Fatalf("pending = %d, want the message queued once", n)
	}
}

// Verify round 1: a send-time listing that fails is never silent — the daemon
// logs it like the sweep does; the CLI keeps the "none" answer (D29).
func TestDeliveryLogsSendListingFailure(t *testing.T) {
	t.Parallel()
	f, fw, lg := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	fw.listErr = errors.New("cmux sessions: signal: killed")
	if out := f.sendTo(t, fw, "s-alpha"); out.Delivery != pigtail.DeliveryNone {
		t.Fatalf("delivery = %q, want none", out.Delivery)
	}
	if lg.count("wake: s-alpha: cmux sessions list: cmux sessions: signal: killed") != 1 {
		t.Fatalf("send-time listing failure not logged:\n%s", lg)
	}
}

// A read-screen failure that repeats is one line per change, not per sweep.
func TestWakeRepeatedFailuresLogOnChange(t *testing.T) {
	t.Parallel()
	f, fw, lg := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	f.sendTo(t, fw, "s-alpha")
	fw.screenErr = map[string]error{surf: errors.New("cmux read-screen: signal: killed")}
	for i := 0; i < 3; i++ {
		f.rl.sweepIdle()
	}
	if lg.count("wake: s-alpha: cmux read-screen: cmux read-screen: signal: killed") != 1 {
		t.Fatalf("read-screen failure must log once per change:\n%s", lg)
	}
}

// T-f, Review Focus 4: while a cmux check types, no wake is typed and no
// cmux call is made; one log line on entry, one on exit; the sweep then
// types as before.
func TestWakeHeldWhileCheckRuns(t *testing.T) {
	t.Parallel()
	f, fw, lg := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	f.sendTo(t, fw, "s-alpha")
	running := true
	f.rl.cfg.Wake.CheckRunning = func() bool { return running }
	f.rl.sweepIdle()
	f.rl.sweepIdle()
	if len(fw.sent) != 0 || fw.cmuxCalls != 0 {
		t.Fatalf("sent %v, cmux calls %d while a check runs", fw.sent, fw.cmuxCalls)
	}
	if lg.count("wake: held — pigtail cmux check is typing into panes") != 1 {
		t.Fatalf("want one held line:\n%s", lg)
	}
	running = false
	f.rl.sweepIdle()
	if lg.count("wake: resumed — pigtail cmux check done") != 1 || len(fw.sent) != 1 {
		t.Fatalf("after the check: sent %v, log:\n%s", fw.sent, lg)
	}
}

// T-f: a check that starts during the sweep — re-read right before SendLine.
func TestWakeRechecksCheckBeforeSend(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	f.sendTo(t, fw, "s-alpha")
	calls := 0
	f.rl.cfg.Wake.CheckRunning = func() bool { calls++; return calls > 1 }
	f.rl.sweepIdle()
	if len(fw.sent) != 0 {
		t.Fatalf("sent %v: a check started before the send", fw.sent)
	}
}

// D16 (Tâche 3): a session in Standby is never woken; its message waits in
// its inbox, the refusal noted once.
func TestWakeSkipsStandbySession(t *testing.T) {
	t.Parallel()
	f, fw, lg := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	f.sendTo(t, fw, "s-alpha")
	if err := f.st.SetStandby("s-alpha", f.now); err != nil {
		t.Fatal(err)
	}
	compactedAlready(t, f, "s-alpha")
	f.rl.sweepIdle()
	f.rl.sweepIdle()
	if len(fw.sent) != 0 || fw.cmuxCalls != 0 {
		t.Fatalf("sent %v, %d cmux calls; want none", fw.sent, fw.cmuxCalls)
	}
	if n := lg.count("standby (D16)"); n != 1 {
		t.Fatalf("refusal noted %d times:\n%s", n, lg)
	}
	if n, _ := f.st.CountPending("s-alpha", f.now); n != 1 {
		t.Fatalf("pending = %d, want 1", n)
	}
}

// The Standby ends: the next sweep wakes the session.
func TestWakeResumesAfterStandbyEnds(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	f.sendTo(t, fw, "s-alpha")
	if err := f.st.SetStandby("s-alpha", f.now); err != nil {
		t.Fatal(err)
	}
	compactedAlready(t, f, "s-alpha")
	f.rl.sweepIdle()
	if len(fw.sent) != 0 {
		t.Fatalf("typed in Standby: %v", fw.sent)
	}
	if _, err := f.st.ClearStandby("s-alpha", f.now); err != nil {
		t.Fatal(err)
	}
	f.rl.sweepIdle()
	if !reflect.DeepEqual(fw.sent, []string{surf}) {
		t.Fatalf("sent = %v, want [surf]", fw.sent)
	}
}

// A send to a session in Standby promises no wake and reads no cmux listing.
func TestDeliveryStaysNoneInStandby(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	if err := f.st.SetStandby("s-alpha", f.now); err != nil {
		t.Fatal(err)
	}
	rec, out := f.send(t, pigtail.SendMessageRequest{From: "s-beta", To: "s-alpha", Body: "hello"})
	if rec.Code != http.StatusCreated || out.Delivery != pigtail.DeliveryNone {
		t.Fatalf("code %d, delivery %q, want none", rec.Code, out.Delivery)
	}
	if fw.atSendCalls != 0 || fw.cmuxCalls != 0 {
		t.Fatalf("%d send-time listings, %d cmux calls; want none", fw.atSendCalls, fw.cmuxCalls)
	}
}

// An unreadable Standby is never read as "not in Standby": no typing, the
// reason named.
func TestWakeStandbyUnreadableRefuses(t *testing.T) {
	t.Parallel()
	f, fw, lg := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	f.sendTo(t, fw, "s-alpha")
	f.rl.getStandby = func(string) (*pigtail.SessionStandby, error) { return nil, errors.New("disk gone") }
	f.rl.sweepIdle()
	if len(fw.sent) != 0 || fw.cmuxCalls != 0 {
		t.Fatalf("sent %v, %d cmux calls; want none", fw.sent, fw.cmuxCalls)
	}
	if lg.count("standby unreadable") != 1 {
		t.Fatalf("reason missing:\n%s", lg)
	}
}

// compactedAlready marks the Standby compacted: a wake test in Standby
// measures the wake, the compaction (relay_standby.go) has nothing to do.
func compactedAlready(t *testing.T, f *relayFixture, s string) {
	t.Helper()
	if err := f.st.MarkStandbyCompacted(s, f.now); err != nil {
		t.Fatal(err)
	}
}

// --- the session budget (Tâche 8) ---------------------------------------------

// fakeBudget stands in for the controller behind the WakeDeps seams: it grants
// `allow` asks (-1: every ask), refuses the sessions in deny, and records what
// the relay asked and what it published.
type fakeBudget struct {
	mu        sync.Mutex
	allow     int
	deny      map[string]bool
	acquired  []string
	kinds     []string
	released  []string
	gaugeN    []int
	gaugeAt   []time.Time
	onAcquire func(session string)
}

func (b *fakeBudget) install(w *WakeDeps) {
	w.BudgetAcquire = func(kind, session string) (bool, string) {
		b.mu.Lock()
		b.acquired = append(b.acquired, session)
		b.kinds = append(b.kinds, kind)
		hook := b.onAcquire
		grant := b.allow != 0 && !b.deny[session]
		if grant && b.allow > 0 {
			b.allow--
		}
		b.mu.Unlock()
		if hook != nil {
			hook(session)
		}
		if !grant {
			return false, "session budget full (n=6, m=6)"
		}
		return true, ""
	}
	w.BudgetRelease = func(session string) {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.released = append(b.released, session)
	}
	w.BudgetDeferred = func(n int, oldest time.Time) {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.gaugeN = append(b.gaugeN, n)
		b.gaugeAt = append(b.gaugeAt, oldest)
	}
}

func (b *fakeBudget) asked() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.acquired...)
}

// budgetFixture: s-alpha wakeable with one pending message, the budget seams
// of the fake installed with `allow` grants.
func budgetFixture(t *testing.T, allow int) (*relayFixture, *fakeWake, *wakeLog, *fakeBudget) {
	t.Helper()
	f, fw, lg := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	f.sendTo(t, fw, "s-alpha")
	b := &fakeBudget{allow: allow}
	b.install(f.rl.cfg.Wake)
	return f, fw, lg, b
}

func pendingOf(t *testing.T, f *relayFixture, s string) int {
	t.Helper()
	n, err := f.st.CountPending(s, f.clock())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestWakeDeferredWhileBudgetClosed(t *testing.T) {
	t.Parallel()
	f, fw, _, b := budgetFixture(t, 0)
	f.rl.sweepIdle()
	if len(fw.sent) != 0 {
		t.Fatalf("typed with the budget closed: %v", fw.sent)
	}
	if got := b.asked(); !reflect.DeepEqual(got, []string{"s-alpha"}) || b.kinds[0] != string(sessbudget.KindWake) {
		t.Fatalf("asked %v kinds %v, want one wake ask for s-alpha", got, b.kinds)
	}
	if n := pendingOf(t, f, "s-alpha"); n != 1 {
		t.Fatalf("pending = %d, want the message still in the inbox", n)
	}
	if len(b.released) != 0 {
		t.Fatalf("released %v: a refusal holds nothing", b.released)
	}
}

func TestWakeProceedsWhenGateOpens(t *testing.T) {
	t.Parallel()
	f, fw, _, b := budgetFixture(t, 0)
	f.rl.sweepIdle()
	b.mu.Lock()
	b.allow = 1
	b.mu.Unlock()
	f.rl.sweepIdle()
	if !reflect.DeepEqual(fw.sent, []string{surf}) {
		t.Fatalf("sent = %v, want one typing at the next pass", fw.sent)
	}
	if got := b.asked(); len(got) != 2 {
		t.Fatalf("asked %v, want one ask per pass", got)
	}
}

// A place appears between two passes: the next pass types it, once, and asks once.
func TestWakeRefusedThenPlaceExistsTypesNextPass(t *testing.T) {
	t.Parallel()
	f, fw, _, b := budgetFixture(t, 0)
	f.rl.sweepIdle()
	f.rl.sweepIdle()
	b.mu.Lock()
	b.allow = -1
	b.mu.Unlock()
	f.rl.sweepIdle()
	f.rl.sweepIdle() // typed already, never left idle: no second ask
	if len(fw.sent) != 1 || len(b.asked()) != 3 {
		t.Fatalf("sent %v, asked %v; want one typing and three asks", fw.sent, b.asked())
	}
}

func TestTwoPendingRecipientsBudgetAllowsOne(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	twoWakeable(t, f, fw)
	b := &fakeBudget{allow: 1}
	b.install(f.rl.cfg.Wake)
	f.rl.sweepIdle()
	if len(fw.sent) != 1 {
		t.Fatalf("sent = %v, want exactly one woken", fw.sent)
	}
	b.mu.Lock()
	b.allow = 1
	b.mu.Unlock()
	f.rl.sweepIdle()
	if len(fw.sent) != 2 || fw.sent[0] == fw.sent[1] {
		t.Fatalf("sent = %v, want the other one at the next pass", fw.sent)
	}
}

// Every guard before the budget: a wake a guard refuses never asks; the ask
// comes once, after the last guard, right before the send.
func TestWakeAcquireAfterLastGuard(t *testing.T) {
	t.Parallel()
	guards := map[string]func(f *relayFixture, fw *fakeWake){
		"grace": func(f *relayFixture, _ *fakeWake) { f.rl.startedAt = f.now },
		"surface": func(_ *relayFixture, fw *fakeWake) {
			fw.listing = cmuxListingOf(cmuxEntry("s-alpha", surf, 7, "s-other"))
		},
		"screen":  func(t *relayFixture, fw *fakeWake) { fw.screen = "no input box here\n" },
		"watcher": func(f *relayFixture, _ *fakeWake) { f.rl.register("s-alpha") },
		"native re-read": func(_ *relayFixture, fw *fakeWake) {
			fw.onScreen = func() { delete(fw.natives, 7) }
		},
		"check running": func(f *relayFixture, _ *fakeWake) {
			calls := 0
			f.rl.cfg.Wake.CheckRunning = func() bool { calls++; return calls > 1 }
		},
		"idle re-check": func(_ *relayFixture, fw *fakeWake) {
			fw.onScreen = func() {
				n := fw.natives[7]
				n.Status = "busy"
				fw.natives[7] = n
			}
		},
	}
	for name, mutate := range guards {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			f, fw, _, b := budgetFixture(t, -1)
			mutate(f, fw)
			f.rl.sweepIdle()
			if got := b.asked(); len(got) != 0 || len(fw.sent) != 0 {
				t.Fatalf("asked %v, sent %v: a guard refused, the budget must stay untouched", got, fw.sent)
			}
		})
	}
	t.Run("control", func(t *testing.T) {
		f, fw, _, b := budgetFixture(t, -1)
		var screens, sentAtAsk int
		b.onAcquire = func(string) { screens, sentAtAsk = fw.screenCalls, len(fw.sent) }
		f.rl.sweepIdle()
		if got := b.asked(); len(got) != 1 || len(fw.sent) != 1 {
			t.Fatalf("asked %v, sent %v; want one ask and one typing", got, fw.sent)
		}
		if screens == 0 || sentAtAsk != 0 {
			t.Fatalf("ask came with %d screen reads and %d typed: want after the guards, before the send", screens, sentAtAsk)
		}
	})
}

func TestScreenHeldWakeNeverTouchesBudget(t *testing.T) {
	t.Parallel()
	idle := screenFixture(t, "screen-idle-empty.txt")
	cases := map[string]func(fw *fakeWake){
		"focus": func(fw *fakeWake) { fw.focused = surf },
		"draft": func(fw *fakeWake) { fw.screen = screenFixture(t, "screen-draft.txt") },
		"usage_limit": func(fw *fakeWake) {
			fw.screen = strings.TrimRight(idle, "\n") + "\n  ⎿  You've hit your session limit · resets 11:40pm (Europe/Paris)\n"
		},
	}
	for name, hold := range cases {
		t.Run(name, func(t *testing.T) {
			f, fw, _, b := budgetFixture(t, 0)
			hold(fw)
			f.rl.sweepIdle()
			if got := b.asked(); len(got) != 0 {
				t.Fatalf("asked %v for a screen-held wake", got)
			}
			if len(b.gaugeN) != 1 || b.gaugeN[0] != 0 || !b.gaugeAt[0].IsZero() {
				t.Fatalf("gauge = %v %v, want one publication of zero", b.gaugeN, b.gaugeAt)
			}
		})
	}
}

func TestWakeRefusedForBudgetDoesNotBlockOtherWake(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	twoWakeable(t, f, fw)
	b := &fakeBudget{allow: -1, deny: map[string]bool{"s-alpha": true}}
	b.install(f.rl.cfg.Wake)
	f.rl.sweepIdle()
	if !reflect.DeepEqual(fw.sent, []string{surf2}) {
		t.Fatalf("sent = %v, want only s-gamma's surface", fw.sent)
	}
	if got := b.asked(); !reflect.DeepEqual(got, []string{"s-alpha", "s-gamma"}) {
		t.Fatalf("asked %v, want both in one pass", got)
	}
	b.mu.Lock()
	b.deny = nil
	b.mu.Unlock()
	f.rl.sweepIdle()
	if !reflect.DeepEqual(fw.sent, []string{surf2, surf}) {
		t.Fatalf("sent = %v, want s-alpha tried again and typed next pass", fw.sent)
	}
}

// Oldest pending message first, whatever the recipients' ids: s-gamma's
// message is the oldest, s-alpha sorts first by id.
func TestOldestPendingWakeTriedFirst(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	fw.natives = map[int]claudesession.Native{
		7: {SessionID: "s-alpha", Status: "idle", Kind: "interactive", StatusUpdatedAt: 1000},
		8: {SessionID: "s-gamma", Status: "idle", Kind: "interactive", StatusUpdatedAt: 1000},
	}
	fw.listing = cmuxListingOf(cmuxEntry("s-alpha", surf, 7, "s-alpha"), cmuxEntry("s-gamma", surf2, 8, "s-gamma"))
	f.wakePeers(t, alphaPeer(), gammaPeer())
	f.sendTo(t, fw, "s-gamma")
	f.advance(time.Minute)
	f.sendTo(t, fw, "s-alpha")
	f.advance(time.Minute)
	f.sendTo(t, fw, "s-gamma")
	b := &fakeBudget{allow: 1}
	b.install(f.rl.cfg.Wake)
	f.rl.sweepIdle()
	if got := b.asked(); !reflect.DeepEqual(got, []string{"s-gamma", "s-alpha"}) {
		t.Fatalf("asked %v, want oldest message first", got)
	}
	if !reflect.DeepEqual(fw.sent, []string{surf2}) {
		t.Fatalf("sent = %v, want the oldest recipient typed", fw.sent)
	}
}

func TestDeferredGaugePublishedPerPass(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	twoWakeable(t, f, fw)
	b := &fakeBudget{allow: 0}
	b.install(f.rl.cfg.Wake)
	t0 := f.clock()
	f.rl.sweepIdle()
	if len(b.gaugeN) != 1 || b.gaugeN[0] != 2 || b.gaugeAt[0].IsZero() || b.gaugeAt[0].After(t0) {
		t.Fatalf("gauge = %v %v, want (2, oldest message time)", b.gaugeN, b.gaugeAt)
	}
	claimVia(t, f, "s-alpha", "prompt")
	claimVia(t, f, "s-gamma", "prompt")
	f.rl.sweepIdle()
	if len(b.gaugeN) != 2 || b.gaugeN[1] != 0 || !b.gaugeAt[1].IsZero() {
		t.Fatalf("gauge = %v %v, want a zero publication for a pass with none", b.gaugeN, b.gaugeAt)
	}
	// A cmux answer that stops the pass: still published (zero deferred).
	f.sendTo(t, fw, "s-alpha")
	fw.listErr = errors.New("cmux down")
	f.rl.sweepIdle()
	if len(b.gaugeN) != 3 || b.gaugeN[2] != 0 {
		t.Fatalf("gauge = %v, want one publication per pass, the stopped one included", b.gaugeN)
	}
}

// The real controller behind the seams: n = Floor-3 busy, a pending recipient
// past what the rows admit. Row 3 admits freely below the floor; the pending
// wakes count in n at once; at n = floor the gap (row 5) refuses the rest.
func TestWakeSweepBelowFloorTypesOnlyMinusN(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	const recipients = 6
	var peers []pigtail.PeerSession
	var entries []string
	fw.natives = map[int]claudesession.Native{}
	for i := 0; i < recipients; i++ {
		id, pid, surface := fmt.Sprintf("s-w%d", i), 30+i, fmt.Sprintf("5B1E0C44-7A2D-4F3B-9C8E-1D2F3A4B5C%02d", i)
		fw.natives[pid] = claudesession.Native{SessionID: id, Status: "idle", Kind: "interactive", StatusUpdatedAt: 1000}
		peers = append(peers, pigtail.PeerSession{SessionID: id, Name: id, Scope: "gx", Status: "idle", PID: pid, ConfigDir: "/cfg", CmuxSurfaceID: surface})
		entries = append(entries, cmuxEntry(id, surface, pid, id))
	}
	fw.listing = cmuxListingOf(entries...)
	f.wakePeers(t, peers...)
	for _, p := range peers {
		f.sendTo(t, fw, p.SessionID)
	}
	clk := sessbudgettest.NewClock()
	ctl := sessbudget.NewController(sessbudgettest.Config(), clk.Now, nil)
	ctl.Observe(sessbudgettest.BelowFloor.Sample(clk.Now()))
	w := f.rl.cfg.Wake
	w.BudgetAcquire, w.BudgetRelease, w.BudgetDeferred = ctl.BudgetAcquire, ctl.BudgetRelease, ctl.SetDeferredWakes
	f.rl.sweepIdle()
	if want := sessbudgettest.Floor - sessbudgettest.BelowFloor.Busy; len(fw.sent) != want {
		t.Fatalf("typed %d wakes, want exactly Floor-n = %d: %v", len(fw.sent), want, fw.sent)
	}
	if got := ctl.Snapshot().DeferredWakes; got != recipients-len(fw.sent) {
		t.Fatalf("deferred gauge = %d, want the %d left", got, recipients-len(fw.sent))
	}
}

func TestGrantedButUntypedReleases(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	f.sendTo(t, fw, "s-alpha")
	clk := sessbudgettest.NewClock()
	ctl := sessbudget.NewController(sessbudgettest.Config(), clk.Now, nil)
	ctl.Observe(sessbudgettest.AboveFloor.Sample(clk.Now()))
	var released []string
	w := f.rl.cfg.Wake
	w.BudgetAcquire, w.BudgetDeferred = ctl.BudgetAcquire, ctl.SetDeferredWakes
	w.BudgetRelease = func(s string) { released = append(released, s); ctl.BudgetRelease(s) }
	fw.sendErr = errors.New("cmux send failed")
	f.rl.sweepIdle()
	if !reflect.DeepEqual(released, []string{"s-alpha"}) {
		t.Fatalf("released %v, want the untyped grant given back", released)
	}
	// AboveFloor is gap-bound: a grant left standing would refuse this ask.
	fw.sendErr = nil
	f.rl.sweepIdle()
	if len(fw.sent) != 2 {
		t.Fatalf("sent %v: the gap was consumed by a wake that was never typed", fw.sent)
	}
	if got := ctl.Snapshot().DeferredWakes; got != 0 {
		t.Fatalf("deferred = %d, nothing is re-queued", got)
	}
}

func TestCompactionNeverAcquires(t *testing.T) {
	t.Parallel()
	f, fw, _ := standbyWakeFixture(t)
	b := &fakeBudget{allow: 0}
	b.install(f.rl.cfg.Wake)
	f.rl.sweepIdle()
	if !reflect.DeepEqual(fw.compacts, []string{surf}) {
		t.Fatalf("compacts = %v, want /compact typed with the budget closed", fw.compacts)
	}
	if got := b.asked(); len(got) != 0 || len(b.released) != 0 {
		t.Fatalf("asked %v released %v: /compact passes no admission", got, b.released)
	}
}

// sendWake answers « wake » at send time and never asks the budget; the sweep
// is the only asker (the same path serves deliverAnswer).
func TestSendWakeMakesNoBudgetCall(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	b := &fakeBudget{allow: 0}
	b.install(f.rl.cfg.Wake)
	out := f.sendTo(t, fw, "s-alpha")
	if out.Delivery != "wake" {
		t.Fatalf("delivery = %q, want wake (decision logged: no new delivery value)", out.Delivery)
	}
	if got := b.asked(); len(got) != 0 {
		t.Fatalf("send asked the budget: %v", got)
	}
	if !f.rl.sendWake("s-alpha", newListing(f.rl.cfg.Wake.CmuxSessionsAtSend)) || len(b.asked()) != 0 {
		t.Fatalf("sendWake must answer true without a budget call")
	}
}

func TestUserAnswerWakesDespiteClosedBudget(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	seedHumanMessage(t, f, "m-human", "s-alpha")
	fw.resetCounts()
	b := &fakeBudget{allow: 0}
	b.install(f.rl.cfg.Wake)
	f.rl.sweepIdle()
	if !reflect.DeepEqual(fw.sent, []string{surf}) || len(b.asked()) != 0 {
		t.Fatalf("sent %v, asked %v: Ada' answer skips the budget", fw.sent, b.asked())
	}
}

// The exemption reads the store, no in-memory set: a fresh relay over the
// same store still wakes.
func TestUserAnswerWakesAfterStoreReload(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	seedHumanMessage(t, f, "m-human", "s-alpha")
	fresh := NewRelay(f.rl.svc, f.rl.cfg)
	fresh.startedAt = f.now.Add(-time.Hour)
	b := &fakeBudget{allow: 0}
	b.install(fresh.cfg.Wake)
	fresh.sweepIdle()
	if !reflect.DeepEqual(fw.sent, []string{surf}) || len(b.asked()) != 0 {
		t.Fatalf("sent %v, asked %v after a reload", fw.sent, b.asked())
	}
}

func TestWakeDeferralIsLogged(t *testing.T) {
	t.Parallel()
	f, _, lg, _ := budgetFixture(t, 0)
	f.rl.sweepIdle()
	f.rl.sweepIdle()
	if lg.count("wake: s-alpha held: session budget full (n=6, m=6)") != 1 {
		t.Fatalf("want the deferral reason logged once per change:\n%s", lg)
	}
}

// Explicit, never silent: no budget seam = no gate (every handler test, and a
// daemon built without the controller).
func TestNilBudgetAcquireIsFailOpen(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	f.sendTo(t, fw, "s-alpha")
	if f.rl.cfg.Wake.BudgetAcquire != nil || f.rl.cfg.Wake.BudgetRelease != nil || f.rl.cfg.Wake.BudgetDeferred != nil {
		t.Fatal("fixture must leave the budget seams nil")
	}
	f.rl.sweepIdle()
	if len(fw.sent) != 1 {
		t.Fatalf("sent %v, want a typing with no budget seam", fw.sent)
	}
}

func TestKickWakeRunsOneEarlyPass(t *testing.T) {
	t.Parallel()
	f, fw, _, b := budgetFixture(t, 0) // refused: every pass asks once, so asks count passes
	f.rl.cfg.SweepInterval = time.Hour
	var late atomic.Bool
	f.rl.cfg.Now = func() time.Time {
		if late.Load() {
			return f.clock().Add(time.Minute) // past the start-up grace
		}
		return f.clock()
	}
	entered, release := make(chan struct{}, 16), make(chan struct{})
	first := sync.OnceFunc(func() { <-release })
	b.onAcquire = func(string) { entered <- struct{}{}; first() }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.rl.Run(ctx); close(done) }()
	for { // Run has set its start: only then does the clock move on
		f.rl.wakeMu.Lock()
		started := f.rl.startedAt.Equal(f.clock()) // the fixture's earlier value is an hour back
		f.rl.wakeMu.Unlock()
		if started {
			break
		}
		time.Sleep(time.Millisecond)
	}
	late.Store(true)
	wait := func(what string) {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("no ask: %s", what)
		}
	}
	f.rl.KickWake()
	wait("the first kick ran no pass before the ticker")
	// Kicks during that pass: any number costs at most one more pass, and none blocks.
	finished := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			f.rl.KickWake()
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("KickWake blocked on a full channel")
	}
	close(release)
	wait("the kicks during a pass ran no extra pass")
	f.rl.KickWake()
	wait("the loop stopped after the coalesced pass")
	cancel()
	<-done
	if got := len(b.asked()); got != 3 {
		t.Fatalf("asked %d times, want 3 (first kick, one coalesced, one more)", got)
	}
	if len(fw.sent) != 0 {
		t.Fatalf("sent %v with the budget closed", fw.sent)
	}
}
