package staffing

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	tPerso = "perso"
	tWork  = "globex"
)

var t0 = time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)

// ranked builds a perso-queue subject; created_at follows the rank so the
// input order is the rank order.
func ranked(id string, rank int, status string) store.RankedSubject {
	return store.RankedSubject{Subject: pigtail.Subject{
		ID: id, Title: "title " + id, ShortTitle: "short " + id, Status: status, Rank: rank, Scope: tPerso,
		CreatedAt: t0.Add(time.Duration(rank) * time.Minute),
	}, BlockedBy: []string{}}
}

func inScope(r store.RankedSubject, sc string) store.RankedSubject {
	r.Scope = sc
	return r
}

var pidSeq atomic.Int64

// livePeer is a verifiable live session (distinct pid, config dir) on sub,
// native status busy: a session that works holds a place. Tests that need
// another status use peerStatus; no test sets busy by hand.
func livePeer(session, sub string) pigtail.PeerSession {
	return pigtail.PeerSession{SessionID: session, SubjectID: sub, PID: int(100 + pidSeq.Add(1)), ConfigDir: "/cfg", Scope: tPerso, Status: "busy"}
}

func peerStatus(session, sub, status string) pigtail.PeerSession {
	p := livePeer(session, sub)
	p.Status = status
	return p
}

func inputs(queue ...store.RankedSubject) Inputs {
	return Inputs{Now: t0, Queues: map[string][]store.RankedSubject{tPerso: queue}}
}

func skipOf(p Plan, id string) (string, string, bool) {
	for _, q := range p.Queues {
		for _, c := range q {
			if c.SubjectID == id {
				return c.Skip, c.Detail, true
			}
		}
	}
	return "", "", false
}

func pickID(p Plan) string {
	if p.Pick == nil {
		return ""
	}
	return p.Pick.SubjectID
}

func TestComputeSkipsHeldRankOne(t *testing.T) {
	in := inputs(ranked("s1", 1, pigtail.StatusBuilding), ranked("s2", 2, pigtail.StatusBuilding))
	in.Peers = []pigtail.PeerSession{livePeer("p1", "s1")}
	in.AllPeers = in.Peers
	p := Compute(in)
	if pickID(p) != "s2" {
		t.Fatalf("Pick = %q, want s2", pickID(p))
	}
	if len(p.Skipped) != 1 || p.Skipped[0].SubjectID != "s1" || p.Skipped[0].Skip != pigtail.StaffingSkipHeld {
		t.Fatalf("Skipped = %+v, want s1 held", p.Skipped)
	}
	if len(p.Places) != 1 || p.Places[0].SessionID != "p1" {
		t.Fatalf("Places = %+v", p.Places)
	}
}

func TestComputeWaitingOnUserFreesPlace(t *testing.T) {
	in := inputs(ranked("s1", 1, pigtail.StatusBuilding), ranked("s2", 2, pigtail.StatusBuilding), ranked("s3", 3, pigtail.StatusBuilding))
	in.Peers = []pigtail.PeerSession{peerStatus("p1", "s1", "idle")}
	in.AllPeers = in.Peers
	in.Waits = map[string][]pigtail.SessionWait{"p1": {{Type: "review", Source: "declared", Since: t0}}}
	in.OpenWaits = map[string]string{"s2": "arbitrage"} // its session ended, the wait stays open
	p := Compute(in)
	if len(p.Places) != 0 {
		t.Fatalf("Places = %+v, want none (idle waiting on Ada)", p.Places)
	}
	if s, _, _ := skipOf(p, "s1"); s != pigtail.StaffingSkipHeld {
		t.Fatalf("s1 skip = %q, want held", s)
	}
	if s, d, _ := skipOf(p, "s2"); s != pigtail.StaffingSkipWaitsHuman || d != "arbitrage" {
		t.Fatalf("s2 skip = %q/%q, want waits_human/arbitrage", s, d)
	}
	if pickID(p) != "s3" {
		t.Fatalf("Pick = %q, want s3", pickID(p))
	}
}

func TestComputeIdlePeerHoldsSubjectWithoutPlace(t *testing.T) {
	for _, st := range []string{"idle", "waiting"} {
		in := inputs(ranked("s1", 1, pigtail.StatusBuilding), ranked("s2", 2, pigtail.StatusBuilding))
		in.Peers = []pigtail.PeerSession{peerStatus("p1", "s1", st)}
		in.AllPeers = in.Peers
		p := Compute(in)
		if len(p.Places) != 0 {
			t.Fatalf("%s: Places = %+v, want none", st, p.Places)
		}
		if s, _, _ := skipOf(p, "s1"); s != pigtail.StaffingSkipHeld {
			t.Fatalf("%s: s1 skip = %q, want held", st, s)
		}
		if pickID(p) != "s2" {
			t.Fatalf("%s: Pick = %q, want s2", st, pickID(p))
		}
	}
	// Everything else counts: busy, shell, unreadable (""), unknown, a phantom.
	phantom := peerStatus("p1", "s1", "idle")
	phantom.PID = 0
	for name, peer := range map[string]pigtail.PeerSession{
		"busy": peerStatus("p1", "s1", "busy"), "shell": peerStatus("p1", "s1", "shell"),
		"unreadable": peerStatus("p1", "s1", ""), "unknown": peerStatus("p1", "s1", "foo"), "phantom": phantom,
	} {
		in := inputs(ranked("s1", 1, pigtail.StatusBuilding))
		in.Peers = []pigtail.PeerSession{peer}
		in.AllPeers = in.Peers
		if p := Compute(in); len(p.Places) != 1 {
			t.Fatalf("%s: Places = %+v, want one", name, p.Places)
		}
	}
}

// Some legs pass on the base too (idle + grace still open): they guard
// against an implementation with no grace or no native status.
func TestComputeJustLaunchedIdlePeerKeepsPlace(t *testing.T) {
	run := func(l pigtail.SessionLaunch, status string) Plan {
		in := inputs(ranked("s", 1, pigtail.StatusBuilding))
		l.SubjectID = "s"
		in.Launches = []pigtail.SessionLaunch{l}
		in.Peers = []pigtail.PeerSession{peerStatus("p1", "s", status)}
		in.AllPeers = in.Peers
		return Compute(in)
	}
	if p := run(pigtail.SessionLaunch{LaunchID: "ln", State: pigtail.LaunchPending}, "idle"); len(p.Places) != 1 {
		t.Fatalf("pending + idle: Places = %+v, want one", p.Places)
	}
	fresh := pigtail.SessionLaunch{LaunchID: "ln", State: pigtail.LaunchLaunched, UpdatedAt: t0.Add(-LaunchGrace + time.Second)}
	if p := run(fresh, "idle"); len(p.Places) != 1 {
		t.Fatalf("launched within grace + idle: Places = %+v, want one", p.Places)
	}
	old := pigtail.SessionLaunch{LaunchID: "ln", State: pigtail.LaunchLaunched, UpdatedAt: t0.Add(-LaunchGrace - time.Second)}
	if p := run(old, "idle"); len(p.Places) != 0 {
		t.Fatalf("launched after grace + idle: Places = %+v, want none", p.Places)
	}
	if p := run(old, "foo"); len(p.Places) != 1 {
		t.Fatalf("unknown status: Places = %+v, want one", p.Places)
	}
}

func TestComputeDoneSessionFreesPlace(t *testing.T) {
	for _, w := range []string{"done", "wrapped"} {
		in := inputs(ranked("s1", 1, pigtail.StatusBuilding))
		in.Peers = []pigtail.PeerSession{livePeer("p1", "s1")}
		in.AllPeers = in.Peers
		in.Waits = map[string][]pigtail.SessionWait{"p1": {{Type: w, Source: "declared", Since: t0}}}
		p := Compute(in)
		if len(p.Places) != 0 {
			t.Fatalf("%s: Places = %+v, want none", w, p.Places)
		}
		if s, _, _ := skipOf(p, "s1"); s != pigtail.StaffingSkipHeld {
			t.Fatalf("%s: s1 skip = %q, want held", w, s)
		}
	}
}

func TestComputeEligibility(t *testing.T) {
	in := inputs(
		ranked("u", 1, pigtail.StatusUnderstanding),
		ranked("d", 2, pigtail.StatusDesigning),
		ranked("b", 3, pigtail.StatusBuilding),
		ranked("i", 4, pigtail.StatusInbox),
		ranked("pk", 5, pigtail.StatusParked),
		ranked("c", 6, pigtail.StatusCaptured),
		ranked("z", 0, pigtail.StatusBuilding),
	)
	p := Compute(in)
	var ids []string
	for _, c := range p.Queues[tPerso] {
		ids = append(ids, c.SubjectID)
	}
	if strings.Join(ids, ",") != "u,d,b" {
		t.Fatalf("queue = %v, want u,d,b", ids)
	}
	if pickID(p) != "u" {
		t.Fatalf("Pick = %q, want u", pickID(p))
	}
}

func TestComputeDepsSkip(t *testing.T) {
	a := ranked("a", 1, pigtail.StatusBuilding)
	a.BlockedBy = []string{"x", "y"}
	p := Compute(inputs(a, ranked("b", 2, pigtail.StatusBuilding)))
	s, d, _ := skipOf(p, "a")
	if s != pigtail.StaffingSkipDeps || !strings.Contains(d, "x") || !strings.Contains(d, "y") {
		t.Fatalf("a skip = %q/%q, want deps with x and y", s, d)
	}
	if pickID(p) != "b" {
		t.Fatalf("Pick = %q, want b", pickID(p))
	}
}

func TestComputeCooldown(t *testing.T) {
	in := inputs(ranked("s", 1, pigtail.StatusBuilding))
	in.Cooldown = map[string]time.Time{"s": t0.Add(5 * time.Minute)}
	p := Compute(in)
	s, d, _ := skipOf(p, "s")
	if s != pigtail.StaffingSkipCooldown || d != t0.Add(5*time.Minute).Format("15:04") {
		t.Fatalf("skip = %q/%q, want cooldown/%s", s, d, t0.Add(5*time.Minute).Format("15:04"))
	}
	in.Cooldown = map[string]time.Time{"s": t0}
	if p := Compute(in); pickID(p) != "s" {
		t.Fatalf("cooldown passed: Pick = %q, want s", pickID(p))
	}
}

func TestComputePendingLaunchCountsOnce(t *testing.T) {
	in := inputs(ranked("s", 1, pigtail.StatusBuilding))
	in.Launches = []pigtail.SessionLaunch{{LaunchID: "ln1", SubjectID: "s", State: pigtail.LaunchPending}}
	p := Compute(in)
	if len(p.Places) != 1 || !p.Places[0].Pending || p.Places[0].LaunchID != "ln1" {
		t.Fatalf("Places = %+v, want one pending ln1", p.Places)
	}
	if s, _, _ := skipOf(p, "s"); s != pigtail.StaffingSkipPending {
		t.Fatalf("skip = %q, want pending", s)
	}
	// The session registers in the same tour: still one place.
	in.Peers = []pigtail.PeerSession{livePeer("p1", "s")}
	in.AllPeers = in.Peers
	p = Compute(in)
	if len(p.Places) != 1 || p.Places[0].Pending {
		t.Fatalf("Places = %+v, want the one live session", p.Places)
	}
	if pickID(p) != "" {
		t.Fatalf("Pick = %q, want none", pickID(p))
	}
}

func TestComputePausedSessionHoldsSubject(t *testing.T) {
	ended := time.Now()
	for _, tc := range []struct {
		name      string
		state     string
		peerAlive bool
	}{
		{"paused, peer ended", pigtail.PausePaused, false},
		{"stopping, peer still alive", pigtail.PauseStopping, true},
		{"stopping, peer ended", pigtail.PauseStopping, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := inputs(ranked("s", 1, pigtail.StatusBuilding))
			peer := livePeer("p1", "s")
			if tc.peerAlive {
				in.Peers = []pigtail.PeerSession{peer}
			} else {
				peer.EndedAt = &ended
			}
			in.AllPeers = []pigtail.PeerSession{peer}
			in.Pauses = []pigtail.SessionPause{{SessionID: "p1", State: tc.state}}
			p := Compute(in)
			if s, _, _ := skipOf(p, "s"); s != pigtail.StaffingSkipPaused {
				t.Fatalf("skip = %q, want paused", s)
			}
			if pickID(p) != "" || len(p.Places) != 0 {
				t.Fatalf("Pick = %q, Places = %+v; want none", pickID(p), p.Places)
			}
		})
	}
	t.Run("pause on an unbound or unknown peer", func(t *testing.T) {
		in := inputs(ranked("s", 1, pigtail.StatusBuilding))
		in.AllPeers = []pigtail.PeerSession{{SessionID: "p_nosub", PID: 1, ConfigDir: "/c"}}
		in.Pauses = []pigtail.SessionPause{{SessionID: "p_nosub", State: pigtail.PausePaused}, {SessionID: "p_gone", State: pigtail.PauseStopping}}
		p := Compute(in)
		if pickID(p) != "s" {
			t.Fatalf("Pick = %q, want s", pickID(p))
		}
	})
}

func TestComputePhantomPeerHoldsSubjectAndTakesPlace(t *testing.T) {
	for _, phantom := range []pigtail.PeerSession{
		{SessionID: "ph", SubjectID: "s", PID: 0, ConfigDir: "/cfg"},
		{SessionID: "ph", SubjectID: "s", PID: 7, ConfigDir: ""},
	} {
		in := inputs(ranked("s", 1, pigtail.StatusBuilding))
		in.Peers = []pigtail.PeerSession{phantom}
		in.AllPeers = in.Peers
		p := Compute(in)
		if s, _, _ := skipOf(p, "s"); s != pigtail.StaffingSkipPhantom {
			t.Fatalf("skip = %q, want phantom", s)
		}
		if pickID(p) != "" {
			t.Fatalf("Pick = %q, want none", pickID(p))
		}
		if len(p.Places) != 1 || !p.Places[0].Phantom {
			t.Fatalf("Places = %+v, want one phantom", p.Places)
		}

		// Plus a verifiable peer on the same subject: held, two places.
		in.Peers = []pigtail.PeerSession{phantom, livePeer("p1", "s")}
		in.AllPeers = in.Peers
		p = Compute(in)
		if s, _, _ := skipOf(p, "s"); s != pigtail.StaffingSkipHeld {
			t.Fatalf("skip = %q, want held", s)
		}
		if len(p.Places) != 2 {
			t.Fatalf("Places = %+v, want two", p.Places)
		}

		// A pause row on the phantom takes it out of the places.
		in.Peers = []pigtail.PeerSession{phantom}
		in.AllPeers = in.Peers
		in.Pauses = []pigtail.SessionPause{{SessionID: "ph", State: pigtail.PausePaused}}
		p = Compute(in)
		if len(p.Places) != 0 {
			t.Fatalf("paused phantom: Places = %+v, want none", p.Places)
		}
		if s, _, _ := skipOf(p, "s"); s != pigtail.StaffingSkipPaused {
			t.Fatalf("paused phantom: skip = %q, want paused", s)
		}
	}
}

func TestComputeAcrossScopesBestRankThenAlternate(t *testing.T) {
	in := Inputs{Now: t0, Queues: map[string][]store.RankedSubject{
		tPerso: {ranked("p", 2, pigtail.StatusBuilding)},
		tWork:  {inScope(ranked("t", 1, pigtail.StatusBuilding), tWork)},
	}}
	if p := Compute(in); pickID(p) != "t" || p.Pick.Scope != tWork {
		t.Fatalf("Pick = %+v, want t in work", p.Pick)
	}
	in.Queues[tPerso] = []store.RankedSubject{ranked("p", 1, pigtail.StatusBuilding)}
	in.LastScope = tWork
	if p := Compute(in); pickID(p) != "p" {
		t.Fatalf("equal ranks, last work: Pick = %q, want p", pickID(p))
	}
	in.LastScope = tPerso
	if p := Compute(in); pickID(p) != "t" {
		t.Fatalf("equal ranks, last perso: Pick = %q, want t", pickID(p))
	}
}

func TestComputeOverMarginScopeYieldsNothing(t *testing.T) {
	in := Inputs{Now: t0, Queues: map[string][]store.RankedSubject{
		tPerso: {ranked("p", 1, pigtail.StatusBuilding)},
		tWork:  {inScope(ranked("t", 5, pigtail.StatusBuilding), tWork)},
	}, Margin: map[string]string{tPerso: "all cells over margin"}}
	p := Compute(in)
	if pickID(p) != "t" {
		t.Fatalf("Pick = %q, want t", pickID(p))
	}
	if s, d, _ := skipOf(p, "p"); s != pigtail.StaffingSkipOverMargin || d != "all cells over margin" {
		t.Fatalf("p skip = %q/%q, want over_margin", s, d)
	}
	in.Margin[tWork] = "work over"
	p = Compute(in)
	if p.Pick != nil || p.Gate != pigtail.StaffingGateOverMargin {
		t.Fatalf("both over: Pick = %+v, Gate = %q", p.Pick, p.Gate)
	}
}

func TestComputeEmptyQueue(t *testing.T) {
	p := Compute(inputs(ranked("i", 1, pigtail.StatusInbox)))
	if p.Pick != nil || p.Gate != pigtail.StaffingGateNoReadySubject {
		t.Fatalf("Pick = %+v, Gate = %q; want no_ready_subject", p.Pick, p.Gate)
	}
	if p := Compute(Inputs{Now: t0}); p.Gate != pigtail.StaffingGateNoReadySubject {
		t.Fatalf("no queue: Gate = %q", p.Gate)
	}
}

func TestComputeOverBudgetSkipsHead(t *testing.T) {
	const why = "every account of scope perso is out of its weekly token budget; first release at Mon 15:04"
	in := Inputs{Now: t0, Queues: map[string][]store.RankedSubject{
		tPerso: {ranked("p", 1, pigtail.StatusBuilding)},
	}, Tokens: map[string]string{tPerso: why}}
	p := Compute(in)
	if p.Pick != nil || p.Gate != pigtail.StaffingGateOverBudget {
		t.Fatalf("Pick = %+v, Gate = %q; want over_budget", p.Pick, p.Gate)
	}
	if s, d, _ := skipOf(p, "p"); s != pigtail.StaffingSkipOverBudget || d != why {
		t.Fatalf("p skip = %q/%q, want over_budget", s, d)
	}

	// The margin wins on the same scope: it is read first.
	in.Margin = map[string]string{tPerso: "all cells over margin"}
	p = Compute(in)
	if p.Gate != pigtail.StaffingGateOverMargin {
		t.Fatalf("margin and budget: Gate = %q, want over_margin", p.Gate)
	}
	if s, _, _ := skipOf(p, "p"); s != pigtail.StaffingSkipOverMargin {
		t.Fatalf("margin and budget: skip = %q", s)
	}

	// A scope with no refusal is picked.
	in.Margin = nil
	in.Queues[tWork] = []store.RankedSubject{inScope(ranked("t", 5, pigtail.StatusBuilding), tWork)}
	if p = Compute(in); pickID(p) != "t" {
		t.Fatalf("Pick = %q, want t", pickID(p))
	}

	// One scope over its margin, the other over its budget: not the budget alone.
	in.Margin = map[string]string{tWork: "work over"}
	if p = Compute(in); p.Pick != nil || p.Gate != pigtail.StaffingGateOverMargin {
		t.Fatalf("Pick = %+v, Gate = %q; want over_margin", p.Pick, p.Gate)
	}
}
