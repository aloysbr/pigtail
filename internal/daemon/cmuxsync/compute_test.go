package cmuxsync

import (
	"reflect"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	wsA = "AAAAAAAA-0000-4000-8000-000000000001"
	wsB = "BBBBBBBB-0000-4000-8000-000000000002"
)

var t0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

// live is an open workspace whose panes hold the surfaces S1..S3, the ones
// the cases below place their sessions on.
func live(id string) Workspace {
	return Workspace{ID: id, WindowID: "W", Surfaces: surfaces("S1", "S2", "S3")}
}

func surfaces(ids ...string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func sess(id, ws, surface, status string, alive bool) Session {
	return Session{SessionID: id, WorkspaceID: ws, SurfaceID: surface, Status: status, Alive: alive, Kind: "interactive", UpdatedAt: t0}
}

func TestComputeAttention(t *testing.T) {
	t.Parallel()
	ended := sess("s2", wsA, "S1", "idle", false)
	ended.Ended = true
	older := sess("old", wsA, "S1", "idle", false)
	older.UpdatedAt = t0.Add(-time.Hour)
	newerEnded := sess("new", wsA, "S1", "idle", false)
	newerEnded.Ended = true
	print := sess("p", wsA, "S1", "waiting", true)
	print.Kind = "print"
	printIdle := sess("p2", wsA, "S1", "idle", true) // A19: a finished non-interactive turn is no Your turn
	printIdle.Kind = "print"
	printIdle.NativeStatusAt = t0
	printIdle.BusySeen = true

	cases := []struct {
		name      string
		sessions  []Session
		want      Attention
		wantCount int
	}{
		{"no session (bare terminal)", nil, AttentionNone, 0},
		{"busy session", []Session{sess("s1", wsA, "S1", "busy", true)}, AttentionNone, 0},
		{"idle session: no working/idle pill", []Session{sess("s1", wsA, "S1", "idle", true)}, AttentionNone, 0},
		{"one session waiting", []Session{sess("s1", wsA, "S1", "waiting", true)}, AttentionWaiting, 1},
		{"two sessions waiting: ×2", []Session{
			sess("s1", wsA, "S1", "waiting", true),
			sess("s2", wsA, "S2", "waiting", true),
			sess("s3", wsA, "S3", "busy", true),
		}, AttentionWaiting, 2},
		{"a dead surface beats a waiting dialog (D6)", []Session{
			sess("s1", wsA, "S1", "waiting", true),
			sess("s2", wsA, "S2", "busy", false),
		}, AttentionDead, 1},
		{"a dead waiting session is dead, not waiting", []Session{sess("s1", wsA, "S1", "waiting", false)}, AttentionDead, 1},
		{"dead session (pid gone, no SessionEnd)", []Session{sess("s1", wsA, "S1", "busy", false)}, AttentionDead, 1},
		{"dead per surface: a live neighbour does not hide it", []Session{
			sess("s1", wsA, "S1", "busy", true),
			sess("s2", wsA, "S2", "idle", false),
		}, AttentionDead, 1},
		{"resumed on the same surface: no longer dead", []Session{
			sess("s1", wsA, "S1", "idle", false),
			sess("s2", wsA, "S1", "idle", true),
		}, AttentionNone, 0},
		{"clean exit (SessionEnd): no pill", []Session{ended}, AttentionNone, 0},
		{"dead then a newer session ended cleanly on the surface: no pill", []Session{older, newerEnded}, AttentionNone, 0},
		{"surface-less dead session: no surface to prove it on, no pill", []Session{sess("s1", wsA, "", "idle", false)}, AttentionNone, 0},
		{"non-interactive session ignored", []Session{print}, AttentionNone, 0},
		{"non-interactive idle after a turn: no Your turn", []Session{printIdle}, AttentionNone, 0},
		{"session of another workspace ignored", []Session{sess("s1", wsB, "S1", "waiting", true)}, AttentionNone, 0},
		{"session in a closed workspace ignored", []Session{sess("s1", "CLOSED", "S1", "waiting", true)}, AttentionNone, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := Compute(Input{Now: t0, Workspaces: []Workspace{live(wsA), live(wsB)}, Sessions: c.sessions})
			d := res.Desired[wsA]
			if d.Attention != c.want || d.WaitingCount != c.wantCount {
				t.Fatalf("attention = %q ×%d, want %q ×%d", d.Attention, d.WaitingCount, c.want, c.wantCount)
			}
		})
	}
}

// A session reads dead only while its surface is still open: closing the
// pane of a session that never sent SessionEnd must not leave "Dead" on a
// workspace whose other session lives (bug of 2026-10-01, workspace F4F58E67:
// cmux sessions list keeps the closed pane's session forever).
func TestComputeDeadNeedsAnOpenSurface(t *testing.T) {
	t.Parallel()
	alive := sess("live", wsA, "A", "idle", true)
	dead := sess("gone", wsA, "B", "idle", false)
	cases := []struct {
		name     string
		surfaces map[string]bool
		want     Attention
	}{
		{"dead session's surface closed: no pill", surfaces("A"), AttentionNone},
		{"dead session's surface still open: dead", surfaces("A", "B"), AttentionDead},
		{"surfaces unknown (listing failed): never dead", nil, AttentionNone},
		{"workspace listed with no surface at all: no pill", surfaces(), AttentionNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := Workspace{ID: wsA, WindowID: "W", Surfaces: c.surfaces}
			res := Compute(Input{Now: t0, Workspaces: []Workspace{w}, Sessions: []Session{alive, dead}})
			if got := res.Desired[wsA].Attention; got != c.want {
				t.Fatalf("attention = %q, want %q", got, c.want)
			}
		})
	}
}

// Waiting does not depend on the surface listing: a live session asking for
// an answer always shows, even when cmux could not list the surfaces.
func TestComputeWaitingIgnoresSurfaces(t *testing.T) {
	t.Parallel()
	w := Workspace{ID: wsA, WindowID: "W"}
	res := Compute(Input{Now: t0, Workspaces: []Workspace{w}, Sessions: []Session{sess("s1", wsA, "A", "waiting", true)}})
	if d := res.Desired[wsA]; d.Attention != AttentionWaiting || d.WaitingCount != 1 {
		t.Fatalf("desired = %+v", d)
	}
}

func TestStatusesRendering(t *testing.T) {
	t.Parallel()
	cases := []struct {
		d    Desired
		want string
	}{
		{Desired{Attention: AttentionWaiting, WaitingCount: 1}, "Waiting for you"},
		{Desired{Attention: AttentionWaiting, WaitingCount: 3}, "Waiting for you ×3"},
		{Desired{Attention: AttentionWaiting, WaitingCount: 12}, "Waiting for you ×12"},
		{Desired{Attention: AttentionDead}, "Dead"},
	}
	for _, c := range cases {
		st := c.d.Statuses()
		if got := st[KeyAttention]; got.Value != c.want || got.Priority != 90 || got.Icon == "" || got.Color == "" {
			t.Errorf("%+v → %+v, want value %q prio 90", c.d, got, c.want)
		}
	}
	if e := (Desired{Attention: AttentionDead}).Statuses()[KeyAttention]; e.Icon != "xmark.octagon.fill" || e.Color != "#FF3B30" {
		t.Errorf("dead pill = %+v, want xmark.octagon.fill #FF3B30", e)
	}
	if st := (Desired{}).Statuses(); len(st) != 0 {
		t.Fatalf("no attention must render no pill, got %+v", st)
	}
}

func subject(scope, status string, rank int, phases ...pigtail.SubjectPhase) SubjectView {
	return SubjectView{
		Subject:   pigtail.Subject{ID: "sub_1", Title: "Enrichir la sidebar cmux avec l'état pigtail", ShortTitle: "Sidebar pigtail", Scope: scope, Status: status, Rank: rank},
		Phases:    phases,
		ThemeName: "Method",
	}
}

func withTheme(sv SubjectView, name string) SubjectView { sv.ThemeName = name; return sv }

func phase(kind, state string, at time.Time) pigtail.SubjectPhase {
	return pigtail.SubjectPhase{Kind: kind, State: state, CreatedAt: at}
}

// TestDescriptionNeverShowsPhase (D6): the description shows the subject's
// status, never its open phase.
func TestDescriptionNeverShowsPhase(t *testing.T) {
	t.Parallel()
	sv := subject("perso", pigtail.StatusBuilding, 3, phase(pigtail.PhaseImplement, pigtail.PhaseActive, t0))
	if got := Description(sv); got != "Method · building · #3" {
		t.Fatalf("Description = %q, want %q", got, "Method · building · #3")
	}
}

func TestDescription(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		sv   SubjectView
		want string
	}{
		{"first unfinished phase, by creation", subject("perso", pigtail.StatusBuilding, 3,
			phase(pigtail.PhaseImplement, pigtail.PhasePending, t0.Add(2*time.Hour)),
			phase(pigtail.PhaseSpec, pigtail.PhaseActive, t0.Add(time.Hour)),
			phase(pigtail.PhaseResearch, pigtail.PhaseDone, t0),
		), "Method · building · #3"},
		{"rank 0 omitted (unranked)", subject("perso", pigtail.StatusDesigning, 0, phase(pigtail.PhaseSpec, pigtail.PhaseActive, t0)), "Method · designing"},
		{"no unfinished phase: status", subject("gx", pigtail.StatusBuilding, 2, phase(pigtail.PhaseImplement, pigtail.PhaseDone, t0)), "Method · building · #2"},
		{"no phase at all: status", subject("gx", pigtail.StatusInbox, 0), "Method · inbox"},
		{"done subject reads done", subject("perso", pigtail.StatusDone, 1, phase(pigtail.PhaseImplement, pigtail.PhasePending, t0)), "Method · done · #1"},
		{"accented theme name kept as is", withTheme(subject("perso", pigtail.StatusDesigning, 0), "Méthode & outils"), "Méthode & outils · designing"},
		{"theme unknown: scope fallback", withTheme(subject("perso", pigtail.StatusDesigning, 2), ""), "perso · designing · #2"},
		{"blank theme name: scope fallback", withTheme(subject("gx", pigtail.StatusInbox, 0), "   "), "gx · inbox"},
	}
	for _, c := range cases {
		if got := Description(c.sv); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func binding(ws string) pigtail.SubjectCmuxBinding {
	return pigtail.SubjectCmuxBinding{CmuxWorkspaceID: ws, SubjectID: "sub_1", TitleOwner: pigtail.CmuxOwnerPigtail, DescriptionOwner: pigtail.CmuxOwnerPigtail}
}

func TestComputeTitleAndDescription(t *testing.T) {
	t.Parallel()
	subjects := map[string]SubjectView{"sub_1": subject("perso", pigtail.StatusBuilding, 0, phase(pigtail.PhaseImplement, pigtail.PhaseActive, t0))}
	posed := binding(wsA)
	posed.AppliedTitle, posed.AppliedDescription = "Sidebar pigtail", "Method · building"
	posedWS := Workspace{ID: wsA, Title: "Sidebar pigtail", CustomTitle: "Sidebar pigtail", HasCustomTitle: true, Description: "Method · building"}

	userOwned := posed
	userOwned.TitleOwner, userOwned.DescriptionOwner = pigtail.CmuxOwnerUser, pigtail.CmuxOwnerUser

	type want struct {
		title, desc string
		takeovers   []Takeover
		reverts     []TitleRevert
	}
	cases := []struct {
		name     string
		ws       Workspace
		b        []pigtail.SubjectCmuxBinding
		subjects map[string]SubjectView
		lastW    map[string]time.Time
		want     want
	}{
		{"unbound workspace: neither title nor description", live(wsA), nil, subjects, nil, want{}},
		{"first pass on a bound workspace: short title + description",
			Workspace{ID: wsA, Title: "✳ claude", HasCustomTitle: false}, []pigtail.SubjectCmuxBinding{binding(wsA)}, subjects, nil,
			want{title: "Sidebar pigtail", desc: "Method · building"}},
		{"a custom title set by hand before the link is replaced (explicit link)",
			Workspace{ID: wsA, Title: "Metric login", CustomTitle: "Metric login", HasCustomTitle: true}, []pigtail.SubjectCmuxBinding{binding(wsA)}, subjects, nil,
			want{title: "Sidebar pigtail", desc: "Method · building"}},
		{"title still the one pigtail posed: kept", posedWS, []pigtail.SubjectCmuxBinding{posed}, subjects, nil,
			want{title: "Sidebar pigtail", desc: "Method · building"}},
		{"surrounding spaces are not a rename",
			Workspace{ID: wsA, CustomTitle: " Sidebar pigtail ", HasCustomTitle: true, Description: "Method · building "}, []pigtail.SubjectCmuxBinding{posed}, subjects, nil,
			want{title: "Sidebar pigtail", desc: "Method · building"}},
		{"renamed by hand: the short title comes back",
			Workspace{ID: wsA, CustomTitle: "Mon nom", HasCustomTitle: true, Description: "Method · building"}, []pigtail.SubjectCmuxBinding{posed}, subjects, nil,
			want{title: "Sidebar pigtail", desc: "Method · building", reverts: []TitleRevert{{WorkspaceID: wsA, SubjectID: "sub_1", Short: "Sidebar pigtail", Manual: "Mon nom"}}}},
		{"name cleared by hand in cmux: the short title comes back",
			Workspace{ID: wsA, Title: "✳ claude", HasCustomTitle: false, Description: "Method · building"}, []pigtail.SubjectCmuxBinding{posed}, subjects, nil,
			want{title: "Sidebar pigtail", desc: "Method · building", reverts: []TitleRevert{{WorkspaceID: wsA, SubjectID: "sub_1", Short: "Sidebar pigtail", Manual: "claude", Cleared: true}}}},
		{"description edited by hand (⌘⇧E)",
			Workspace{ID: wsA, CustomTitle: "Sidebar pigtail", HasCustomTitle: true, Description: "à moi"}, []pigtail.SubjectCmuxBinding{posed}, subjects, nil,
			want{title: "Sidebar pigtail", takeovers: []Takeover{{WorkspaceID: wsA, SubjectID: "sub_1", Field: FieldDescription}}}},
		{"listing read < 1.5 s after our own write is not trusted",
			Workspace{ID: wsA, Title: "✳ claude", HasCustomTitle: false}, []pigtail.SubjectCmuxBinding{posed}, subjects,
			map[string]time.Time{wsA: t0.Add(-time.Second)},
			want{title: "Sidebar pigtail", desc: "Method · building"}},
		{"listing read ≥ 1.5 s after our write is trusted",
			Workspace{ID: wsA, Title: "✳ claude", HasCustomTitle: false, Description: "Method · building"}, []pigtail.SubjectCmuxBinding{posed}, subjects,
			map[string]time.Time{wsA: t0.Add(-2 * time.Second)},
			want{title: "Sidebar pigtail", desc: "Method · building", reverts: []TitleRevert{{WorkspaceID: wsA, SubjectID: "sub_1", Short: "Sidebar pigtail", Manual: "claude", Cleared: true}}}},
		{"title_owner user is ignored: the short title is posed",
			Workspace{ID: wsA, CustomTitle: "Mon nom", HasCustomTitle: true, Description: "à moi"}, []pigtail.SubjectCmuxBinding{userOwned},
			map[string]SubjectView{"sub_1": {Subject: pigtail.Subject{ID: "sub_1", ShortTitle: "Nouveau titre", Scope: "perso", Status: "designing"}}}, nil,
			want{title: "Nouveau titre", reverts: []TitleRevert{{WorkspaceID: wsA, SubjectID: "sub_1", Short: "Nouveau titre", Manual: "Mon nom"}}}},
		{"subject not visible to the loop: touch nothing", posedWS, []pigtail.SubjectCmuxBinding{posed}, map[string]SubjectView{}, nil, want{}},
		{"done subject keeps its link and reads done", posedWS, []pigtail.SubjectCmuxBinding{posed},
			map[string]SubjectView{"sub_1": subject("perso", pigtail.StatusDone, 0)}, nil,
			want{title: "Sidebar pigtail", desc: "Method · done"}},
		{"derived short title when none is set", live(wsA), []pigtail.SubjectCmuxBinding{binding(wsA)},
			map[string]SubjectView{"sub_1": {Subject: pigtail.Subject{ID: "sub_1", Title: "Trier les workspaces cmux par attention puis rang pigtail", Scope: "perso", Status: "designing"}}}, nil,
			want{title: "Trier les workspaces cmux…", desc: "perso · designing"}},
		{"a title cmux would read as a flag is defused", live(wsA), []pigtail.SubjectCmuxBinding{binding(wsA)},
			map[string]SubjectView{"sub_1": {Subject: pigtail.Subject{ID: "sub_1", ShortTitle: "--help", Status: "designing"}}}, nil,
			want{title: "help", desc: "designing"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := Compute(Input{Now: t0, Workspaces: []Workspace{c.ws}, Bindings: c.b, Subjects: c.subjects, LastTextWrite: c.lastW})
			d := res.Desired[wsA]
			if d.Title != c.want.title || d.Description != c.want.desc {
				t.Errorf("title/desc = %q / %q, want %q / %q", d.Title, d.Description, c.want.title, c.want.desc)
			}
			if !reflect.DeepEqual(res.Takeovers, c.want.takeovers) {
				t.Errorf("takeovers = %+v, want %+v", res.Takeovers, c.want.takeovers)
			}
			if !reflect.DeepEqual(res.Reverts, c.want.reverts) {
				t.Errorf("reverts = %+v, want %+v", res.Reverts, c.want.reverts)
			}
		})
	}
}

func TestComputeBindingOfClosedWorkspaceIgnored(t *testing.T) {
	t.Parallel()
	res := Compute(Input{Now: t0, Workspaces: []Workspace{live(wsB)}, Bindings: []pigtail.SubjectCmuxBinding{binding(wsA)},
		Subjects: map[string]SubjectView{"sub_1": subject("perso", "designing", 0)}})
	if _, ok := res.Desired[wsA]; ok {
		t.Fatal("a closed workspace must have no desired state")
	}
	if d := res.Desired[wsB]; d.Title != "" || d.Description != "" {
		t.Fatalf("unbound workspace got text: %+v", d)
	}
}

// One subject, several workspaces (research in one, implement in the
// other): all renamed alike.
func TestComputeSubjectOnSeveralWorkspaces(t *testing.T) {
	t.Parallel()
	res := Compute(Input{Now: t0, Workspaces: []Workspace{live(wsA), live(wsB)},
		Bindings: []pigtail.SubjectCmuxBinding{binding(wsA), binding(wsB)},
		Subjects: map[string]SubjectView{"sub_1": subject("perso", "designing", 0)}})
	if res.Desired[wsA].Title != "Sidebar pigtail" || res.Desired[wsB].Title != "Sidebar pigtail" {
		t.Fatalf("desired = %+v", res.Desired)
	}
}

// heldMac is a held Mac wait (the Mac-hold helper), holder pid 4242.
func heldMac() Wait {
	return Wait{Type: waittype.Mac, Since: t0.Add(-time.Minute), HolderPID: 4242}
}

// A held wait shows like a declared one, the more severe of the two winning
// (waittype.Order): Mac sits just above Your turn, so over Review, Done and
// Working, below the waits that ask the user something else.
func TestWaitOfHeld(t *testing.T) {
	t.Parallel()
	limit := Wait{Type: waittype.Limit, Since: t0.Add(-time.Minute), Until: t0.Add(time.Hour)}
	cases := []struct {
		name     string
		status   string
		auto     Wait
		declared waittype.Type
		held     bool
		want     waittype.Type // the type shown
		waits    bool          // false: no wait shown
	}{
		{"idle working + held", "idle", Wait{}, waittype.Working, true, waittype.Mac, true},
		{"idle question + held", "idle", Wait{}, waittype.Question, true, waittype.Question, true},
		{"idle held alone", "idle", Wait{}, waittype.Unknown, true, waittype.Mac, true},
		{"waiting: gravest of declared and held", "waiting", Wait{}, waittype.Working, true, waittype.Mac, true},
		{"waiting: held over declared review", "waiting", Wait{}, waittype.Review, true, waittype.Mac, true},
		{"idle done + held", "idle", Wait{}, waittype.Done, true, waittype.Mac, true},
		{"waiting: held alone", "waiting", Wait{}, waittype.Unknown, true, waittype.Mac, true},
		{"idle limit + held", "idle", limit, waittype.Unknown, true, waittype.Limit, true},
		{"busy", "busy", Wait{}, waittype.Unknown, true, waittype.Unknown, false},
	}
	for _, c := range cases {
		s := waitingSess("s1", c.status)
		s.Auto = c.auto
		if c.declared != waittype.Unknown {
			s.Declared = Wait{Type: c.declared, Since: t0.Add(-time.Minute)}
		}
		if c.held {
			s.Held = heldMac()
		}
		waits, typ, _ := waitOf(s, time.UTC)
		if waits != c.waits || typ != c.want {
			t.Errorf("%s: waitOf = %v %q, want %v %q", c.name, waits, typ, c.waits, c.want)
		}
	}
}

// A Mac pill with no reason reads "Waiting for Mac" (no release command
// set), a grey Working beside it included; shared with a Your turn of the
// same colour (orange) it reads "Waiting for Mac ×2" (F11).
func TestPillValueMac(t *testing.T) {
	t.Parallel()
	s := waitingSess("s1", "idle")
	s.Held = heldMac()
	if v := computeOne(t, s).Statuses()[KeyAttention].Value; v != "Waiting for Mac" {
		t.Errorf("held alone: pill %q", v)
	}
	w := sess("s2", wsA, "S2", "idle", true)
	w.NativeStatusAt = t0
	w.Declared = Wait{Type: waittype.Working, Reason: "CI", Since: t0.Add(-time.Minute)}
	if v := computeOne(t, s, w).Statuses()[KeyAttention].Value; v != "Waiting for Mac" {
		t.Errorf("held + working: pill %q", v)
	}
	y := sess("s3", wsA, "S3", "idle", true)
	y.NativeStatusAt = t0
	y.BusySeen = true
	if v := computeOne(t, s, y).Statuses()[KeyAttention].Value; v != "Waiting for Mac ×2" {
		t.Errorf("held + your turn: pill %q", v)
	}
}

// A live held Mac wait is never hidden by the turn-end Your turn nor by a
// green state: Waiting for Mac ranks just above Your turn (waittype.Order).
// In one session the held wait already replaces Your turn (waitOf); in a
// workspace it wins over another session's Your turn, Review or Done, while
// the waits above it keep their rank (a Question still wins).
func TestMacOutranksYourTurn(t *testing.T) {
	t.Parallel()
	mac := waitingSess("s1", "idle") // the Mac-hold helper in the background, turn ended undeclared
	mac.BusySeen = true
	mac.Held = heldMac()
	if v := computeOne(t, mac).Statuses()[KeyAttention].Value; v != "Waiting for Mac" {
		t.Errorf("one session, held + turn ended: pill %q", v)
	}

	turn := sess("s2", wsA, "S2", "idle", true) // a second session whose turn ended
	turn.NativeStatusAt = t0
	turn.BusySeen = true
	// Both orange: the pill counts them (D15) and drops the reason.
	if v := computeOne(t, mac, turn).Statuses()[KeyAttention].Value; v != "Waiting for Mac ×2" {
		t.Errorf("held + another session's Your turn: pill %q", v)
	}
	if v := computeOne(t, turn, mac).Statuses()[KeyAttention].Value; v != "Waiting for Mac ×2" {
		t.Errorf("Your turn first, then held: pill %q", v)
	}

	review := sess("s3", wsA, "S3", "idle", true)
	review.NativeStatusAt = t0
	review.Declared = Wait{Type: waittype.Review, Reason: "PR", Since: t0.Add(-time.Minute)}
	if d := computeOne(t, mac, turn, review); d.WaitType != waittype.Mac {
		t.Errorf("declared Review + held + Your turn: pill type %q, want mac", d.WaitType)
	}
	done := sess("s5", wsA, "S5", "idle", true)
	done.NativeStatusAt = t0
	done.Declared = Wait{Type: waittype.Done, Since: t0.Add(-time.Minute)}
	for _, order := range [][]Session{{done, mac}, {mac, done}} {
		if v := computeOne(t, order...).Statuses()[KeyAttention].Value; v != "Waiting for Mac" {
			t.Errorf("declared Done + held: pill %q, want the Mac", v)
		}
	}
	question := sess("s4", wsA, "S4", "idle", true)
	question.NativeStatusAt = t0
	question.Declared = Wait{Type: waittype.Question, Since: t0.Add(-time.Minute)}
	if d := computeOne(t, mac, question); d.WaitType != waittype.Question {
		t.Errorf("declared Question + held: pill type %q, want question", d.WaitType)
	}
	if d := computeOne(t, turn); d.WaitType != waittype.YourTurn {
		t.Errorf("no Mac: pill type %q, want your_turn", d.WaitType)
	}
	if d := computeOne(t, turn, review); d.WaitType != waittype.YourTurn {
		t.Errorf("no Mac, Your turn + Review: pill type %q, want your_turn (unchanged)", d.WaitType)
	}
}

// Account rotation (sub_x000000000000008, D12): a Limit whose reason starts
// with "rotation: " says on its pill that the rotation did not happen — with
// the reset (refused: not switched) or without (moved, resume prompt not
// sent). The whole reason never fits the 36-rune pill; it stays in the wait.
// A Limit from the base keeps its reset alone.
func TestFailurePillShowsRotationReason(t *testing.T) {
	t.Parallel()
	until := time.Date(2026, 10, 20, 15, 4, 0, 0, time.UTC)
	cases := []struct {
		reason string
		until  time.Time
		want   string
	}{
		{"rotation: not quit — no input box on screen", until, "Limit · 15:04 · not switched"},
		{"rotation: moved to j, resume prompt not sent — no turn started", time.Time{}, "Limit · switched, not resumed"},
		{"", until, "Limit · 15:04"},
	}
	for _, c := range cases {
		s := waitingSess("s1", "idle")
		s.Auto = Wait{Type: waittype.Limit, Reason: c.reason, Since: t0.Add(-time.Minute), Until: c.until}
		waits, typ, reason := waitOf(s, time.UTC)
		if !waits || typ != waittype.Limit {
			t.Fatalf("%q: waitOf = %v %q", c.reason, waits, typ)
		}
		got := pillValue(Desired{WaitType: typ, WaitReason: reason})
		if got != c.want || utf8.RuneCountInString(got) > pillMaxRunes {
			t.Errorf("%q: pill = %q, want %q", c.reason, got, c.want)
		}
	}
}

// The rotation's relaunch held after /exit: the session ended (SessionEnd),
// so no wait shows; the workspace's respawn-failed mark does, until a
// session starts on the surface again.
func TestRelaunchHeldPillAfterSessionEnd(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.r.deps.PidAlive = func(int) bool { return false }
	h.world.add(winA, wsA, "a")
	h.nativeOn("s1", wsA, rsSurface, 4242, "idle", h.now.Add(-time.Minute))
	h.alive["s1"] = false // /exit typed: SessionEnd
	h.r.RespawnMark(wsA, pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnFailed, SurfaceID: rsSurface})
	h.step(0)
	if p := h.pill(); p != "Blocked · respawn failed" {
		t.Fatalf("pill = %q", p)
	}
	h.step(3 * time.Minute)
	h.relaunchOn("s1", wsA, rsSurface, 4545, h.now, h.now.Add(500*time.Millisecond))
	h.settle()
	if p := h.pill(); p != "" {
		t.Fatalf("after Claude came back on the surface: %q", p)
	}
}

// withWaitCommands sets the user's own commands (settings.yaml) for one
// serial test and puts the previous ones back.
func withWaitCommands(t *testing.T, c waittype.Commands) {
	t.Helper()
	testutil.SerialOnly(t)
	prev := waittype.Configured()
	waittype.Configure(c)
	t.Cleanup(func() { waittype.Configure(prev) })
}

// With the user's commands set, a Done or a Mac pill with no reason names
// them; a reason given still wins.
func TestPillValueNamesConfiguredCommands(t *testing.T) {
	withWaitCommands(t, waittype.Commands{Done: "/finish", MacRelease: "/lend"})
	mac := waitingSess("s1", "idle")
	mac.Held = heldMac()
	if v := computeOne(t, mac).Statuses()[KeyAttention].Value; v != "Waiting for Mac · /lend" {
		t.Errorf("held Mac: pill %q", v)
	}
	done := sess("s2", wsA, "S2", "idle", true)
	done.NativeStatusAt = t0
	done.Declared = Wait{Type: waittype.Done, Since: t0.Add(-time.Minute)}
	if v := computeOne(t, done).Statuses()[KeyAttention].Value; v != "Done · /finish" {
		t.Errorf("Done: pill %q", v)
	}
	done.Declared.Reason = "PR #31"
	if v := computeOne(t, done).Statuses()[KeyAttention].Value; v != "Done · PR #31" {
		t.Errorf("Done with a reason: pill %q", v)
	}
}
