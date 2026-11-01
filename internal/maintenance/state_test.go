package maintenance

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 4, 14, 0, 0, 0, time.Local)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func onState() State {
	until := t0.Add(2 * time.Hour)
	return State{ID: "mnt_0123456789abcdef", On: true, Reason: "bascule scope", Freeze: true,
		Until: &until, Exempt: "sid-lead", ExemptLabel: "tools-scope", Since: t0}
}

func TestSaveLoadRoundTripAndToken(t *testing.T) {
	dir := t.TempDir()
	st := onState()
	if err := Save(dir, st); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Load(dir)
	if err != nil || !ok || got.ID != st.ID || !got.Freeze || got.Exempt != "sid-lead" {
		t.Fatalf("Load = %+v %v %v", got, ok, err)
	}
	line, _ := os.ReadFile(filepath.Join(dir, TokenName))
	want := "mnt_0123456789abcdef on " + itoa(st.Until.Unix()) + "\n"
	if string(line) != want {
		t.Fatalf("token = %q, want %q", line, want)
	}
	for _, f := range []string{FileName, TokenName} {
		fi, _ := os.Stat(filepath.Join(dir, f))
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", f, fi.Mode().Perm())
		}
	}
}

func TestLoadAbsent(t *testing.T) {
	_, ok, err := Load(t.TempDir())
	if ok || err != nil {
		t.Fatalf("absent file: ok=%v err=%v", ok, err)
	}
}

func TestPhaseLateNeverExpires(t *testing.T) {
	st := onState()
	if st.Phase(t0.Add(time.Hour)) != "on" || st.Phase(t0.Add(3*time.Hour)) != "late" {
		t.Fatal("phase")
	}
	if st.Phase(t0.Add(24*365*time.Hour)) != "late" { // D4 : jamais d'expiration
		t.Fatal("expired")
	}
	if st.Token(t0.Add(3*time.Hour)) != "mnt_0123456789abcdef:late" {
		t.Fatal("token late")
	}
}

func TestLiftDeletesStateKeepsAnnouncement(t *testing.T) {
	dir := t.TempDir()
	st := onState()
	_ = Save(dir, st)
	lifted := t0.Add(time.Hour)
	if err := Lift(dir, st.ID, lifted, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("off must delete %s: %v", FileName, err)
	}
	line, _ := os.ReadFile(filepath.Join(dir, TokenName))
	if string(line) != st.ID+" off "+itoa(lifted.Unix())+"\n" {
		t.Fatalf("token = %q", line)
	}
	got, ok, err := Load(dir)
	if err != nil || !ok || got.On || got.ID != st.ID || got.LiftedAt == nil || got.FrozenFor("x") {
		t.Fatalf("lifted state from the token: %+v %v %v", got, ok, err)
	}
	if got.Token(lifted.Add(time.Minute)) != st.ID+":off" || got.Token(lifted.Add(30*24*time.Hour)) != st.ID+":off" {
		t.Fatal("off token must stay until the next on — no grace period")
	}
}

func TestLiftOnCorruptFileDeletes(t *testing.T) {
	dir := t.TempDir()
	_ = Save(dir, onState())
	_ = os.WriteFile(filepath.Join(dir, FileName), []byte("{"), 0o600)
	if err := Lift(dir, RawTokenID(dir), t0, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("corrupt file survived off")
	}
	if st, _, err := Load(dir); err != nil || st.FrozenFor("x") {
		t.Fatalf("after off: %+v %v", st, err)
	}
}

// An interrupted write never leaves a half-written state: the old file stays
// whole and no temp file is left behind.
func TestSaveInterruptedKeepsOldFile(t *testing.T) {
	dir := t.TempDir()
	old := onState()
	_ = Save(dir, old)
	before, _ := os.ReadFile(filepath.Join(dir, FileName))
	syncFile = func(*os.File) error { return errors.New("disk full") }
	defer func() { syncFile = (*os.File).Sync }()
	nw := onState()
	nw.Reason = "autre chose"
	if err := Save(dir, nw); err == nil {
		t.Fatal("Save must fail when fsync fails")
	}
	after, _ := os.ReadFile(filepath.Join(dir, FileName))
	if string(after) != string(before) {
		t.Fatal("old state not intact")
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}

func TestFrozenFor(t *testing.T) {
	st := onState()
	if !st.FrozenFor("other") || st.FrozenFor("sid-lead") {
		t.Fatal("exempt")
	}
	st.Freeze = false
	if st.FrozenFor("other") {
		t.Fatal("no --freeze, no freeze")
	}
	st.Freeze, st.On = true, false
	if st.FrozenFor("other") {
		t.Fatal("lifted")
	}
}

func TestParseUntil(t *testing.T) {
	for in, want := range map[string]time.Time{
		"2h":               t0.Add(2 * time.Hour),
		"18:30":            time.Date(2026, 10, 4, 18, 30, 0, 0, time.Local),
		"2026-10-05T09:00": time.Date(2026, 10, 5, 9, 0, 0, 0, time.Local),
	} {
		got, err := ParseUntil(in, t0)
		if err != nil || !got.Equal(want) {
			t.Fatalf("ParseUntil(%q) = %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "13:00", "-1h", "demain"} { // 13:00 est passé à 14:00
		if _, err := ParseUntil(bad, t0); err == nil {
			t.Fatalf("ParseUntil(%q) accepted", bad)
		}
	}
}

func TestValidSessionAndID(t *testing.T) {
	if ValidSession("../evil") || ValidSession("a/b") || !ValidSession("4f0e-sid") {
		t.Fatal("ValidSession")
	}
	if !strings.HasPrefix(NewID(), "mnt_") || len(NewID()) != 20 {
		t.Fatal("NewID")
	}
}

// D15 point 3 (option B): Lift records whether the lifted maintenance had a
// freeze in maintenance.lifted, read back only under the token's id.
func TestLiftRecordsFreezeForTheLiftNotice(t *testing.T) {
	lifted := t0.Add(time.Hour)
	for _, c := range []struct {
		name      string
		freeze    bool
		wantKnown bool
	}{{"with freeze", true, true}, {"without freeze", false, true}} {
		dir := t.TempDir()
		st := onState()
		st.Freeze = c.freeze
		_ = Save(dir, st)
		if err := Lift(dir, st.ID, lifted, "", "", ""); err != nil {
			t.Fatal(err)
		}
		got, ok, err := Load(dir)
		if err != nil || !ok || got.On || got.Freeze != c.freeze || got.LiftedFreezeKnown != c.wantKnown {
			t.Fatalf("%s: %+v %v %v", c.name, got, ok, err)
		}
	}
}

func TestLiftedFreezeUnknownCases(t *testing.T) {
	// No .lifted (a lift written before it existed): unknown.
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, TokenName), []byte("mnt_a off 1800000000\n"), 0o600)
	if got, _, _ := Load(dir); got.LiftedFreezeKnown {
		t.Fatalf("absent .lifted: %+v", got)
	}
	// .lifted of another id: ignored.
	_ = os.WriteFile(filepath.Join(dir, LiftedName), []byte("mnt_b nofreeze\n"), 0o600)
	if got, _, _ := Load(dir); got.LiftedFreezeKnown {
		t.Fatalf("other id: %+v", got)
	}
	// Unreadable content: ignored.
	_ = os.WriteFile(filepath.Join(dir, LiftedName), []byte("garbage\n"), 0o600)
	if got, _, _ := Load(dir); got.LiftedFreezeKnown {
		t.Fatalf("garbage: %+v", got)
	}
	// A corrupt state lifted: freeze unknown, even under a reused id.
	dir = t.TempDir()
	_ = Save(dir, onState())
	_ = os.WriteFile(filepath.Join(dir, FileName), []byte("{"), 0o600)
	if err := Lift(dir, RawTokenID(dir), t0, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := Load(dir); got.LiftedFreezeKnown {
		t.Fatalf("corrupt lifted: %+v", got)
	}
}

// A failed write of maintenance.lifted never breaks the lift itself.
func TestLiftSurvivesFailedLiftedWrite(t *testing.T) {
	dir := t.TempDir()
	st := onState()
	st.Freeze = false
	_ = Save(dir, st)
	if err := os.Mkdir(filepath.Join(dir, LiftedName), 0o700); err != nil { // rename onto a directory fails
		t.Fatal(err)
	}
	if err := Lift(dir, st.ID, t0, "", "", ""); err != nil {
		t.Fatalf("lift broken by .lifted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("state file survived")
	}
	got, ok, err := Load(dir)
	if err != nil || !ok || got.On || got.LiftedFreezeKnown {
		t.Fatalf("after a failed .lifted: %+v %v %v", got, ok, err)
	}
}

func TestSaveLoadKeepsOwner(t *testing.T) {
	dir := t.TempDir()
	st := onState()
	st.Owner, st.OwnerLabel = "sid-owner", "Alpha · pigtail (mp)"
	if err := Save(dir, st); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Load(dir)
	if err != nil || !ok || got.Owner != "sid-owner" || got.OwnerLabel != "Alpha · pigtail (mp)" {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	tok, _ := os.ReadFile(filepath.Join(dir, TokenName))
	if f := strings.Fields(string(tok)); len(f) != 3 { // the bash contract: three fields, owner never in the token
		t.Fatalf("token %q", tok)
	}
}

func TestFrozenForOwnerAndExempt(t *testing.T) {
	st := onState() // Exempt sid-lead
	st.Owner = "sid-owner"
	for sess, want := range map[string]bool{"sid-owner": false, "sid-lead": false, "other": true, "": true} {
		if got := st.FrozenFor(sess); got != want {
			t.Fatalf("FrozenFor(%q) = %v, want %v", sess, got, want)
		}
	}
	st.Owner, st.Exempt = "", "" // set by the user, nobody exempt: the empty session never matches an empty owner
	if !st.FrozenFor("") || !st.FrozenFor("x") {
		t.Fatal("human-owned freeze must freeze every caller, the header-less one included")
	}
}

func TestCanLift(t *testing.T) {
	owned := State{ID: "mnt_a", On: true, Owner: "sid-owner", Exempt: "sid-lead"}
	human := State{ID: "mnt_b", On: true}
	for _, c := range []struct {
		st   State
		sess string
		want bool
	}{
		{owned, "sid-owner", true}, {owned, "", true}, {owned, "sid-lead", false}, {owned, "other", false},
		{human, "", true}, {human, "sid-owner", false}, {human, "other", false},
	} {
		if got := c.st.CanLift(c.sess); got != c.want {
			t.Fatalf("%s CanLift(%q) = %v, want %v", c.st.ID, c.sess, got, c.want)
		}
	}
}

func TestLiftRecordsLifter(t *testing.T) {
	for _, c := range []struct {
		by, want string
		human    bool
	}{{"", "", true}, {"Alpha · pigtail (mp)", "Alpha · pigtail (mp)", false}} {
		dir := t.TempDir()
		_ = Save(dir, onState())
		byID := ""
		if c.by != "" {
			byID = "sid-alpha"
		}
		if err := Lift(dir, onState().ID, t0, byID, c.by, ""); err != nil {
			t.Fatal(err)
		}
		st, ok, err := Load(dir)
		if err != nil || !ok || st.On || st.LiftedBy != c.want || st.LiftedByUser != c.human {
			t.Fatalf("by %q: %+v %v %v", c.by, st, ok, err)
		}
	}
}

// A maintenance.json written by the previous binary has no owner: the user-owned,
// so no session can lift it (never "any session, since nobody owns it").
func TestCanLiftOldStateWithoutOwner(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, FileName), []byte(`{"id":"mnt_old","on":true,"reason":"r","freeze":true,"since":"2026-10-04T10:00:00Z"}`), 0o600)
	st, ok, err := Load(dir)
	if err != nil || !ok || st.Owner != "" || st.CanLift("11111111-1111-1111-1111-111111111111") || !st.CanLift("") {
		t.Fatalf("%+v %v %v", st, ok, err)
	}
}

// A maintenance.lifted written by the previous binary (two fields) still
// reads: freeze known, lifter unknown.
func TestLiftedOldFormatHasNoLifter(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, TokenName), []byte("mnt_a off 1800000000\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, LiftedName), []byte("mnt_a freeze\n"), 0o600)
	st, ok, err := Load(dir)
	if err != nil || !ok || !st.LiftedFreezeKnown || !st.Freeze || st.LiftedBy != "" || st.LiftedByUser {
		t.Fatalf("%+v %v %v", st, ok, err)
	}
}

// A maintenance.lifted written by an older binary carries another word than
// "user" as third field for a lift by the person: any field that is neither
// "sid …" nor "session …" reads as a lift by the user, with no session.
func TestLoadLiftedOlderBinaryWordIsUser(t *testing.T) {
	for _, c := range []struct {
		third string
		human bool
	}{{"user", true}, {"Ada", true}, {"someone else", true}, {"session Alpha", false}, {"sid sid-a - Alpha", false}} {
		dir := t.TempDir()
		_ = Save(dir, onState())
		if err := Lift(dir, onState().ID, t0, "", "", ""); err != nil {
			t.Fatal(err)
		}
		line := onState().ID + " freeze " + c.third + "\n"
		if err := os.WriteFile(filepath.Join(dir, LiftedName), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
		st, ok, err := Load(dir)
		if err != nil || !ok || st.On || st.LiftedByUser != c.human {
			t.Fatalf("third %q: %+v %v %v", c.third, st, ok, err)
		}
	}
}
