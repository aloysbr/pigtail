package cmuxsync

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/waittype"
)

// Every pill pigtail emits, one per state it can be in.
func everyEmittedPill() []cmux.StatusEntry {
	var out []cmux.StatusEntry
	dead := Desired{WorkspaceID: wsA, DeadSurfaces: 1}
	dead.resolve()
	out = append(out, dead.Statuses()[KeyAttention])
	for _, ty := range waittype.Order {
		if ty == waittype.Dead {
			continue // a display state resolve picks from DeadSurfaces, above
		}
		for _, d := range []Desired{liveDesired(ty, 1, "PR #9"), liveDesired(ty, 3, "")} {
			out = append(out, d.Statuses()[KeyAttention])
		}
	}
	for _, st := range []ServerState{ServerUp, ServerStopped, ServerDead} {
		e, _ := serversPill([]Server{{Name: "dev+web", Port: 3070, State: st}, {Name: "storybook", Port: 6006, State: ServerUp}})
		out = append(out, e)
	}
	for _, m := range []Desired{
		{Maintenance: &MaintenancePill{Value: "Maintenance · freeze", Color: waittype.Orange.Color()}},
		{Maintenance: &MaintenancePill{Value: "Maintenance · late", Color: waittype.Red.Color(), OwnerValue: "Maintenance · late · set here"}, MaintenanceHere: true},
	} {
		out = append(out, m.Statuses()[KeyMaintenance])
	}
	return out
}

// Apports DS: every pill pigtail emits wears one of the four gravity colours.
func TestEveryPillColourIsAGravity(t *testing.T) {
	t.Parallel()
	ok := map[string]bool{}
	for g := waittype.Grey; g <= waittype.Red; g++ {
		ok[g.Color()] = true
	}
	for _, e := range everyEmittedPill() {
		if !ok[e.Color] {
			t.Errorf("%s=%q wears %s, outside the gravity table", e.Key, e.Value, e.Color)
		}
	}
}

// cmux draws the pill's icon (an SF Symbol) in front of its value: a glyph
// at the head of the value would show the sign twice (`🔔 ⏳ …`, `▶ ▶ …`).
// Every value starts with a letter or a digit, and every pill has its icon.
func TestPillValuesCarryNoLeadingGlyph(t *testing.T) {
	t.Parallel()
	for _, e := range everyEmittedPill() {
		if e.Icon == "" {
			t.Errorf("%s=%q has no icon", e.Key, e.Value)
		}
		r, _ := utf8.DecodeRuneInString(e.Value)
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			t.Errorf("%s=%q starts with %q, which doubles its icon %s", e.Key, e.Value, r, e.Icon)
		}
		for _, g := range []string{"⏳", "▶", "🔔", "✕", "■"} {
			if strings.Contains(e.Value, g) {
				t.Errorf("%s=%q carries the glyph %q", e.Key, e.Value, g)
			}
		}
	}
}

// The labels read in English (decision of 2026-10-01).
func TestPillLabelsInEnglish(t *testing.T) {
	t.Parallel()
	cases := []struct {
		d    Desired
		want string
	}{
		{Desired{Attention: AttentionWaiting, WaitingCount: 1}, "Waiting for you"},
		{Desired{Attention: AttentionWaiting, WaitingCount: 2}, "Waiting for you ×2"},
		{Desired{Attention: AttentionDead}, "Dead"},
	}
	for _, c := range cases {
		if got := c.d.Statuses()[KeyAttention].Value; got != c.want {
			t.Errorf("%+v → %q, want %q", c.d, got, c.want)
		}
	}
	for st, want := range map[ServerState]string{ServerUp: "dev :5173 up", ServerStopped: "dev :5173 stopped", ServerDead: "dev :5173 dead"} {
		e, _ := serversPill([]Server{{Name: "dev", Port: 5173, State: st}})
		if e.Value != want {
			t.Errorf("%s → %q, want %q", st, e.Value, want)
		}
	}
}

// Server names are shortened on the pill through one table: the line is
// narrow and cut from the second server on. Twins on one port show a single
// name — the first of the priority table, else the shortest.
func TestServerDisplayName(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, want string }{
		{"dev", "dev"},
		{"storybook", "sb"},
		{"web", "web"},
		{"ui-styleguide", "ui-styleguide"},
		{"dev+web", "dev"},
		{"storybook+ui-styleguide", "sb"},
		{"api+web", "api"},       // no table entry: the shortest
		{"admin+web", "web"},     // the shortest, not the first
		{"docs+site", "docs"},    // same length: alphabetical
		{"storybook+dev", "dev"}, // dev ranks before sb in the table
		{"", ""},
	}
	for _, c := range cases {
		if got := serverDisplayName(c.name); got != c.want {
			t.Errorf("serverDisplayName(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// The pill sorts on the names it shows, and the wire keeps the real names.
func TestServerPillShortNames(t *testing.T) {
	t.Parallel()
	ss := []Server{
		{WorkspaceID: wsA, Name: "storybook+ui-styleguide", Port: 6006, State: ServerUp},
		{WorkspaceID: wsA, Name: "dev+web", Port: 3009, State: ServerUp},
	}
	e, _ := serversPill(ss)
	if e.Value != "dev :3009 up · sb :6006 up" {
		t.Fatalf("pill %q", e.Value)
	}
	w := wireServers(ss)
	if w[0].Name != "storybook+ui-styleguide" || w[1].Name != "dev+web" {
		t.Fatalf("wire names changed: %+v", w)
	}
}

// After the upgrade, the pills cmux still shows carry the old French text
// with its glyph. The first Diff after the reconcile rewrites them.
func TestDiffRewritesOldLabels(t *testing.T) {
	t.Parallel()
	old := []cmux.StatusEntry{
		{Key: KeyAttention, Value: "⏳ en attente de toi ×2", Icon: "bell.fill", Color: "#FF9500", Priority: attentionPriority},
		{Key: KeyServers, Value: "▶ dev+web :3070 up", Icon: "play.fill", Color: "#34C759", Priority: serversPriority},
	}
	d := Desired{WorkspaceID: wsA, Attention: AttentionWaiting, WaitingCount: 2,
		Servers: []Server{{WorkspaceID: wsA, Name: "dev+web", Port: 3070, State: ServerUp}}}
	ops := Diff(map[string]Desired{wsA: d}, map[string]Applied{wsA: SeedApplied(old, nil)})
	if len(ops) != 2 {
		t.Fatalf("ops %+v", ops)
	}
	if ops[0].Kind != OpSetStatus || ops[0].Status.Value != "Waiting for you ×2" {
		t.Errorf("attention op %+v", ops[0])
	}
	if ops[1].Kind != OpSetStatus || ops[1].Status.Value != "dev :3070 up" {
		t.Errorf("server op %+v", ops[1])
	}

	// The grey Dead posed before this lot is rewritten red, filled octagon.
	deadOld := []cmux.StatusEntry{{Key: KeyAttention, Value: "morte", Icon: "xmark.octagon", Color: "#8E8E93", Priority: attentionPriority}}
	ops = Diff(map[string]Desired{wsA: {WorkspaceID: wsA, Attention: AttentionDead}}, map[string]Applied{wsA: SeedApplied(deadOld, nil)})
	if len(ops) != 1 || ops[0].Status.Value != "Dead" || ops[0].Status.Icon != "xmark.octagon.fill" || ops[0].Status.Color != "#FF3B30" {
		t.Fatalf("dead ops %+v", ops)
	}
}

// End to end: a daemon started over a sidebar posed by the previous version
// rewrites the old pill on its first pass, then leaves it alone.
func TestRunnerRewritesOldLabelOnFirstPass(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	f := withServers(h)
	h.world.add(winA, wsA, "a")
	h.world.status[wsA][KeyServers] = cmux.StatusEntry{Key: KeyServers, Value: "▶ dev :5173 up", Icon: "play.fill", Color: "#34C759", Priority: 60}
	f.snap = ServerSnapshot{Up: []ServerObs{up("dev", 5173, wsA)}}
	h.step(0)
	if srvPill(h, wsA) != "dev :5173 up" {
		t.Fatalf("old pill not rewritten: %q", srvPill(h, wsA))
	}
	h.world.calls = nil
	h.step(1)
	for _, c := range h.world.calls {
		if strings.HasPrefix(c, "set-status") {
			t.Fatalf("rewritten again: %q", h.world.calls)
		}
	}
}
