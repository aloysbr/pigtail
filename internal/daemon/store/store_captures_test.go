package store

import (
	"errors"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func mkCapture(id, theme, text string) *pigtail.Capture {
	return &pigtail.Capture{ID: id, ThemeID: theme, Text: text, CreatedAt: time.UnixMilli(10).UTC()}
}

func logTexts(t *testing.T, s *Store, subjectID string) []string {
	t.Helper()
	logs, err := s.ListLog(subjectID, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range logs {
		out = append(out, l.Kind+":"+l.Text)
	}
	return out
}

func TestCaptureAdd(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)
	c := mkCapture("cap_1", themeID, "an idea")
	if err := s.AddCapture(c, "log_1"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCapture("cap_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "an idea" || got.ThemeID != themeID || got.Scope != "globex" || got.ClosedAt != nil || got.FromSubjectID != "" {
		t.Fatalf("capture = %+v", got)
	}
	open, err := s.ListCaptures(CaptureFilter{ThemeID: themeID, OpenOnly: true, Scope: "globex"})
	if err != nil || len(open) != 1 {
		t.Fatalf("open captures = %v, %v", open, err)
	}
}

// A failing log write rolls the whole capture back: no capture row, status kept.
func TestCaptureAddFromSubjectAtomic(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)
	if err := s.CreateSubject(mkSubject("sub_a", themeID, pigtail.StatusInbox, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE subject_log`); err != nil {
		t.Fatal(err)
	}
	c := mkCapture("cap_1", themeID, "replaces a")
	c.FromSubjectID = "sub_a"
	if err := s.AddCapture(c, "log_1"); err == nil {
		t.Fatal("AddCapture must fail when the log write fails")
	}
	if _, err := s.GetCapture("cap_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("capture left behind: %v", err)
	}
	if sub, _ := s.GetSubject("sub_a"); sub.Status != pigtail.StatusInbox {
		t.Fatalf("status = %q, want inbox (rolled back)", sub.Status)
	}
}

func TestCaptureFromOtherTheme(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)
	if err := s.CreateTheme(&pigtail.Theme{ID: "thm_o", Name: "O", Kind: "project", VaultLink: "Globex/Projects/o", CreatedAt: time.UnixMilli(1).UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSubject(mkSubject("sub_a", themeID, pigtail.StatusInbox, 1)); err != nil {
		t.Fatal(err)
	}
	c := mkCapture("cap_1", "thm_o", "x")
	c.FromSubjectID = "sub_a"
	if err := s.AddCapture(c, "log_1"); !errors.Is(err, ErrCaptureOtherTheme) {
		t.Fatalf("err = %v, want ErrCaptureOtherTheme", err)
	}
}

func TestCapturePromote(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)
	for _, id := range []string{"cap_1", "cap_2"} {
		if err := s.AddCapture(mkCapture(id, themeID, id), "log_"+id); err != nil {
			t.Fatal(err)
		}
	}
	sub := &pigtail.Subject{ID: "sub_new", ThemeID: themeID, Title: "Both", Slug: "both", Status: pigtail.StatusInbox, Source: pigtail.SourceLocal,
		CreatedAt: time.UnixMilli(20).UTC(), UpdatedAt: time.UnixMilli(20).UTC()}
	caps, err := s.PromoteCaptures([]string{"cap_1", "cap_2"}, sub)
	if err != nil || len(caps) != 2 {
		t.Fatalf("promote = %v, %v", caps, err)
	}
	for _, c := range caps {
		if c.PromotedSubjectID != "sub_new" || c.ClosedAt == nil {
			t.Fatalf("capture not closed: %+v", c)
		}
	}
	if got, err := s.GetSubject("sub_new"); err != nil || got.Status != pigtail.StatusInbox {
		t.Fatalf("subject = %+v, %v", got, err)
	}
	if _, err := s.PromoteCaptures([]string{"cap_1"}, &pigtail.Subject{ID: "sub_x", ThemeID: themeID, Title: "x", Slug: "x", Status: pigtail.StatusInbox}); !errors.Is(err, ErrCaptureClosed) {
		t.Fatalf("re-promote err = %v, want ErrCaptureClosed", err)
	}
}

func TestCapturePromoteMixedThemes(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)
	if err := s.CreateTheme(&pigtail.Theme{ID: "thm_o", Name: "O", Kind: "project", VaultLink: "Globex/Projects/o", CreatedAt: time.UnixMilli(1).UTC()}); err != nil {
		t.Fatal(err)
	}
	_ = s.AddCapture(mkCapture("cap_1", themeID, "a"), "l1")
	_ = s.AddCapture(mkCapture("cap_2", "thm_o", "b"), "l2")
	sub := &pigtail.Subject{ID: "sub_new", ThemeID: themeID, Title: "Mixed", Slug: "mixed", Status: pigtail.StatusInbox, Source: pigtail.SourceLocal}
	if _, err := s.PromoteCaptures([]string{"cap_1", "cap_2"}, sub); !errors.Is(err, ErrCapturesMixedThemes) || errors.Is(err, ErrCaptureOtherTheme) {
		t.Fatalf("err = %v, want ErrCapturesMixedThemes", err)
	}
	if _, err := s.GetSubject("sub_new"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused promote created a subject: %v", err)
	}
}

func TestCaptureDeleteRestoresStatus(t *testing.T) {
	for _, prev := range []string{pigtail.StatusInbox, pigtail.StatusParked} {
		s := newTestStore(t)
		themeID := seedTheme(t, s)
		if err := s.CreateSubject(mkSubject("sub_a", themeID, prev, 1)); err != nil {
			t.Fatal(err)
		}
		c := mkCapture("cap_1", themeID, "x")
		c.FromSubjectID = "sub_a"
		if err := s.AddCapture(c, "log_1"); err != nil {
			t.Fatal(err)
		}
		if sub, _ := s.GetSubject("sub_a"); sub.Status != pigtail.StatusCaptured {
			t.Fatalf("after add status = %q, want captured", sub.Status)
		}
		_, sub, err := s.DeleteCapture("cap_1", "log_2")
		if err != nil || sub == nil || sub.Status != prev {
			t.Fatalf("delete = %+v, %v; want subject restored to %s", sub, err, prev)
		}
		if got, _ := s.GetSubject("sub_a"); got.Status != prev {
			t.Fatalf("status = %q, want %q", got.Status, prev)
		}
		logs := logTexts(t, s, "sub_a")
		want := []string{"status:captured → " + prev + " · restored", "status:" + prev + " → captured · cap_1"}
		if len(logs) != 2 || logs[0] != want[0] || logs[1] != want[1] {
			t.Fatalf("log = %q, want %q (newest first)", logs, want)
		}
		if _, err := s.GetCapture("cap_1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("capture still there: %v", err)
		}
	}
}

func TestCaptureDeletePromotedRefused(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)
	_ = s.AddCapture(mkCapture("cap_1", themeID, "a"), "l1")
	sub := &pigtail.Subject{ID: "sub_new", ThemeID: themeID, Title: "N", Slug: "n", Status: pigtail.StatusInbox, Source: pigtail.SourceLocal, CreatedAt: time.UnixMilli(5).UTC(), UpdatedAt: time.UnixMilli(5).UTC()}
	if _, err := s.PromoteCaptures([]string{"cap_1"}, sub); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DeleteCapture("cap_1", "l2"); !errors.Is(err, ErrCapturePromoted) {
		t.Fatalf("err = %v, want ErrCapturePromoted", err)
	}
	if _, err := s.GetCapture("cap_1"); err != nil {
		t.Fatalf("promoted capture vanished: %v", err)
	}
}

func TestCaptureDeleteSubjectChanged(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)
	_ = s.CreateSubject(mkSubject("sub_a", themeID, pigtail.StatusInbox, 1))
	c := mkCapture("cap_1", themeID, "x")
	c.FromSubjectID = "sub_a"
	if err := s.AddCapture(c, "log_1"); err != nil {
		t.Fatal(err)
	}
	sub, _ := s.GetSubject("sub_a")
	sub.Status = pigtail.StatusBuilding // modified since the capture
	if err := s.UpdateSubject(sub); err != nil {
		t.Fatal(err)
	}
	_, restored, err := s.DeleteCapture("cap_1", "log_2")
	if err != nil || restored != nil {
		t.Fatalf("delete = %+v, %v; want capture deleted, subject untouched", restored, err)
	}
	if got, _ := s.GetSubject("sub_a"); got.Status != pigtail.StatusBuilding {
		t.Fatalf("status = %q, want building (untouched)", got.Status)
	}
}

func TestCountOpenCapturesByTheme(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)
	_ = s.AddCapture(mkCapture("cap_1", themeID, "a"), "l1")
	if n, err := s.CountOpenCapturesByTheme(themeID); err != nil || n != 1 {
		t.Fatalf("n = %d, %v", n, err)
	}
}
