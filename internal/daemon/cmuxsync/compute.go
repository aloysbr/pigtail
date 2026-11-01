package cmuxsync

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Compute derives the wanted sidebar of every live workspace (spec §4.1,
// §4.5, §4.7), servers included (lot 3). Pure: same input, same output, no clock but in.Now.
//
// Attention comes out raw here; the runner passes it through a Damper for
// the hysteresis of §4.3.
func Compute(in Input) Result {
	res := Result{Desired: make(map[string]Desired, len(in.Workspaces))}
	live := make(map[string]Workspace, len(in.Workspaces))
	for _, w := range in.Workspaces {
		live[w.ID] = w
		res.Desired[w.ID] = Desired{WorkspaceID: w.ID, Maintenance: in.Maintenance}
	}
	for _, id := range maintenanceOwnerWorkspaces(in.Maintenance, in.Sessions, live) {
		d := res.Desired[id]
		d.MaintenanceHere = true
		res.Desired[id] = d
	}

	loc := in.Location
	if loc == nil {
		loc = time.Local
	}
	byWS := sessionsByWorkspace(in.Sessions, live)
	for _, w := range in.Workspaces {
		d := res.Desired[w.ID]
		var rm *respawnMark
		if m, ok := in.Respawn[w.ID]; ok {
			rm = &m
		}
		d.Live, d.DeadSurfaces = attentionOf(byWS[w.ID], w.Surfaces, in.TrustPending[w.ID], rm, in.Frozen, loc)
		d.Cells = cellsOf(byWS[w.ID])
		d.resolve()
		res.Desired[w.ID] = d
	}

	for _, s := range in.Servers {
		if _, ok := live[s.WorkspaceID]; !ok {
			continue // owned by no live workspace (closed, or not a cmux one)
		}
		d := res.Desired[s.WorkspaceID]
		d.Servers = append(d.Servers, s)
		res.Desired[s.WorkspaceID] = d
	}

	for id, p := range checkPills(in.CheckDefects, in.Workspaces) {
		p := p
		d := res.Desired[id]
		d.Check = &p
		res.Desired[id] = d
	}
	for id, n := range inputDeadPills(in.DeadInputs, in.Workspaces) {
		d := res.Desired[id]
		p := CheckPill{}
		if d.Check != nil {
			p = *d.Check
		}
		p.InputDead = n
		d.Check = &p
		res.Desired[id] = d
	}

	bound := make(map[string]bool, len(in.Bindings))
	for _, b := range sortedBindings(in.Bindings) {
		bound[b.CmuxWorkspaceID] = true
		w, ok := live[b.CmuxWorkspaceID]
		if !ok {
			continue // closed: the runner's missing-ticks rule handles the link
		}
		sv, ok := in.Subjects[b.SubjectID]
		if !ok {
			continue // subject not visible to the loop: touch nothing
		}
		d := res.Desired[w.ID]
		guard := recentWrite(in, w.ID)

		short := sidebarText(pigtail.ShortTitle(sv.Subject))
		d.Title = short // D5: a bound workspace always carries its short title
		if short != "" && !guard && titleTakenBack(w, b) {
			res.Reverts = append(res.Reverts, TitleRevert{
				WorkspaceID: w.ID, SubjectID: b.SubjectID, Short: short,
				Manual:  pigtail.ShownTitle(w.Title, w.CustomTitle, w.HasCustomTitle),
				Cleared: !w.HasCustomTitle || strings.TrimSpace(w.CustomTitle) == "",
			})
		}
		if b.DescriptionOwner != pigtail.CmuxOwnerUser {
			if !guard && descriptionTakenBack(w, b) {
				res.Takeovers = append(res.Takeovers, Takeover{WorkspaceID: w.ID, SubjectID: b.SubjectID, Field: FieldDescription})
			} else {
				desc := Description(sv)
				if ct := cellText(d.Cells); ct != "" {
					desc += " · " + ct // D1
				}
				d.Description = sidebarText(desc)
			}
		}
		res.Desired[w.ID] = d
	}

	for _, w := range in.Workspaces {
		if in.Paused[w.ID] {
			d := res.Desired[w.ID]
			d.Paused = true
			res.Desired[w.ID] = d
		}
	}

	if len(in.RoleAlerts) > 0 {
		bySubject := roleBySubject(in.RoleAlerts)
		for _, b := range sortedBindings(in.Bindings) {
			a, ok := bySubject[b.SubjectID]
			if !ok {
				continue
			}
			if _, live := res.Desired[b.CmuxWorkspaceID]; !live {
				continue
			}
			d := res.Desired[b.CmuxWorkspaceID]
			if d.Role == nil || a.Since.Before(d.Role.Since) {
				d.Role = &RolePill{Role: a.Role, State: a.State, Since: a.Since, Now: in.Now}
			}
			res.Desired[b.CmuxWorkspaceID] = d
		}
	}

	for _, w := range in.Workspaces {
		if in.OffTopic[w.ID] && !bound[w.ID] {
			d := res.Desired[w.ID]
			d.OffTopic = true
			res.Desired[w.ID] = d
		}
	}

	// An unbound workspace shows its cells alone, unless its description was
	// written by hand (spec § Comportement attendu, « Description reprise à
	// la main »): pigtail writes only over nothing or its own cell text.
	for _, w := range in.Workspaces {
		if bound[w.ID] {
			continue
		}
		cur := strings.TrimSpace(w.Description)
		if cur != "" && cur != in.CellText[w.ID] && !isCellText(cur, in.KnownCells) {
			continue
		}
		d := res.Desired[w.ID]
		if ct := cellText(d.Cells); ct != "" {
			d.Description = sidebarText(ct)
		} else if cur != "" {
			d.ClearDescription = true
		}
		res.Desired[w.ID] = d
	}
	return res
}

// cellsOf lists the cells of a workspace's live sessions (D1): distinct, in
// arrival order (earliest StartedAt; unknown last), then by id. A session
// without a cell adds nothing (D7).
func cellsOf(ss []Session) []CellRef {
	type first struct {
		ref CellRef
		at  time.Time
	}
	seen := map[string]*first{}
	for _, s := range ss {
		if !s.Alive || s.Ended || s.CellID == "" {
			continue
		}
		f := seen[s.CellID]
		if f == nil {
			seen[s.CellID] = &first{CellRef{s.CellID, s.Scope}, s.StartedAt}
			continue
		}
		if !s.StartedAt.IsZero() && (f.at.IsZero() || s.StartedAt.Before(f.at)) {
			f.at = s.StartedAt
		}
	}
	out := make([]first, 0, len(seen))
	for _, f := range seen {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.at.IsZero() != b.at.IsZero() {
			return !a.at.IsZero()
		}
		if !a.at.Equal(b.at) {
			return a.at.Before(b.at)
		}
		return a.ref.ID < b.ref.ID
	})
	refs := make([]CellRef, len(out))
	for i, f := range out {
		refs[i] = f.ref
	}
	return refs
}

// cellText is the cells as the description shows them: "mp+jt".
func cellText(cs []CellRef) string {
	ids := make([]string, len(cs))
	for i, c := range cs {
		ids[i] = c.ID
	}
	return strings.Join(ids, "+")
}

// isCellText: s is made only of known cell ids joined by "+" — a description
// pigtail posed, recognised after a daemon restart.
func isCellText(s string, known map[string]bool) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(known) == 0 {
		return false
	}
	for _, id := range strings.Split(s, "+") {
		if !known[id] {
			return false
		}
	}
	return true
}

// sessionsByWorkspace keeps the interactive sessions placed in a live
// workspace. A non-interactive session (`claude -p`, kind set to anything
// but "interactive") never earns a pill.
func sessionsByWorkspace(ss []Session, live map[string]Workspace) map[string][]Session {
	out := map[string][]Session{}
	for _, s := range ss {
		if _, ok := live[s.WorkspaceID]; !ok {
			continue
		}
		if s.Kind != "" && s.Kind != "interactive" {
			continue
		}
		out[s.WorkspaceID] = append(out[s.WorkspaceID], s)
	}
	return out
}

// AutoWaitSkew bounds how long before the native wait began a hook may have
// typed it: the hook fires just before the dialog opens (PreToolUse,
// PermissionRequest) or just after (Notification). An automatic wait older
// than that belongs to an earlier dialog whose end the loop missed.
const AutoWaitSkew = 10 * time.Second

// waitOf tells whether one session waits for its user, and for what (spec
// §4.8). Only a live session waits.
//   - status "waiting" (a dialog is open): the hook's type when one typed
//     this dialog, else the native waitingFor when telling, else the more
//     severe of the declared and held types, else Unknown;
//   - status "idle" (the turn ended): with a failure told by StopFailure
//     (Limit, Login, API error), the more severe of that failure and the
//     declared or held wait (D8), the failure over anything it outranks;
//     else the more severe of the declared and held types, else Your turn
//     when the loop saw the session busy since its start or resume
//     (Session.BusySeen, D24) and the session is not in Standby
//     (Session.Standby, 2026-10-16), else nothing;
//   - status "busy": nothing — a declaration made mid-turn shows once the
//     agent hands back.
func waitOf(s Session, loc *time.Location) (bool, waittype.Type, string) {
	if !s.Alive {
		return false, waittype.Unknown, ""
	}
	switch s.Status {
	case "waiting":
		if s.Auto.Set() && (s.NativeStatusAt.IsZero() || !s.Auto.Since.Before(s.NativeStatusAt.Add(-AutoWaitSkew))) {
			return true, s.Auto.Type, s.Auto.Reason
		}
		if t := waittype.FromWaitingFor(s.WaitingFor); t != waittype.Unknown {
			return true, t, ""
		}
		if w := gravest(s.Declared, s.Held); w.Set() {
			return true, w.Type, w.Reason
		}
		return true, waittype.Unknown, ""
	case "idle":
		if s.Auto.Set() && s.Auto.Type.Failure() {
			// StopFailure: the turn ended on a failure, not on the user's
			// turn. Within one session the most severe state wins (D8): a
			// declared Blocked or Permission is kept over a Limit.
			if w := gravest(s.Declared, s.Held); w.Set() && waittype.Before(w.Type, s.Auto.Type) {
				return true, w.Type, w.Reason
			}
			return true, s.Auto.Type, failureReason(s, loc)
		}
		if w := gravest(s.Declared, s.Held); w.Set() {
			return true, w.Type, w.Reason
		}
		if s.BusySeen && !s.Standby {
			return true, waittype.YourTurn, ""
		}
	}
	return false, waittype.Unknown, ""
}

// gravest is the more severe of a declared and a held wait (waittype.Order),
// the declared one on a tie; zero when neither is set.
func gravest(declared, held Wait) Wait {
	if held.Set() && (!declared.Set() || waittype.Before(held.Type, declared.Type)) {
		return held
	}
	return declared
}

// failureReason is the reason a failure pill shows: the local reset of a
// limit (D2), the cell to log in again (D3); for an API error, the retry's own
// reason (relay_retry.go: "relance à 14:05 (2/5)", "relance au retour
// réseau", "relances épuisées", "relance en attente"), none before any retry.
func failureReason(s Session, loc *time.Location) string {
	switch s.Auto.Type {
	case waittype.Limit:
		if strings.HasPrefix(s.Auto.Reason, waittype.RotationReasonPrefix) {
			// The account rotation refused (the limit stands, its reset kept)
			// or moved the session without its resume prompt (no reset: the
			// new account is not limited). The detail is in the worker's log.
			if s.Auto.Until.IsZero() {
				return "switched, not resumed"
			}
			return s.Auto.Until.In(loc).Format("15:04") + " · not switched"
		}
		if !s.Auto.Until.IsZero() {
			return s.Auto.Until.In(loc).Format("15:04")
		}
	case waittype.Login:
		return s.CellID
	case waittype.APIError:
		return s.Auto.Reason // already cleaned at its write
	}
	return ""
}

// respawnAbandonedReason is the reason of the orange Respawn an abandoned
// respawn shows (D2, 2026-10-23).
const respawnAbandonedReason = "abandoned"

// trustReason is the reason of the pill a pending trust dialog shows (D4).
const trustReason = "trust folder"

// freezeReason is the reason of the Blocked a freeze refusal shows (D4).
const freezeReason = "freeze"

// attentionOf adds up a workspace (spec §4, D6, D14, D15): every live
// session that waits (waitOf), the pending trust dialog as one red Blocked,
// a respawn run's mark (D16), and the open surfaces whose latest session died
// (pid gone, no SessionEnd) with no live session since. A surface missing
// from open was closed: its dead session is history. open == nil (unknown)
// proves none dead.
//
// A live held Mac wait (the Mac-hold helper) is never hidden by another
// session's turn-end Your turn, Review or Done: Waiting for Mac ranks just
// above Your turn (waittype.Order). Within one session the held wait already
// replaces Your turn (waitOf).
//
// A waiting or abandoned respawn is one orange Respawn · <reason>: the
// computed Your turn of its surface's session is that same wait, not a
// second one. A failed respawn is one red Blocked · respawn failed, and its
// surface reads dead to no one: it closed that session itself (an abandoned
// one closed nothing, so a death there is a real Dead).
func attentionOf(ss []Session, open map[string]bool, trustPending bool, rm *respawnMark, frozen map[string]bool, loc *time.Location) (liveSummary, int) {
	type shown struct {
		s      Session
		ok     bool
		t      waittype.Type
		reason string
	}
	waits := make([]shown, 0, len(ss))
	var l liveSummary
	for _, s := range ss {
		ok, t, reason := waitOf(s, loc)
		if frozen[s.SessionID] && s.Alive && !s.Ended && (!ok || !waittype.Before(t, waittype.Blocked)) {
			// D4, within one session D8: the freeze replaces its own wait
			// unless that wait outranks Blocked (Permission).
			ok, t, reason = true, waittype.Blocked, freezeReason
		}
		waits = append(waits, shown{s, ok, t, reason})
	}
	if trustPending {
		l.addSystem(waittype.Blocked, trustReason)
	}
	held, absorbed := "", ""
	if rm != nil {
		switch rm.state {
		case pigtail.CmuxRespawnWaiting:
			l.addSystem(waittype.Respawn, rm.reason)
			held = rm.surface
		case pigtail.CmuxRespawnAbandoned:
			// Nothing was closed: the surface's own death still shows.
			l.addSystem(waittype.Respawn, respawnAbandonedReason)
			held = rm.surface
		default:
			l.addSystem(waittype.Blocked, "respawn "+rm.state)
			absorbed = rm.surface
		}
	}
	for _, w := range waits {
		if w.ok {
			if w.t == waittype.YourTurn && held != "" && w.s.SurfaceID == held {
				continue
			}
			l.add(w.t, w.reason, w.s.Scope)
		}
	}
	return l, deadSurfaces(ss, open, absorbed)
}

// deadSurfaces counts the open surfaces whose latest session died, but the
// one a final respawn accounts for (skip; "" = none).
func deadSurfaces(ss []Session, open map[string]bool, skip string) int {
	type surface struct {
		alive  bool
		latest Session
		seen   bool
	}
	surfaces := map[string]*surface{}
	for _, s := range ss {
		key := s.SurfaceID
		if key == "" || !open[key] || key == skip {
			continue
		}
		sf := surfaces[key]
		if sf == nil {
			sf = &surface{}
			surfaces[key] = sf
		}
		if s.Alive {
			sf.alive = true
		}
		if !sf.seen || s.UpdatedAt.After(sf.latest.UpdatedAt) {
			sf.latest, sf.seen = s, true
		}
	}
	n := 0
	for _, sf := range surfaces {
		if !sf.alive && !sf.latest.Alive && !sf.latest.Ended {
			n++
		}
	}
	return n
}

// Description renders `theme · status · #rank` (D2, subject « Noms de sujets »;
// spec §4.1 had scope). The theme's name falls back to the subject's scope
// when it is unknown. The middle word is the subject's status, never its open
// phase (D6: the status is the maturity, the phase a workflow step). "#rank" is
// omitted at 0 (unranked).
func Description(sv SubjectView) string {
	var parts []string
	if name := strings.TrimSpace(sv.ThemeName); name != "" {
		parts = append(parts, name)
	} else if sv.Subject.Scope != "" {
		parts = append(parts, sv.Subject.Scope)
	}
	if st := sv.Subject.Status; st != "" {
		parts = append(parts, st)
	}
	if sv.Subject.Rank > 0 {
		parts = append(parts, "#"+strconv.Itoa(sv.Subject.Rank))
	}
	return strings.Join(parts, " · ")
}

// titleTakenBack: pigtail posed a title and the workspace no longer shows it
// as its custom title — renamed by hand, or its name cleared in cmux. Either
// way the short title is put back (D5, sub_x000000000000018).
func titleTakenBack(w Workspace, b pigtail.SubjectCmuxBinding) bool {
	if strings.TrimSpace(b.AppliedTitle) == "" {
		return false // pigtail never posed one: nothing to take back
	}
	if !w.HasCustomTitle {
		return true
	}
	return strings.TrimSpace(w.CustomTitle) != strings.TrimSpace(b.AppliedTitle)
}

func descriptionTakenBack(w Workspace, b pigtail.SubjectCmuxBinding) bool {
	if strings.TrimSpace(b.AppliedDescription) == "" {
		return false
	}
	return strings.TrimSpace(w.Description) != strings.TrimSpace(b.AppliedDescription)
}

func recentWrite(in Input, wsID string) bool {
	t, ok := in.LastTextWrite[wsID]
	if !ok {
		return false
	}
	read := in.ListStartedAt
	if read.IsZero() {
		read = in.Now
	}
	return read.Before(t.Add(TakeoverReadGuard))
}

// shownTitle is the title the sidebar shows for w once d is applied: the
// one pigtail is about to pose, else pigtail.ShownTitle.
func shownTitle(w Workspace, d Desired) string {
	if d.Title != "" {
		return d.Title
	}
	return pigtail.ShownTitle(w.Title, w.CustomTitle, w.HasCustomTitle)
}

// sidebarText makes a label safe to hand to cmux: one line, and no leading
// "-", which cmux would parse as a flag.
func sidebarText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimLeft(s, "- ")
}

func sortedBindings(bs []pigtail.SubjectCmuxBinding) []pigtail.SubjectCmuxBinding {
	out := append([]pigtail.SubjectCmuxBinding(nil), bs...)
	sort.Slice(out, func(i, j int) bool { return out[i].CmuxWorkspaceID < out[j].CmuxWorkspaceID })
	return out
}

// maintenanceOwnerWorkspaces lists the live workspaces where the session
// that set the maintenance runs (D1). The whole workspace is marked, whatever
// other sessions it carries; the kind is not filtered (the mark says where the
// maintenance came from, not who waits). An owner that is empty, dead, ended,
// unlisted or in a closed workspace marks none (D2): `cmux sessions list` is
// a history, a dead session stays listed.
func maintenanceOwnerWorkspaces(m *MaintenancePill, ss []Session, live map[string]Workspace) []string {
	if m == nil || m.Owner == "" || m.OwnerValue == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, s := range ss {
		if s.SessionID != m.Owner || !s.Alive || s.Ended || seen[s.WorkspaceID] {
			continue
		}
		if _, ok := live[s.WorkspaceID]; !ok {
			continue
		}
		seen[s.WorkspaceID] = true
		out = append(out, s.WorkspaceID)
	}
	return out
}
