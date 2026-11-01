package maintenance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var n0 = time.Unix(1_800_000_000, 0)

// step runs one hook turn for a session: decide, then keep the witness the
// way the hook does. Returns the phase shown, "" when nothing is shown.
func step(t *testing.T, dir, sid string, now time.Time) string {
	t.Helper()
	d := Decide(dir, ReadWitness(dir, sid), now, sid, "perso")
	if err := WriteWitness(dir, sid, d.Next); err != nil {
		t.Fatal(err)
	}
	if d.Show {
		return d.Phase
	}
	return ""
}

func on(t *testing.T, dir string, until time.Duration) {
	u := n0.Add(until)
	if err := Save(dir, State{ID: "mnt_a", On: true, Reason: "bascule", Until: &u, Since: n0}); err != nil {
		t.Fatal(err)
	}
}

func TestDecideOnOnce(t *testing.T) { // on → on
	dir := t.TempDir()
	on(t, dir, time.Hour)
	if got := step(t, dir, "s1", n0); got != "on" {
		t.Fatalf("first: %q", got)
	}
	for i := 1; i <= 3; i++ {
		if got := step(t, dir, "s1", n0.Add(time.Duration(i)*time.Minute)); got != "" {
			t.Fatalf("repeat %d: %q", i, got)
		}
	}
}

func TestDecideOnThenLateOnce(t *testing.T) { // on → late
	dir := t.TempDir()
	on(t, dir, time.Hour)
	step(t, dir, "s1", n0)
	if got := step(t, dir, "s1", n0.Add(2*time.Hour)); got != "late" {
		t.Fatalf("late: %q", got)
	}
	if got := step(t, dir, "s1", n0.Add(3*time.Hour)); got != "" {
		t.Fatalf("late twice: %q", got)
	}
}

func TestDecideOnThenOffOnce(t *testing.T) { // on → off
	dir := t.TempDir()
	on(t, dir, time.Hour)
	step(t, dir, "s1", n0)
	_ = Lift(dir, "mnt_a", n0.Add(10*time.Minute), "", "", "")
	if got := step(t, dir, "s1", n0.Add(11*time.Minute)); got != "off" {
		t.Fatalf("off: %q", got)
	}
	if got := step(t, dir, "s1", n0.Add(12*time.Minute)); got != "" {
		t.Fatalf("off twice: %q", got)
	}
}

// Saw on, idle for days after off: the lift is still shown, once (no grace).
func TestDecideIdleDaysThenOffOnce(t *testing.T) {
	dir := t.TempDir()
	on(t, dir, time.Hour)
	step(t, dir, "s1", n0) // saw on, then goes idle
	_ = Lift(dir, "mnt_a", n0.Add(time.Hour), "", "", "")
	later := n0.Add(72 * time.Hour)
	if got := step(t, dir, "s1", later); got != "off" {
		t.Fatalf("lift lost after an idle period: %q", got)
	}
	if got := step(t, dir, "s1", later.Add(time.Minute)); got != "" {
		t.Fatalf("off twice: %q", got)
	}
}

func TestDecideNeverSawOffIsSilent(t *testing.T) { // never → off = 0
	dir := t.TempDir()
	on(t, dir, time.Hour)
	_ = Lift(dir, "mnt_a", n0.Add(time.Minute), "", "", "")
	if got := step(t, dir, "fresh", n0.Add(2*time.Minute)); got != "" {
		t.Fatalf("a session that never saw it was told of the lift: %q", got)
	}
	_ = WriteWitness(dir, "other", Witness{Key: "mnt_b:on", Shown: "mnt_b:on"}) // saw ANOTHER maintenance
	if got := step(t, dir, "other", n0.Add(2*time.Minute)); got != "" {
		t.Fatalf("wrong id: %q", got)
	}
}

func TestDecideCorruptOnce(t *testing.T) { // corrupt → corrupt
	dir := t.TempDir()
	on(t, dir, time.Hour)
	step(t, dir, "s1", n0) // saw on
	_ = os.WriteFile(filepath.Join(dir, FileName), []byte("{"), 0o600)
	d := Decide(dir, ReadWitness(dir, "s1"), n0.Add(time.Minute), "s1", "perso")
	want := "Message système pigtail — l'état de maintenance est illisible (" + filepath.Join(dir, FileName) + " : "
	if !d.Show || d.Phase != "corrupt" || !strings.HasPrefix(d.Text, want) ||
		!strings.Contains(d.Text, "Réparation par the user seul, hors de Claude Code (`pigtail maintenance off`) : qui l'a posée est illisible aussi.") ||
		!strings.Contains(d.Text, "Dis-lui dans ta prochaine réponse que tu as reçu cet avis.") {
		t.Fatalf("corrupt notice: %+v", d)
	}
	_ = WriteWitness(dir, "s1", d.Next)
	if got := step(t, dir, "s1", n0.Add(2*time.Minute)); got != "" {
		t.Fatalf("corrupt twice: %q", got)
	}
	if got := step(t, dir, "s1", n0.Add(2*time.Hour)); got != "" { // key on → late, still corrupt
		t.Fatalf("corrupt re-shown on the late key change: %q", got)
	}
	if got := step(t, dir, "new", n0.Add(2*time.Hour)); got != "corrupt" { // a new session: once
		t.Fatalf("new session: %q", got)
	}
}

// JSON corrupt AND token unreadable: corrupt under mnt_unknown, then `off`
// (Lift with RawTokenID) writes a readable lift the same sessions receive.
func TestDecideCorruptBadTokenThenOff(t *testing.T) {
	dir := t.TempDir()
	on(t, dir, time.Hour)
	_ = os.WriteFile(filepath.Join(dir, FileName), []byte("{"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, TokenName), []byte("garbage\n"), 0o600)
	if got := step(t, dir, "s1", n0); got != "corrupt" {
		t.Fatalf("corrupt: %q", got)
	}
	if id := RawTokenID(dir); id != UnknownID {
		t.Fatalf("id %q", id)
	}
	_ = Lift(dir, RawTokenID(dir), n0.Add(time.Minute), "", "", "")
	if k := TokenKey(dir, n0.Add(2*time.Minute)); k != UnknownID+":off" {
		t.Fatalf("lift token unreadable: %q", k)
	}
	if got := step(t, dir, "s1", n0.Add(2*time.Minute)); got != "off" {
		t.Fatalf("lift after corrupt+bad token: %q", got)
	}
}

func TestDecideCorruptThenOff(t *testing.T) { // corrupt → off
	dir := t.TempDir()
	on(t, dir, time.Hour)
	_ = os.WriteFile(filepath.Join(dir, FileName), []byte("{"), 0o600)
	step(t, dir, "s1", n0) // corrupt shown
	_ = Lift(dir, RawTokenID(dir), n0.Add(time.Minute), "", "", "")
	if got := step(t, dir, "s1", n0.Add(2*time.Minute)); got != "off" {
		t.Fatalf("off after corrupt: %q", got)
	}
	if got := step(t, dir, "s1", n0.Add(3*time.Minute)); got != "" {
		t.Fatalf("off twice: %q", got)
	}
}

func TestDecideBadTokenWithoutStateIsSilent(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, TokenName), []byte("garbage\n"), 0o600)
	if got := step(t, dir, "s1", n0); got != "" {
		t.Fatalf("no JSON = no maintenance: %q", got)
	}
	if w := ReadWitness(dir, "s1"); w.Key != "bad+-" {
		t.Fatalf("witness key must quiet the bash fast path: %+v", w)
	}
}

// off interrupted between the token and the JSON removal: the token says off,
// the JSON still says on — still frozen, nothing shown; the second off removes
// the JSON, the fast key changes (+j → +-), and the lift is shown once.
func TestDecideInterruptedOffThenOff(t *testing.T) {
	dir := t.TempDir()
	on(t, dir, time.Hour)
	step(t, dir, "s1", n0)
	_ = os.WriteFile(filepath.Join(dir, TokenName), []byte("mnt_a off 1800000060\n"), 0o600) // crash after the token
	if got := step(t, dir, "s1", n0.Add(2*time.Minute)); got != "" {
		t.Fatalf("half-done off: %q", got)
	}
	_ = Lift(dir, "mnt_a", n0.Add(3*time.Minute), "", "", "") // second off
	if got := step(t, dir, "s1", n0.Add(4*time.Minute)); got != "off" {
		t.Fatalf("second off: %q", got)
	}
	if got := step(t, dir, "s1", n0.Add(5*time.Minute)); got != "" {
		t.Fatalf("off twice: %q", got)
	}
}

func TestFastKey(t *testing.T) {
	dir := t.TempDir()
	if got := FastKey(dir, n0); got != "+-" {
		t.Fatalf("empty: %q", got)
	}
	on(t, dir, time.Hour)
	if got := FastKey(dir, n0); got != "mnt_a:on+j" {
		t.Fatalf("on: %q", got)
	}
	_ = Lift(dir, "mnt_a", n0, "", "", "")
	if got := FastKey(dir, n0.Add(time.Minute)); got != "mnt_a:off+-" {
		t.Fatalf("off: %q", got)
	}
}

// Contract shared with the hook's test script (T1–T7).
func TestTokenKeyContract(t *testing.T) {
	for _, c := range []struct{ name, line, want string }{
		{"T1", "", ""},
		{"T2", "mnt_a on 0", "mnt_a:on"},
		{"T3", "mnt_a on 1799990000", "mnt_a:late"},
		{"T4", "mnt_a on 1800010000", "mnt_a:on"},
		{"T5", "mnt_a off 1799996400", "mnt_a:off"},
		{"T6", "mnt_a off 1799827200", "mnt_a:off"}, // two days old: still the lift (no grace)
		{"T7", "garbage", "bad"},
	} {
		dir := t.TempDir()
		if c.line != "" {
			_ = os.WriteFile(filepath.Join(dir, TokenName), []byte(c.line+"\n"), 0o600)
		}
		if got := TokenKey(dir, n0); got != c.want {
			t.Errorf("%s %q: %q, want %q", c.name, c.line, got, c.want)
		}
	}
}
