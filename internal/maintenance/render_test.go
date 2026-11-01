package maintenance

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/waittype"
)

func TestRenderOnFreezeOther(t *testing.T) {
	s := Render(onState(), t0.Add(time.Hour), "other", "perso")
	for _, want := range []string{"Message système pigtail — le system est en maintenance depuis", "bascule scope", "Fin prévue à 16:00.", "GEL", "pigtail new", "`pigtail new` et `pigtail dispatch`", "`pigtail session spawn` est refusé. Ne relance", "tools-scope"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "EN RETARD") {
		t.Fatal("not late yet")
	}
}

func TestRenderLateExemptLifted(t *testing.T) {
	st := onState()
	if s := Render(st, t0.Add(3*time.Hour), "other", "perso"); !strings.Contains(s, "EN RETARD") {
		t.Fatalf("late: %s", s)
	}
	if s := Render(st, t0, "sid-lead", "perso"); !strings.Contains(s, "session exemptée") {
		t.Fatalf("exempt: %s", s)
	}
	l := t0.Add(time.Hour)
	st.On, st.LiftedAt = false, &l
	if s := Render(st, l, "other", "perso"); !strings.Contains(s, "Message système pigtail — la maintenance du system (mnt_0123456789abcdef) est levée à") {
		t.Fatalf("lifted: %s", s)
	}
}

func TestPillValue(t *testing.T) {
	st := onState()
	v, c, ok := PillValue(st, t0)
	if !ok || v != "Maintenance · freeze" || c != "#FF9500" {
		t.Fatalf("%q %q %v", v, c, ok)
	}
	v, c, _ = PillValue(st, t0.Add(3*time.Hour))
	if v != "Maintenance · freeze · late" || c != "#FF3B30" || utf8.RuneCountInString(v) > 36 {
		t.Fatalf("%q %q", v, c)
	}
	st.Freeze = false
	if v, _, _ = PillValue(st, t0); v != "Maintenance" {
		t.Fatalf("%q", v)
	}
	if v, c, _ = PillValue(st, t0.Add(3*time.Hour)); v != "Maintenance · late" || c != "#FF3B30" {
		t.Fatalf("%q %q", v, c)
	}
	st.On = false
	if _, _, ok = PillValue(st, t0); ok {
		t.Fatal("lifted: no pill")
	}
	if v, c := ErrorPill(); v != "Maintenance · state error" || c != "#FF3B30" || utf8.RuneCountInString(v) > 36 {
		t.Fatalf("error pill %q %q", v, c)
	}
}

func TestOwnerPillValue(t *testing.T) {
	st := onState()
	st.Owner = "sid-owner"

	v, c, ok := OwnerPillValue(st, t0, 36)
	if !ok || v != "Maintenance · freeze · set here" || c != "#FF9500" {
		t.Fatalf("freeze: %q %q %v", v, c, ok)
	}
	// freeze + late = 38 runes: " · late" goes first, red says it; "set here" stays.
	v, c, _ = OwnerPillValue(st, t0.Add(3*time.Hour), 36)
	if v != "Maintenance · freeze · set here" || c != "#FF3B30" || utf8.RuneCountInString(v) > 36 {
		t.Fatalf("freeze+late: %q %q", v, c)
	}
	// With room, late is kept.
	if v, _, _ = OwnerPillValue(st, t0.Add(3*time.Hour), 40); v != "Maintenance · freeze · late · set here" {
		t.Fatalf("freeze+late, room: %q", v)
	}
	st.Freeze = false
	if v, c, _ = OwnerPillValue(st, t0.Add(3*time.Hour), 36); v != "Maintenance · late · set here" || c != "#FF3B30" {
		t.Fatalf("late: %q %q", v, c)
	}
	if v, _, _ = OwnerPillValue(st, t0, 36); v != "Maintenance · set here" {
		t.Fatalf("plain: %q", v)
	}
	// A budget too small for anything optional still keeps "set here".
	st.Freeze = true
	if v, _, _ = OwnerPillValue(st, t0.Add(3*time.Hour), 10); v != "Maintenance · set here" {
		t.Fatalf("tiny budget: %q", v)
	}
	// D2: no owner (the user outside Claude Code) → no owner pill.
	st.Owner = ""
	if _, _, ok = OwnerPillValue(st, t0, 36); ok {
		t.Fatal("empty owner must give no owner pill")
	}
	st.Owner, st.On = "sid-owner", false
	if _, _, ok = OwnerPillValue(st, t0, 36); ok {
		t.Fatal("lifted maintenance must give no owner pill")
	}
}

func TestFrozenMessage(t *testing.T) {
	m := FrozenMessage(onState(), "perso")
	for _, want := range []string{"since 14:00", "bascule scope", "pigtail maintenance off", "set by the user"} {
		if !strings.Contains(m, want) {
			t.Fatalf("missing %q in %q", want, m)
		}
	}
}

// The lift says « Tout est rouvert » only when the maintenance had a freeze,
// or when that is unknown (prudence); never for a known freeze-less one.
func TestRenderLiftReopenSentence(t *testing.T) {
	l := t0.Add(time.Hour)
	reopen := "Tout est rouvert : écritures de sujets, `pigtail new`, `pigtail dispatch`, `pigtail session spawn`."
	for _, c := range []struct {
		name          string
		freeze, known bool
		want          bool
	}{{"freeze", true, true, true}, {"no freeze", false, true, false}, {"unknown", false, false, true}} {
		s := Render(State{ID: "mnt_a", LiftedAt: &l, Freeze: c.freeze, LiftedFreezeKnown: c.known}, l, "other", "perso")
		if strings.Contains(s, reopen) != c.want || !strings.Contains(s, "Reprends normalement, et dis-le à the user dans ta prochaine réponse.") {
			t.Fatalf("%s: %s", c.name, s)
		}
	}
}

func ownedState() State {
	st := onState()
	st.Owner, st.OwnerLabel = "sid-owner", "Alpha · pigtail (h1)"
	return st
}

func TestRenderSetByHuman(t *testing.T) { // replaces the « the user seul » expectation of TestRenderOnFreezeOther
	s := Render(onState(), t0.Add(time.Hour), "other", "perso")
	if !strings.Contains(s, "Posée par the user — levée par lui seul (`pigtail maintenance off` hors de Claude Code).") || strings.Contains(s, "the user seul") {
		t.Fatalf("%s", s)
	}
}

func TestRenderNamesOwnerToOthers(t *testing.T) {
	s := Render(ownedState(), t0.Add(time.Hour), "other", "perso")
	for _, want := range []string{"Posée par la session Alpha · pigtail (h1) — levée par elle ou par the user (`pigtail maintenance off`).",
		"Ne la lève pas toi-même : pigtail refuse toute autre session."} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
}

func TestRenderOwnerVariant(t *testing.T) { // D18
	own := "Tu as posé cette maintenance : c'est à toi de la lever (`pigtail maintenance off`) dès la fin du geste."
	for _, freeze := range []bool{true, false} {
		st := ownedState()
		st.Freeze = freeze
		s := Render(st, t0.Add(time.Hour), "sid-owner", "perso")
		if !strings.Contains(s, own) || strings.Contains(s, "Ne la lève pas toi-même") ||
			strings.Contains(s, "Termine ou mets en pause ce qui touche le system.") ||
			strings.Contains(s, "Le gel ne s'applique pas à toi.") != freeze {
			t.Fatalf("freeze=%v:\n%s", freeze, s)
		}
	}
	// a third-party session still gets the « Termine ou mets en pause » line
	if s := Render(ownedState(), t0.Add(time.Hour), "other", "perso"); !strings.Contains(s, "Termine ou mets en pause ce qui touche le system.") {
		t.Fatalf("%s", s)
	}
}

func TestRenderExemptWithOwner(t *testing.T) { // S23
	st := ownedState() // Owner sid-owner « Alpha · pigtail (h1) », Exempt sid-lead « tools-scope »
	if s := Render(st, t0.Add(time.Hour), "sid-lead", "perso"); !strings.Contains(s, "Tu es aussi exemptée du gel (la session Alpha · pigtail (h1) mène le chantier).") ||
		strings.Contains(s, "Tu es la session exemptée qui mène le chantier") {
		t.Fatalf("exempt: %s", s)
	}
	if s := Render(st, t0.Add(time.Hour), "other", "perso"); !strings.Contains(s, "Session aussi exemptée du gel : tools-scope.") {
		t.Fatalf("other: %s", s)
	}
	if s := Render(onState(), t0, "sid-lead", "perso"); !strings.Contains(s, "Tu es la session exemptée qui mène le chantier") { // no owner: unchanged
		t.Fatalf("no owner: %s", s)
	}
}

func TestRenderLateNamesNobody(t *testing.T) {
	s := Render(ownedState(), t0.Add(3*time.Hour), "other", "perso")
	if !strings.Contains(s, "EN RETARD : la maintenance continue jusqu'à sa levée") || strings.Contains(s, "qu'the user la lève") {
		t.Fatalf("%s", s)
	}
}

func TestRenderLiftNamesLifter(t *testing.T) {
	l := t0.Add(time.Hour)
	for _, c := range []struct {
		by    string
		human bool
		want  string
	}{{"", true, "est levée à 15:00 par the user."}, {"Alpha", false, "est levée à 15:00 par la session Alpha."}, {"", false, "est levée à 15:00."}} {
		s := Render(State{ID: "mnt_a", LiftedAt: &l, LiftedBy: c.by, LiftedByUser: c.human, Freeze: true, LiftedFreezeKnown: true}, l, "other", "perso")
		if !strings.Contains(s, c.want) {
			t.Fatalf("%+v: %s", c, s)
		}
	}
}

func TestFrozenMessageNamesOwner(t *testing.T) { // replaces the bare "the user" expectation of TestFrozenMessage
	if m := FrozenMessage(ownedState(), "perso"); !strings.Contains(m, "set by session Alpha · pigtail (h1) — lifted by it or by the user (`pigtail maintenance off`)") {
		t.Fatalf("%q", m)
	}
	if m := FrozenMessage(onState(), "perso"); !strings.Contains(m, "set by the user — only they lift it: `pigtail maintenance off` outside Claude Code") || strings.Contains(m, "only the user lifts it") {
		t.Fatalf("%q", m)
	}
}

// S22: the CLI refusal to a session on an unreadable state — one mention of
// who repairs, no repair instruction for the session, the path once.
func TestUnreadableForSession(t *testing.T) {
	m := UnreadableForSession("/d", errors.New("unexpected EOF"))
	if !strings.Contains(m, "maintenance state unreadable (/d/maintenance.json: unexpected EOF) — everything the freeze covers stays frozen") ||
		!strings.Contains(m, "only the user, outside Claude Code, repairs it; do not run `off`") ||
		strings.Contains(m, "repair:") || strings.Count(m, "/d/maintenance.json") != 1 {
		t.Fatalf("%q", m)
	}
}

// A3: the maintenance pill takes its colours from the gravity table — no hex
// in this package (gravity.go: "a new state picks a gravity, never a hex").
func TestMaintenanceColoursComeFromGravity(t *testing.T) {
	src, err := os.ReadFile("render.go")
	if err != nil {
		t.Fatal(err)
	}
	if hex := regexp.MustCompile(`#[0-9A-Fa-f]{6}`).FindAllString(string(src), -1); len(hex) > 0 {
		t.Fatalf("hex colours in render.go: %v", hex)
	}
	now := time.Now()
	if _, c, _ := PillValue(State{ID: "m", On: true}, now); c != waittype.Orange.Color() {
		t.Errorf("on: %s", c)
	}
	late := now.Add(-time.Minute)
	if _, c, _ := PillValue(State{ID: "m", On: true, Until: &late}, now); c != waittype.Red.Color() {
		t.Errorf("late: %s", c)
	}
	if _, c := ErrorPill(); c != waittype.Red.Color() {
		t.Errorf("error: %s", c)
	}
}

// withFreezeAlso sets FreezeAlsoRefuses for one (non-parallel) test.
func withFreezeAlso(t *testing.T, gs ...string) {
	t.Helper()
	prev := FreezeAlsoRefuses
	FreezeAlsoRefuses = gs
	t.Cleanup(func() { FreezeAlsoRefuses = prev })
}

// The gestures the user's own guards refuse during a freeze: unset, the
// notice and the summary name pigtail's refusals only; set, they name them
// too, in the freeze line, the lift's reopen list and the CLI summary.
func TestRenderFreezeAlsoRefuses(t *testing.T) {
	withFreezeAlso(t)
	on := Render(onState(), t0.Add(time.Hour), "other", "perso")
	if strings.Contains(on, "aux agents") || FreezeSummary() != "subject writes, pigtail new, pigtail dispatch, pigtail session spawn" {
		t.Fatalf("unset: %q\n%s", FreezeSummary(), on)
	}
	withFreezeAlso(t, "deploy --prod", "release publish")
	on = Render(onState(), t0.Add(time.Hour), "other", "perso")
	if !strings.Contains(on, "`pigtail session spawn` est refusé ; `deploy --prod` est refusé aux agents ; `release publish` est refusé aux agents. Ne relance") {
		t.Fatalf("set, on:\n%s", on)
	}
	l := t0.Add(time.Hour)
	off := Render(State{ID: "mnt_a", LiftedAt: &l, Freeze: true, LiftedFreezeKnown: true}, l, "other", "perso")
	if !strings.Contains(off, "`pigtail session spawn`, `deploy --prod`, `release publish`.") {
		t.Fatalf("set, lift:\n%s", off)
	}
	if got := FreezeSummary(); got != "subject writes, pigtail new, pigtail dispatch, pigtail session spawn, agents' deploy --prod, agents' release publish" {
		t.Fatalf("summary %q", got)
	}
}
