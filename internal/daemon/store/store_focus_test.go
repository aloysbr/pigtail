package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store/storetest"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

var ft0 = time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)

func fTheme(id string) pigtail.FocusMember {
	return pigtail.FocusMember{Kind: pigtail.FocusMemberTheme, ID: id}
}

func fSubject(id string) pigtail.FocusMember {
	return pigtail.FocusMember{Kind: pigtail.FocusMemberSubject, ID: id}
}

func at(d time.Duration) *time.Time { v := ft0.Add(d); return &v }

// focusFixture: theme thm_t (perso) with s1 building and s2 done; theme thm_u
// (perso, no subject); subjects s3, s4, s5 under thm_u.
func focusFixture(t *testing.T, s *Store) {
	t.Helper()
	for _, id := range []string{"thm_t", "thm_u"} {
		if err := s.CreateTheme(&pigtail.Theme{ID: id, Name: id, Kind: "project", VaultLink: "Perso/Projects/" + id,
			Scope: "perso", CreatedAt: ft0}); err != nil {
			t.Fatal(err)
		}
	}
	for _, sub := range []struct{ id, theme, status string }{
		{"s1", "thm_t", pigtail.StatusBuilding}, {"s2", "thm_t", pigtail.StatusDone},
		{"s3", "thm_u", pigtail.StatusInbox}, {"s4", "thm_u", pigtail.StatusInbox}, {"s5", "thm_u", pigtail.StatusInbox},
	} {
		if err := s.CreateSubject(&pigtail.Subject{ID: sub.id, ThemeID: sub.theme, Title: sub.id, Slug: sub.id,
			Status: sub.status, CreatedAt: ft0, UpdatedAt: ft0}); err != nil {
			t.Fatal(err)
		}
	}
}

func newFocusStore(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	focusFixture(t, s)
	return s
}

func newFocusFileStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pigtail.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	focusFixture(t, s)
	return s, path
}

func mkFocus(t *testing.T, s *Store, id, name string, endsAt *time.Time, now time.Time, members ...pigtail.FocusMember) {
	t.Helper()
	if err := s.CreateFocus(&pigtail.Focus{ID: id, Name: name, Scope: "perso", EndsAt: endsAt, Members: members}, now); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
}

// focusLines returns the focus end lines of a subject's log.
func focusLines(t *testing.T, s *Store, sub string) []pigtail.SubjectLogEntry {
	t.Helper()
	all, err := s.ListLog(sub, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var out []pigtail.SubjectLogEntry
	for _, e := range all {
		if strings.HasPrefix(e.Text, "focus ") {
			out = append(out, e)
		}
	}
	return out
}

func wantOneLine(t *testing.T, s *Store, sub string, ts time.Time, text string) {
	t.Helper()
	ls := focusLines(t, s, sub)
	if len(ls) != 1 || !ls[0].Timestamp.Equal(ts) || ls[0].Text != text || ls[0].Kind != pigtail.LogNote {
		t.Fatalf("%s lines = %+v, want one note %q at %v", sub, ls, text, ts)
	}
}

func wantNoLine(t *testing.T, s *Store, sub string) {
	t.Helper()
	if ls := focusLines(t, s, sub); len(ls) != 0 {
		t.Fatalf("%s lines = %+v, want none", sub, ls)
	}
}

// dbEnded reads ended_at straight from the base (nil = NULL).
func dbEnded(t *testing.T, s *Store, id string) *int64 {
	t.Helper()
	var v *int64
	if err := s.db.QueryRow(`SELECT ended_at FROM focus_spaces WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func inList(fs []pigtail.Focus, id string) *pigtail.Focus {
	for i := range fs {
		if fs[i].ID == id {
			return &fs[i]
		}
	}
	return nil
}

func TestCreateFocusRoundTrip(t *testing.T) {
	s := newFocusStore(t)
	mkFocus(t, s, "fcs_1", "Offsite", nil, ft0, fTheme("thm_t"), fSubject("s3"))
	f, sealErr, err := s.GetFocus("fcs_1", ft0.Add(time.Minute))
	if err != nil || sealErr != nil {
		t.Fatalf("get: %v / %v", sealErr, err)
	}
	if f.Name != "Offsite" || f.Scope != "perso" || f.EndedAt != nil || f.EndsAt != nil || !f.CreatedAt.Equal(ft0) {
		t.Fatalf("focus = %+v", f)
	}
	if len(f.Members) != 2 || f.Members[0] != (pigtail.FocusMember{Kind: "theme", ID: "thm_t", AddedAt: ft0}) ||
		f.Members[1] != (pigtail.FocusMember{Kind: "subject", ID: "s3", AddedAt: ft0}) {
		t.Fatalf("members = %+v", f.Members)
	}
	if _, _, err := s.GetFocus("fcs_nope", ft0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id = %v", err)
	}
}

func TestFocusNameUniqueWhileOpen(t *testing.T) {
	s := newFocusStore(t)
	mkFocus(t, s, "fcs_1", "Offsite", nil, ft0)
	if err := s.CreateFocus(&pigtail.Focus{ID: "fcs_2", Name: "Offsite", Scope: "perso"}, ft0); !errors.Is(err, ErrFocusNameTaken) {
		t.Fatalf("second create = %v, want ErrFocusNameTaken", err)
	}
	if _, err := s.EndFocus("fcs_1", ft0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	mkFocus(t, s, "fcs_2", "Offsite", nil, ft0.Add(2*time.Minute))
}

func TestFocusMemberInOneOpenFocus(t *testing.T) {
	s := newFocusStore(t)
	mkFocus(t, s, "fcs_1", "F1", nil, ft0, fSubject("s3"), fTheme("thm_t"))
	for _, m := range []pigtail.FocusMember{fSubject("s3"), fTheme("thm_t")} {
		err := s.CreateFocus(&pigtail.Focus{ID: "fcs_2", Name: "F2", Scope: "perso", Members: []pigtail.FocusMember{m}}, ft0)
		var taken *ErrFocusMemberTaken
		if !errors.As(err, &taken) || taken.Kind != m.Kind || taken.ID != m.ID {
			t.Fatalf("create with %v = %v, want ErrFocusMemberTaken", m, err)
		}
		if err := s.AddFocusMember("fcs_x", m, ft0); err == nil {
			t.Fatal("add to unknown focus accepted")
		}
	}
	// An expired focus no longer holds its member: F2 is accepted and the
	// expired one is sealed in the same transaction.
	mkFocus(t, s, "fcs_3", "F3", at(time.Hour), ft0, fSubject("s4"))
	mkFocus(t, s, "fcs_4", "F4", nil, ft0.Add(2*time.Hour), fSubject("s4"))
	if v := dbEnded(t, s, "fcs_3"); v == nil || *v != ft0.Add(time.Hour).UnixMilli() {
		t.Fatalf("expired focus not sealed by the create: %v", v)
	}
	wantOneLine(t, s, "s4", ft0.Add(time.Hour), `focus "F3" ended (deadline)`)
}

// Review Focus 1.
func TestFocusSealedOnFirstReadAfterDeadline(t *testing.T) {
	s := newFocusStore(t)
	mkFocus(t, s, "fcs_1", "Deep", at(time.Hour), ft0, fTheme("thm_t"), fSubject("s3"), fSubject("s1"))
	act, sealErr, err := s.ActiveFocus(ft0.Add(2 * time.Hour))
	if err != nil || sealErr != nil || inList(act, "fcs_1") != nil {
		t.Fatalf("active = %+v, %v / %v", act, sealErr, err)
	}
	all, _, err := s.ListFocus(ft0.Add(2*time.Hour), true)
	f := inList(all, "fcs_1")
	if err != nil || f == nil || f.EndedAt == nil || !f.EndedAt.Equal(ft0.Add(time.Hour)) || f.EndReason != pigtail.FocusEndDeadline {
		t.Fatalf("list all = %+v, %v", all, err)
	}
	wantOneLine(t, s, "s1", ft0.Add(time.Hour), `focus "Deep" ended (deadline)`)
	wantOneLine(t, s, "s3", ft0.Add(time.Hour), `focus "Deep" ended (deadline)`)
	wantNoLine(t, s, "s2")
	if _, _, err := s.ActiveFocus(ft0.Add(3 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	wantOneLine(t, s, "s1", ft0.Add(time.Hour), `focus "Deep" ended (deadline)`)
	wantOneLine(t, s, "s3", ft0.Add(time.Hour), `focus "Deep" ended (deadline)`)
}

func TestEndFocusManualLogsOnce(t *testing.T) {
	s := newFocusStore(t)
	mkFocus(t, s, "fcs_1", "Deep", nil, ft0, fSubject("s3"))
	now := ft0.Add(30 * time.Minute)
	f, err := s.EndFocus("fcs_1", now)
	if err != nil || f.EndReason != pigtail.FocusEndManual || f.EndedAt == nil || !f.EndedAt.Equal(now) {
		t.Fatalf("end = %+v, %v", f, err)
	}
	wantOneLine(t, s, "s3", now, `focus "Deep" ended (by hand)`)
	if _, err := s.EndFocus("fcs_1", now.Add(time.Minute)); !errors.Is(err, ErrFocusEnded) {
		t.Fatalf("second end = %v, want ErrFocusEnded", err)
	}
	wantOneLine(t, s, "s3", now, `focus "Deep" ended (by hand)`)
}

func TestActiveFocusJudgedAtReadEvenIfSealFails(t *testing.T) {
	s, path := newFocusFileStore(t)
	mkFocus(t, s, "fcs_1", "Deep", at(time.Hour), ft0, fSubject("s3"))
	restore := storetest.FailFocusSeal(t, path)
	now := ft0.Add(2 * time.Hour)
	act, sealErr, err := s.ActiveFocus(now)
	if sealErr == nil || err != nil || inList(act, "fcs_1") != nil {
		t.Fatalf("active = %+v, sealErr=%v err=%v", act, sealErr, err)
	}
	f, sealErr, err := s.GetFocus("fcs_1", now)
	if sealErr == nil || err != nil || f.EndedAt == nil || !f.EndedAt.Equal(ft0.Add(time.Hour)) || f.EndReason != pigtail.FocusEndDeadline {
		t.Fatalf("get = %+v, sealErr=%v err=%v", f, sealErr, err)
	}
	if _, err := s.EndFocus("fcs_1", now); err == nil {
		t.Fatal("end succeeded while sealing fails")
	}
	wantNoLine(t, s, "s3")
	restore()
	if _, sealErr, err := s.ActiveFocus(now.Add(time.Minute)); sealErr != nil || err != nil {
		t.Fatalf("after restore: %v / %v", sealErr, err)
	}
	if v := dbEnded(t, s, "fcs_1"); v == nil || *v != ft0.Add(time.Hour).UnixMilli() {
		t.Fatalf("ended_at = %v", v)
	}
	wantOneLine(t, s, "s3", ft0.Add(time.Hour), `focus "Deep" ended (deadline)`)
}

// Focus D26.
func TestFocusSealHeldDuringFreeze(t *testing.T) {
	s := newFocusStore(t)
	mkFocus(t, s, "fcs_1", "Deep", at(time.Hour), ft0, fSubject("s1"), fSubject("s3"))
	held := true
	s.SetFocusSealGate(func() bool { return held })
	now := ft0.Add(2 * time.Hour)
	act, sealErr, err := s.ActiveFocus(now)
	if err != nil || sealErr != nil || inList(act, "fcs_1") != nil {
		t.Fatalf("active = %+v, %v / %v", act, sealErr, err)
	}
	f, _, err := s.GetFocus("fcs_1", now)
	if err != nil || f.EndedAt == nil || !f.EndedAt.Equal(ft0.Add(time.Hour)) || f.EndReason != pigtail.FocusEndDeadline {
		t.Fatalf("get = %+v, %v", f, err)
	}
	all, _, err := s.ListFocus(now, true)
	if lf := inList(all, "fcs_1"); err != nil || lf == nil || lf.EndedAt == nil || lf.EndReason != pigtail.FocusEndDeadline {
		t.Fatalf("list all = %+v, %v", all, err)
	}
	if v := dbEnded(t, s, "fcs_1"); v != nil {
		t.Fatalf("ended_at written during the freeze: %v", *v)
	}
	wantNoLine(t, s, "s1")
	wantNoLine(t, s, "s3")
	held = false
	if _, sealErr, err := s.ActiveFocus(ft0.Add(3 * time.Hour)); sealErr != nil || err != nil {
		t.Fatalf("after lift: %v / %v", sealErr, err)
	}
	if v := dbEnded(t, s, "fcs_1"); v == nil || *v != ft0.Add(time.Hour).UnixMilli() {
		t.Fatalf("ended_at after lift = %v", v)
	}
	wantOneLine(t, s, "s1", ft0.Add(time.Hour), `focus "Deep" ended (deadline)`)
	wantOneLine(t, s, "s3", ft0.Add(time.Hour), `focus "Deep" ended (deadline)`)
	if _, _, err := s.ActiveFocus(ft0.Add(4 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	wantOneLine(t, s, "s1", ft0.Add(time.Hour), `focus "Deep" ended (deadline)`)
}

// Focus D26, round 6: a write that passes the freeze seals nothing but its own.
func TestFocusExemptWriteUnderFreezeSealsNothingElse(t *testing.T) {
	s := newFocusStore(t)
	mkFocus(t, s, "fcs_a", "A", nil, ft0, fSubject("s1"))
	mkFocus(t, s, "fcs_b", "B", at(time.Hour), ft0, fSubject("s3"))
	held := true
	s.SetFocusSealGate(func() bool { return held })
	now := ft0.Add(2 * time.Hour)
	a, err := s.EndFocus("fcs_a", now)
	if err != nil || a.EndReason != pigtail.FocusEndManual {
		t.Fatalf("end A = %+v, %v", a, err)
	}
	wantOneLine(t, s, "s1", now, `focus "A" ended (by hand)`)
	if v := dbEnded(t, s, "fcs_b"); v != nil {
		t.Fatalf("B sealed by A's end during the freeze: %v", *v)
	}
	wantNoLine(t, s, "s3")
	if _, err := s.EndFocus("fcs_b", now); !errors.Is(err, ErrFocusEnded) {
		t.Fatalf("end B = %v", err)
	}
	if err := s.AddFocusMember("fcs_b", fSubject("s4"), now); !errors.Is(err, ErrFocusEnded) {
		t.Fatalf("add to B = %v", err)
	}
	if err := s.RemoveFocusMember("fcs_b", pigtail.FocusMemberSubject, "s3", now); !errors.Is(err, ErrFocusEnded) {
		t.Fatalf("rm from B = %v", err)
	}
	mkFocus(t, s, "fcs_2", "F2", nil, now, fSubject("s3"))
	if v := dbEnded(t, s, "fcs_b"); v != nil {
		t.Fatalf("B sealed by a create during the freeze: %v", *v)
	}
	wantNoLine(t, s, "s3")
	held = false
	if _, sealErr, err := s.ActiveFocus(ft0.Add(3 * time.Hour)); sealErr != nil || err != nil {
		t.Fatalf("after lift: %v / %v", sealErr, err)
	}
	if v := dbEnded(t, s, "fcs_b"); v == nil || *v != ft0.Add(time.Hour).UnixMilli() {
		t.Fatalf("B ended_at after lift = %v", v)
	}
	wantOneLine(t, s, "s3", ft0.Add(time.Hour), `focus "B" ended (deadline)`)
	if _, _, err := s.ActiveFocus(ft0.Add(4 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	wantOneLine(t, s, "s3", ft0.Add(time.Hour), `focus "B" ended (deadline)`)
}

// Focus D27.
func TestCreateSameNameUnderFreezeRefused(t *testing.T) {
	s := newFocusStore(t)
	mkFocus(t, s, "fcs_b", "Zqx", at(time.Hour), ft0, fSubject("s3"))
	held := true
	s.SetFocusSealGate(func() bool { return held })
	err := s.CreateFocus(&pigtail.Focus{ID: "fcs_2", Name: "Zqx", Scope: "perso", Members: []pigtail.FocusMember{fSubject("s5")}}, ft0.Add(2*time.Hour))
	if !errors.Is(err, ErrFocusNameTaken) {
		t.Fatalf("create same name under freeze = %v, want ErrFocusNameTaken", err)
	}
	if v := dbEnded(t, s, "fcs_b"); v != nil {
		t.Fatalf("B sealed during the freeze: %v", *v)
	}
	wantNoLine(t, s, "s3")
	wantNoLine(t, s, "s5")
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM focus_spaces WHERE name = 'Zqx'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("focus named Zqx: %d, %v", n, err)
	}
	held = false
	if _, sealErr, err := s.ActiveFocus(ft0.Add(3 * time.Hour)); sealErr != nil || err != nil {
		t.Fatalf("after lift: %v / %v", sealErr, err)
	}
	if v := dbEnded(t, s, "fcs_b"); v == nil || *v != ft0.Add(time.Hour).UnixMilli() {
		t.Fatalf("B ended_at after lift = %v", v)
	}
	wantOneLine(t, s, "s3", ft0.Add(time.Hour), `focus "Zqx" ended (deadline)`)
	mkFocus(t, s, "fcs_2", "Zqx", nil, ft0.Add(3*time.Hour), fSubject("s5"))
	if _, _, err := s.ActiveFocus(ft0.Add(3 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	wantOneLine(t, s, "s3", ft0.Add(time.Hour), `focus "Zqx" ended (deadline)`)
}

func TestRemoveFocusMember(t *testing.T) {
	s := newFocusStore(t)
	mkFocus(t, s, "fcs_1", "Deep", nil, ft0, fSubject("s3"), fSubject("s4"))
	if err := s.RemoveFocusMember("fcs_1", pigtail.FocusMemberSubject, "s5", ft0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rm absent = %v", err)
	}
	if err := s.RemoveFocusMember("fcs_1", pigtail.FocusMemberSubject, "s4", ft0); err != nil {
		t.Fatal(err)
	}
	f, _, _ := s.GetFocus("fcs_1", ft0)
	if len(f.Members) != 1 || f.Members[0].ID != "s3" {
		t.Fatalf("members = %+v", f.Members)
	}
	if _, err := s.EndFocus("fcs_1", ft0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveFocusMember("fcs_1", pigtail.FocusMemberSubject, "s3", ft0.Add(2*time.Minute)); !errors.Is(err, ErrFocusEnded) {
		t.Fatalf("rm on an ended focus = %v", err)
	}
}

func TestFocusMemberMissingAfterDelete(t *testing.T) {
	s := newFocusStore(t)
	mkFocus(t, s, "fcs_1", "Deep", nil, ft0, fTheme("thm_u"), fSubject("s1"))
	// thm_u has subjects in the fixture; DeleteTheme does not cascade, so the
	// theme row goes alone, as the route allows once the theme is empty.
	if err := s.DeleteTheme("thm_u"); err != nil {
		t.Fatal(err)
	}
	f, _, err := s.GetFocus("fcs_1", ft0)
	if err != nil || len(f.Members) != 2 || !f.Members[0].Missing || f.Members[1].Missing {
		t.Fatalf("get members = %+v, %v", f, err)
	}
	all, _, err := s.ListFocus(ft0, false)
	lf := inList(all, "fcs_1")
	if err != nil || lf == nil || !lf.Members[0].Missing || lf.Members[1].Missing {
		t.Fatalf("list = %+v, %v", all, err)
	}
}

// A theme member's end lines skip every closed subject (done, parked,
// dropped, captured), not only done and captured.
func TestFocusEndLinesSkipClosedSubjects(t *testing.T) {
	s := newFocusStore(t)
	for _, sub := range []struct{ id, status string }{
		{"s6", pigtail.StatusDropped}, {"s7", pigtail.StatusParked}, {"s8", pigtail.StatusCaptured},
	} {
		if err := s.CreateSubject(&pigtail.Subject{ID: sub.id, ThemeID: "thm_t", Title: sub.id, Slug: sub.id,
			Status: sub.status, CreatedAt: ft0, UpdatedAt: ft0}); err != nil {
			t.Fatal(err)
		}
	}
	mkFocus(t, s, "fcs_1", "Deep", nil, ft0, fTheme("thm_t"))
	if _, err := s.EndFocus("fcs_1", ft0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	wantOneLine(t, s, "s1", ft0.Add(time.Minute), `focus "Deep" ended (by hand)`)
	for _, id := range []string{"s2", "s6", "s7", "s8"} {
		wantNoLine(t, s, id)
	}
}
