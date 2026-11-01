package staffing

import (
	"sort"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// readyStatuses are the statuses a subject can be launched on (D2): never an
// inbox, never a closed subject.
var readyStatuses = map[string]bool{
	pigtail.StatusUnderstanding: true,
	pigtail.StatusDesigning:     true,
	pigtail.StatusBuilding:      true,
}

// Inputs is the world one tour reads; Compute reads nothing else.
type Inputs struct {
	Now       time.Time
	Peers     []pigtail.PeerSession            // live only (the caller applied Alive)
	AllPeers  []pigtail.PeerSession            // ended included (ListPeers(true)): the only source of the pause → subject join
	Waits     map[string][]pigtail.SessionWait // by session (Store.ListSessionWaits)
	Pauses    []pigtail.SessionPause           // every row, stopping and paused (Store.ListPauses)
	Launches  []pigtail.SessionLaunch          // Launcher.List
	OpenWaits map[string]string                // subject → type of its oldest open wait addressed to the user
	Queues    map[string][]store.RankedSubject // by scope, rank order
	Cooldown  map[string]time.Time             // subject → may be taken again at
	Margin    map[string]string                // scope → "" under the margin, else the refusal's text
	Tokens    map[string]string                // scope → "" within the weekly budget, else the refusal's text
	LastScope string                           // scope of the last launch (alternation)
}

// Plan is what a tour would do.
type Plan struct {
	Places  []pigtail.StaffingPlace
	Queues  map[string][]pigtail.StaffingCandidate // eligible, rank order, Skip set
	Pick    *pigtail.StaffingCandidate
	Skipped []pigtail.StaffingCandidate // the skipped ranked before the choice, in each scope
	Gate    string                      // when Pick == nil: no_ready_subject | over_margin | over_budget
}

// held is the join of sessions to subjects: the one place it is made.
type held struct {
	live, phantom, paused map[string]bool // by subject
	pausedSession         map[string]bool // by session
}

// phantomPeer: a live peer peers.Alive could not check (no pid or no config
// dir) — maybe a real session that registered early, maybe a ghost.
func phantomPeer(p pigtail.PeerSession) bool { return p.PID <= 0 || p.ConfigDir == "" }

// heldSubjects joins pauses and live peers to their subjects. A pause row, in
// any state, names the peer of the same session in AllPeers: its subject is
// paused, and that peer — even alive (stopping) — is neither live nor a place.
func heldSubjects(in Inputs) held {
	h := held{live: map[string]bool{}, phantom: map[string]bool{}, paused: map[string]bool{}, pausedSession: map[string]bool{}}
	bySession := make(map[string]pigtail.PeerSession, len(in.AllPeers))
	for _, p := range in.AllPeers {
		bySession[p.SessionID] = p
	}
	for _, pa := range in.Pauses {
		h.pausedSession[pa.SessionID] = true
		if p, ok := bySession[pa.SessionID]; ok && p.SubjectID != "" {
			h.paused[p.SubjectID] = true
		}
	}
	for _, p := range in.Peers {
		if p.SubjectID == "" || h.pausedSession[p.SessionID] {
			continue
		}
		if phantomPeer(p) {
			h.phantom[p.SubjectID] = true
		} else {
			h.live[p.SubjectID] = true
		}
	}
	return h
}

// sessionWait is the wait a place shows (the most recent) and whether the
// session declared it is finished (done or wrapped), which frees its place.
func sessionWait(ws []pigtail.SessionWait) (shown string, finished bool) {
	var since time.Time
	for _, w := range ws {
		if w.Type == "done" || w.Type == "wrapped" {
			finished = true
		}
		if shown == "" || w.Since.After(since) {
			shown, since = w.Type, w.Since
		}
	}
	return shown, finished
}

// working reports whether a live peer holds a place. It is a deny-list: only
// the native statuses idle and waiting (a dialog waiting on the user) hold none;
// busy, shell (a background shell wakes the model when it ends), "" (native
// file unreadable: fail-safe) and any unknown status count, and so does a phantom (nothing to read). A launch still
// pending, or recorded less than LaunchGrace ago, counts whatever the status:
// between the record and the first busy a tour must not see 0 places. The
// peer keeps its subject either way (heldSubjects).
func working(p pigtail.PeerSession, launches []pigtail.SessionLaunch, now time.Time) bool {
	if phantomPeer(p) || (p.Status != "idle" && p.Status != "waiting") {
		return true
	}
	for _, l := range launches {
		if l.SubjectID != p.SubjectID {
			continue
		}
		if l.State == pigtail.LaunchPending {
			return true
		}
		if l.State == pigtail.LaunchLaunched && now.Sub(l.UpdatedAt) < LaunchGrace {
			return true
		}
	}
	return false
}

// Compute decides one tour: the places taken, the ready queues and the pick.
// It is pure: everything comes from in.
func Compute(in Inputs) Plan {
	h := heldSubjects(in)

	subjects := map[string]pigtail.Subject{}
	subjectScope := map[string]string{}
	for sc, q := range in.Queues {
		for _, r := range q {
			subjects[r.ID] = r.Subject
			subjectScope[r.ID] = sc
		}
	}

	// Places: live peers bound to a subject, not paused, not finished; then
	// the pending launches whose subject has no live peer yet.
	plan := Plan{Places: []pigtail.StaffingPlace{}, Queues: map[string][]pigtail.StaffingCandidate{}, Skipped: []pigtail.StaffingCandidate{}}
	livePeerOn := map[string]bool{}
	for _, p := range in.Peers {
		if p.SubjectID == "" || h.pausedSession[p.SessionID] {
			continue
		}
		livePeerOn[p.SubjectID] = true
		wait, finished := sessionWait(in.Waits[p.SessionID])
		if finished || !working(p, in.Launches, in.Now) {
			continue
		}
		place := pigtail.StaffingPlace{SessionID: p.SessionID, SubjectID: p.SubjectID, ShortTitle: p.SubjectShortTitle,
			Scope: p.Scope, Label: firstNonEmpty(p.CmuxTitle, p.Name), Wait: wait, Phantom: phantomPeer(p)}
		if sub, ok := subjects[p.SubjectID]; ok {
			place.ShortTitle, place.Scope = pigtail.ShortTitle(sub), subjectScope[p.SubjectID]
		}
		plan.Places = append(plan.Places, place)
	}
	pending := map[string]string{} // subject → launch id
	for _, l := range in.Launches {
		if l.State != pigtail.LaunchPending || l.SubjectID == "" {
			continue
		}
		if _, dup := pending[l.SubjectID]; dup {
			continue
		}
		pending[l.SubjectID] = l.LaunchID
		if livePeerOn[l.SubjectID] {
			continue
		}
		place := pigtail.StaffingPlace{SubjectID: l.SubjectID, LaunchID: l.LaunchID, Pending: true, Label: l.TabTitle}
		if sub, ok := subjects[l.SubjectID]; ok {
			place.ShortTitle, place.Scope = pigtail.ShortTitle(sub), subjectScope[l.SubjectID]
		}
		plan.Places = append(plan.Places, place)
	}

	// Queues: the eligible subjects of each scope, each with its skip.
	scopes := make([]string, 0, len(in.Queues))
	for sc := range in.Queues {
		scopes = append(scopes, sc)
	}
	sort.Strings(scopes)
	type head struct {
		scope string
		idx   int
	}
	var heads []head
	marginRefused, budgetRefused := false, false
	for _, sc := range scopes {
		q := []pigtail.StaffingCandidate{}
		for _, r := range in.Queues[sc] {
			if r.Rank <= 0 || !readyStatuses[r.Status] {
				continue
			}
			c := pigtail.StaffingCandidate{SubjectID: r.ID, ShortTitle: pigtail.ShortTitle(r.Subject), Scope: sc, Status: r.Status, Rank: r.Rank}
			c.Skip, c.Detail = skipFor(in, h, pending, r)
			q = append(q, c)
		}
		headIdx := -1
		for i := range q {
			if q[i].Skip == "" {
				headIdx = i
				break
			}
		}
		if headIdx >= 0 {
			if refusal := in.Margin[sc]; refusal != "" {
				q[headIdx].Skip, q[headIdx].Detail = pigtail.StaffingSkipOverMargin, refusal
				marginRefused = true
			} else if refusal := in.Tokens[sc]; refusal != "" {
				q[headIdx].Skip, q[headIdx].Detail = pigtail.StaffingSkipOverBudget, refusal
				budgetRefused = true
			} else {
				heads = append(heads, head{sc, headIdx})
			}
		}
		// The skipped ranked before the scope's head (all of them when the
		// scope has none, the refused head included).
		for i := range q {
			if headIdx >= 0 && i > headIdx {
				break
			}
			if q[i].Skip != "" {
				plan.Skipped = append(plan.Skipped, q[i])
			}
		}
		plan.Queues[sc] = q
	}

	if len(heads) == 0 {
		plan.Gate = pigtail.StaffingGateNoReadySubject
		switch {
		case marginRefused: // over_budget only when the budget alone holds every scope back
			plan.Gate = pigtail.StaffingGateOverMargin
		case budgetRefused:
			plan.Gate = pigtail.StaffingGateOverBudget
		}
		return plan
	}
	// Best rank; a tie goes to a scope other than the last launch's, then to
	// the order of the names (heads are already in that order).
	best := heads[0]
	for _, hd := range heads[1:] {
		b, c := plan.Queues[best.scope][best.idx], plan.Queues[hd.scope][hd.idx]
		switch {
		case c.Rank < b.Rank:
			best = hd
		case c.Rank == b.Rank && best.scope == in.LastScope && hd.scope != in.LastScope:
			best = hd
		}
	}
	pick := plan.Queues[best.scope][best.idx]
	plan.Pick = &pick
	return plan
}

// skipFor returns why an eligible subject is not ready, in the order of the
// rules ("" = ready), and its detail.
func skipFor(in Inputs, h held, pending map[string]string, r store.RankedSubject) (string, string) {
	switch {
	case h.live[r.ID]:
		return pigtail.StaffingSkipHeld, ""
	case h.phantom[r.ID]:
		return pigtail.StaffingSkipPhantom, ""
	case h.paused[r.ID]:
		return pigtail.StaffingSkipPaused, ""
	}
	if id, ok := pending[r.ID]; ok {
		return pigtail.StaffingSkipPending, id
	}
	if w := in.OpenWaits[r.ID]; w != "" {
		return pigtail.StaffingSkipWaitsHuman, w
	}
	if len(r.BlockedBy) > 0 {
		return pigtail.StaffingSkipDeps, strings.Join(r.BlockedBy, ", ")
	}
	if at, ok := in.Cooldown[r.ID]; ok && at.After(in.Now) {
		return pigtail.StaffingSkipCooldown, at.Local().Format("15:04")
	}
	return "", ""
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
