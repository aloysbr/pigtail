package handlers

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/peers"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// RelayConfig configures the session-message relay.
type RelayConfig struct {
	// MarkerDir holds one file per session with pending messages, named after
	// the session id. The recipient's prompt/tool hooks test for that file
	// before doing anything else, so a session with an empty inbox pays one
	// stat per hook and never reaches the daemon. Empty disables markers.
	MarkerDir string
	// WaitMarkerDir holds one file per session with a recorded wait (spec
	// §4.8), named after the session id: the UserPromptSubmit hook clears a
	// wait only when the file exists, so a session that waits for nothing
	// pays one stat per prompt. Empty disables wait markers.
	WaitMarkerDir string
	// StandbyMarkerDir holds one file per session in Standby
	// (sub_x000000000000041), named after the session id: the hooks' and the
	// CLI's reading of the session_standby table (peers_standby.go). Empty
	// disables Standby markers.
	StandbyMarkerDir string
	// SweepInterval is how often pending messages are expired. Default 1 min.
	SweepInterval time.Duration
	// Now and Alive are seams for tests.
	Now   func() time.Time
	Alive func(pigtail.PeerSession) bool
	// DirExists reports whether a peer's last worktree still exists; a path
	// whose worktree was deleted is not an address. Seam for tests.
	DirExists func(string) bool
	// Wake types the wake line (relay.WakeLine) in an idle, unwatched recipient's
	// cmux pane at each sweep (relay_wake.go). nil = no wake.
	Wake *WakeDeps
	// IdlePause is how long a Claude session must have been idle before
	// pigtaild stops its process (relay_pause.go, sub_x000000000000036).
	// 0 = off.
	IdlePause time.Duration
	// DoneCommand is the user's slash command that closes a finished session
	// (settings.yaml session.done_command): the idle wrap types it into an
	// idle session whose declared wait is done (relay_wrap.go). "" = the idle
	// wrap is off: pigtaild types nothing into such a session.
	DoneCommand string
	// RelaunchDir is the `c` launcher's directory: a session's .sid must
	// live there (default relaunchfile.Dir).
	RelaunchDir string
	// Reveal brings a cmux workspace (UUID) to the front for POST
	// /peers/{session}/reveal (reveal.go); nil = the real cmux. Seam for tests.
	Reveal func(workspaceID string) error
	// DraftDir holds the handoff drafts the Stop hook writes, one per session
	// (internal/handoffdraft): the directory of the socket plus
	// "handoff-draft", as the hook resolves it. Empty disables the automatic
	// handoff (relay_handoff_auto.go).
	DraftDir string
}

// Relay is the daemon side of the session-message relay: the long-poll
// waiters, the inbox markers and the expiry sweep. Its state is in memory
// only; messages themselves live in the store.
type Relay struct {
	svc *Services
	cfg RelayConfig

	mu      sync.Mutex
	waiters map[string]*waiter // one per session: a new waiter supersedes the old
	// lastSeen is when a waiter of the session last registered or left. A
	// waiter only exists while a long-poll is open, so between two polls
	// "no waiter" does not mean "no watcher".
	lastSeen map[string]time.Time

	// Idle wake state (relay_wake.go). Its own lock, never rl.mu: the sweep
	// calls watched() (rl.mu) between its reads of this state.
	wakeMu     sync.Mutex
	startedAt  time.Time          // Run's start: the wake's start-up grace
	typed      map[string]typedAt // the last typing per session, until claimed or nothing pending
	lastReason map[string]string  // the last refusal logged per session
	checkHeld  bool               // a cmux check held the last sweep (T-f)
	inboxHeld  map[string]string  // the last Standby hold noted per session by GET /messages/wait (messages.go)
	// compactReason: the last compaction refusal logged per session in
	// Standby (relay_standby.go), apart from lastReason, which forgetWakes
	// clears at every sweep for a session with nothing pending.
	compactReason map[string]string
	// pauseReason: the last idle-pause refusal logged per session
	// (relay_pause.go). The pause's durable state is session_pause.
	pauseReason map[string]string
	// wrapReason: the last idle-wrap refusal logged per session
	// (relay_wrap.go), apart from lastReason, which forgetWakes clears.
	wrapReason map[string]string
	// retryReason: the last auto-retry refusal logged per session
	// (relay_retry.go).
	retryReason map[string]string

	// listJournal and addJournal are test seams for the idle wrap's journal
	// markers (nil = the store).
	listJournal func(store.JournalFilter) ([]pigtail.JournalEntry, error)
	addJournal  func(*pigtail.JournalEntry) (bool, error)

	// getStandby is a test seam for inStandby's one read (nil = the store).
	getStandby func(string) (*pigtail.SessionStandby, error)
	// appendLogOnce is a test seam for the automatic handoff's one write
	// (nil = the store's AppendLogOnce).
	appendLogOnce func(*pigtail.SubjectLogEntry) (bool, error)
	// goneSince: when the tick first saw gone a session holding an unread
	// answer (rekeepGoneRecipients); its own lock.
	goneMu    sync.Mutex
	goneSince map[string]time.Time
	// draftGoneSince: the same for the handoff drafts (sweepHandoffDrafts),
	// kept apart from goneSince, which rekeepGoneRecipients replaces whole.
	draftGoneSince map[string]draftGoneSeen

	// wakeKick asks Run for one extra wake pass (KickWake); capacity 1, so
	// any number of kicks during a pass cost at most one more.
	wakeKick chan struct{}

	// afterAnswerWrite is a test seam run between an answer's write and its
	// delivery (nil = nothing): the recipient may end in between.
	afterAnswerWrite func(waitID string)
}

// watchGrace is how long after its last waiter closed a session still counts
// as watched. The Stop watcher re-polls at once after a timeout and 10 s after
// an error (waitRetry, cmd/pigtail/hook_relay.go); 15 s covers both.
const watchGrace = 15 * time.Second

type waiter struct {
	wake       chan struct{} // buffered 1: a message arrived
	superseded chan struct{} // closed when a newer waiter took over
}

// NewRelay builds the relay over svc.
func NewRelay(svc *Services, cfg RelayConfig) *Relay {
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.Alive == nil {
		cfg.Alive = peers.Alive
	}
	if cfg.DirExists == nil {
		cfg.DirExists = func(p string) bool { st, err := os.Stat(p); return err == nil && st.IsDir() }
	}
	if cfg.RelaunchDir == "" {
		cfg.RelaunchDir = defaultRelaunchDir()
	}
	if cfg.SweepInterval <= 0 {
		cfg.SweepInterval = time.Minute
	}
	return &Relay{svc: svc, cfg: cfg, waiters: map[string]*waiter{}, lastSeen: map[string]time.Time{},
		typed: map[string]typedAt{}, lastReason: map[string]string{}, compactReason: map[string]string{},
		pauseReason: map[string]string{}, wrapReason: map[string]string{}, retryReason: map[string]string{}, wakeKick: make(chan struct{}, 1)}
}

// IdlePauseForTest returns the idle pause threshold the relay was built with
// (0 = off), for the daemon's wiring test (TestServerWiresIdlePause).
func (rl *Relay) IdlePauseForTest() time.Duration { return rl.cfg.IdlePause }

// RelaunchDirForTest returns the launcher directory the pause checks a .sid
// path against, for the daemon's wiring test.
func (rl *Relay) RelaunchDirForTest() string { return rl.cfg.RelaunchDir }

// DraftDirForTest returns the handoff draft directory the relay was built
// with, for the daemon's wiring test (TestServerWiresHandoffDraftDir).
func (rl *Relay) DraftDirForTest() string { return rl.cfg.DraftDir }

// RegisterRelayRoutes wires the /peers and /messages routes.
func RegisterRelayRoutes(r chi.Router, rl *Relay, resp Responder) {
	// Every route answers for the caller's scope (X-Pigtail-Scope, absent =
	// the default scope): the reads filter by it, and the session-bound routes (send,
	// claim, wait, ack) act for a session only when its registered scope is
	// the caller's. See relay_scope.go.
	scoped := r.With(scopeMW(resp))
	scoped.Post("/peers", rl.PostPeers(resp))
	scoped.Get("/peers", rl.GetPeers(resp))
	scoped.Post("/messages", rl.SendMessage(resp))
	scoped.Post("/messages/broadcast", rl.BroadcastMessage(resp))
	scoped.Get("/messages", rl.ListMessages(resp))
	scoped.Post("/messages/claim", rl.ClaimMessages(resp))
	scoped.Get("/messages/wait", rl.WaitMessages(resp))
	scoped.Get("/messages/{id}", rl.GetMessage(resp))
	scoped.Post("/messages/{id}/ack", rl.AckMessage(resp))
	scoped.Post("/peers/{session}/wait", rl.SetSessionWait(resp))
	scoped.Delete("/peers/{session}/wait", rl.ClearSessionWait(resp))
	// Standby (peers_standby.go, sub_x000000000000041)
	scoped.Put("/peers/{session}/standby", rl.SetSessionStandby(resp))
	scoped.Delete("/peers/{session}/standby", rl.ClearSessionStandby(resp))
	// turn ends by skill (session_handbacks.go, D11/D12)
	scoped.Post("/peers/{session}/skill", rl.SetSessionSkill(resp))
	scoped.Post("/peers/{session}/turn", rl.PostSessionTurn(resp))
	// « Aller à la session » (reveal.go, sub_x000000000000025): its cmux tab to the front
	scoped.Post("/peers/{session}/reveal", rl.RevealSession(resp))
	scoped.Get("/session-handbacks", rl.SessionHandbacks(resp))
	// enriched waits (waits.go, sub_x000000000000026)
	scoped.Get("/waits", rl.ListWaits(resp))
	scoped.Get("/waits/{id}", rl.GetWait(resp))
	scoped.Post("/waits/{id}/answer", rl.AnswerWait(resp)) // waits_answer.go
	scoped.Post("/waits/{id}/snooze", rl.SnoozeWait(resp)) // waits.go
}

// Run expires overdue messages on a clock, then wakes idle recipients
// (relay_wake.go) and compacts sessions in Standby (relay_standby.go), and rebuilds the inbox markers once at start (a wiped
// marker dir must not hide a pending message).
func (rl *Relay) Run(ctx context.Context) {
	rl.wakeMu.Lock()
	rl.startedAt = rl.cfg.Now()
	rl.wakeMu.Unlock()
	rl.syncMarkers()
	rl.sweepHandoffDrafts(rl.cfg.Now(), true) // ended peers promoted now; the "gone" are seen once
	rl.purgeWaits()                           // closed wait records past their retention (G3)
	rl.deliverKeptAnswers("", true)           // D29-C: answers kept for a subject now running
	t := time.NewTicker(rl.cfg.SweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rl.tick()
		case <-rl.wakeKick:
			// The session budget freed a place (sessbudget OnFreed): one wake
			// pass now, in this goroutine, so it never overlaps tick.
			rl.sweepIdle()
		}
	}
}

// KickWake asks Run for one wake pass before the next tick. It never blocks:
// a kick already waiting absorbs this one.
func (rl *Relay) KickWake() {
	select {
	case rl.wakeKick <- struct{}{}:
	default:
	}
}

// SweepHandoffDraftsForTest runs one handoff draft sweep, for the end-to-end
// test of cmd/pigtail (which cannot reach the unexported method).
func (rl *Relay) SweepHandoffDraftsForTest(startup bool) {
	rl.sweepHandoffDrafts(rl.cfg.Now(), startup)
}

// WakeKickPendingForTest reports a kick waiting for Run, for the daemon's
// wiring test (TestServerWiresFreedToKickWake).
func (rl *Relay) WakeKickPendingForTest() bool { return len(rl.wakeKick) > 0 }

// tick is one sweep of Run: expire, purge, keep again the answers of a
// recipient gone (waits_subject.go), hand the kept answers on, then the idle
// wake and the Standby compaction.
func (rl *Relay) tick() {
	rl.expire()
	rl.purgeWaits()
	rl.rekeepGoneRecipients()
	rl.sweepHandoffDrafts(rl.cfg.Now(), false)
	rl.deliverKeptAnswers("", true)
	rl.sweepIdle()
}

// --- waiters ----------------------------------------------------------------

func (rl *Relay) register(session string) *waiter {
	w := &waiter{wake: make(chan struct{}, 1), superseded: make(chan struct{})}
	now := rl.cfg.Now() // before rl.mu: the clock seam may take its own lock
	rl.mu.Lock()
	if old, ok := rl.waiters[session]; ok {
		close(old.superseded)
	}
	rl.waiters[session] = w
	rl.lastSeen[session] = now
	rl.mu.Unlock()
	return w
}

func (rl *Relay) unregister(session string, w *waiter) {
	now := rl.cfg.Now()
	rl.mu.Lock()
	if rl.waiters[session] == w {
		delete(rl.waiters, session)
	}
	rl.lastSeen[session] = now
	rl.mu.Unlock()
}

// watched reports whether a Stop watcher serves the session: a waiter is
// open, or one closed less than watchGrace ago (between two polls).
func (rl *Relay) watched(session string) bool {
	now := rl.cfg.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if rl.waiters[session] != nil {
		return true
	}
	t, ok := rl.lastSeen[session]
	return ok && now.Sub(t) <= watchGrace
}

// delivery classifies how the recipient will see a message just queued
// (pigtail.Delivery*). status is Claude's native status from the registry
// (sessions/<pid>.json: busy|idle|waiting), refreshed by the CLI snapshot
// right before every send. Anything else — empty, or a value Claude adds
// later — is "unknown": the sender warns rather than stay silent.
func (rl *Relay) delivery(session, status string) string {
	if rl.watched(session) {
		return pigtail.DeliveryWaiter
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "busy", "waiting":
		return pigtail.DeliveryBusy
	case "idle":
		return pigtail.DeliveryNone
	default:
		return pigtail.DeliveryUnknown
	}
}

// pruneSeen forgets sessions whose last waiter left longer than watchGrace
// ago: they read as unwatched either way.
func (rl *Relay) pruneSeen(now time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for s, t := range rl.lastSeen {
		if rl.waiters[s] == nil && now.Sub(t) > watchGrace {
			delete(rl.lastSeen, s)
		}
	}
}

func (rl *Relay) wake(session string) {
	rl.mu.Lock()
	w := rl.waiters[session]
	rl.mu.Unlock()
	if w == nil {
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// --- markers ----------------------------------------------------------------

func validSessionKey(s string) bool {
	return s != "" && !strings.ContainsAny(s, `/\`) && !strings.Contains(s, "..") && len(s) <= 128
}

// refreshMarker makes the marker of a session reflect its pending count:
// present (holding the count) when > 0, absent otherwise.
func (rl *Relay) refreshMarker(session string) int {
	n, err := rl.svc.Store.CountPending(session, rl.cfg.Now())
	if err != nil {
		rl.svc.logf("relay: count pending: %v", err)
		return 0
	}
	if rl.cfg.MarkerDir == "" || !validSessionKey(session) {
		return n
	}
	path := filepath.Join(rl.cfg.MarkerDir, session)
	if n == 0 {
		_ = os.Remove(path)
		return 0
	}
	if err := os.MkdirAll(rl.cfg.MarkerDir, 0o700); err != nil {
		rl.svc.logf("relay: marker dir: %v", err)
		return n
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(n)+"\n"), 0o600); err != nil {
		rl.svc.logf("relay: write marker: %v", err)
	}
	return n
}

func (rl *Relay) syncMarkers() {
	rl.syncWaitMarkers()
	rl.syncStandbyMarkers()
	if rl.cfg.MarkerDir == "" {
		return
	}
	pending, err := rl.svc.Store.PendingRecipients(rl.cfg.Now())
	if err != nil {
		rl.svc.logf("relay: pending recipients: %v", err)
		return
	}
	keep := map[string]bool{}
	for _, s := range pending {
		keep[s] = true
		rl.refreshMarker(s)
	}
	entries, _ := os.ReadDir(rl.cfg.MarkerDir)
	for _, e := range entries {
		if !e.IsDir() && !keep[e.Name()] && !strings.HasPrefix(e.Name(), ".") {
			_ = os.Remove(filepath.Join(rl.cfg.MarkerDir, e.Name()))
		}
	}
}

// --- expiry -----------------------------------------------------------------

// expire moves overdue pending messages to expired and tells each (non-system)
// sender with a system info message, so a request nobody read does not wait
// for an answer forever.
func (rl *Relay) expire() {
	now := rl.cfg.Now()
	rl.pruneSeen(now)
	due, err := rl.svc.Store.ExpireDue(now)
	if err != nil {
		rl.svc.logf("relay: expire: %v", err)
		return
	}
	touched := map[string]bool{}
	for _, m := range due {
		touched[m.ToSession] = true
		rl.publish(pigtail.EventMessageExpired, m)
		if pigtail.NonPeerSender(m.FromSession) || m.Kind == pigtail.MessageKindNotice {
			// a notice is one-way: its sender learnt each outcome at send time
			// (C3); the relay and the user are no session to tell.
			continue
		}
		notice := pigtail.Message{
			ID:          genID(pigtail.PrefixMessage),
			ThreadID:    m.ThreadID,
			InReplyTo:   m.ID,
			FromSession: pigtail.SystemSender,
			ToSession:   m.FromSession,
			FromLabel:   "pigtail",
			ToLabel:     m.FromLabel,
			SubjectID:   m.SubjectID,
			Kind:        pigtail.MessageKindInfo,
			Body: "Your message " + m.ID + " to " + m.ToLabel + " expired unread (sent " +
				m.CreatedAt.Format(time.RFC3339) + "): \"" + relay.FirstLine(m.Body, 120) + "\". " +
				"Nobody saw it — if it still matters, tell " + rl.svc.humanName() + ".",
			CreatedAt: now,
			ExpiresAt: now.Add(relay.DefaultTTL),
		}
		if err := rl.svc.Store.CreateMessage(&notice); err != nil {
			rl.svc.logf("relay: expiry notice: %v", err)
			continue
		}
		touched[notice.ToSession] = true
		rl.publish(pigtail.EventMessageCreated, notice)
		rl.wake(notice.ToSession)
	}
	for s := range touched {
		rl.refreshMarker(s)
	}
}

// conversationScope is the one scope every session of m (the system sender
// aside) runs on; the master when they differ or one is unknown — then only
// the master's subscribers see the event.
func (rl *Relay) conversationScope(m pigtail.Message) scope.Scope {
	// A notice belongs to its recipient's scope (D1): that scope's subscribers
	// see its events, the sender redacted by publish; the master sees all.
	if m.Kind == pigtail.MessageKindNotice {
		p, err := rl.svc.Store.GetPeer(m.ToSession)
		if err != nil {
			return scope.Master()
		}
		return sessionScope(*p)
	}
	var sc scope.Scope
	for _, s := range []string{m.FromSession, m.ToSession} {
		if pigtail.NonPeerSender(s) {
			continue
		}
		p, err := rl.svc.Store.GetPeer(s)
		if err != nil {
			return scope.Master()
		}
		ps := sessionScope(*p)
		if sc != "" && ps != sc {
			return scope.Master()
		}
		sc = ps
	}
	if sc == "" {
		return scope.Master()
	}
	return sc
}

// publish emits a message event tagged with its conversation's scope (see
// pigtail.Event.Scope). An event never names a subject its readers cannot see.
func (rl *Relay) publish(t pigtail.EventType, m pigtail.Message) {
	if rl.svc.Bus == nil {
		return
	}
	sc := rl.conversationScope(m)
	one := []pigtail.Message{m}
	rl.redactMessages(sc, one)
	m = one[0]
	rl.svc.Bus.Publish(pigtail.Event{Type: t, Scope: string(sc), Data: map[string]interface{}{
		"id": m.ID, "thread_id": m.ThreadID, "kind": m.Kind, "state": m.State,
		"from_session": m.FromSession, "to_session": m.ToSession,
		"from_label": m.FromLabel, "to_label": m.ToLabel, "subject_id": m.SubjectID,
	}})
}
