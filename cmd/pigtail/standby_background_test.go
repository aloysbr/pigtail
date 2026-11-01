package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/testutil"
)

// freeBgDeps makes every /standby background reading answer "free" (no cron,
// no pending task, no shell, a quiet screen) for the caller pid 4242 and the
// previous turn p0, and restores the real readings after the test. The test
// edits the returned deps to turn one reading busy or unreadable.
func freeBgDeps(t *testing.T) *standbyBgDeps {
	t.Helper()
	testutil.SerialOnly(t) // swaps standbyBgDepsFor
	d := &standbyBgDeps{
		Caller:     func() (int, bool, error) { return 4242, true, nil },
		Native:     func(int) (claudesession.Native, bool) { return claudesession.Native{StartedAt: 1}, true },
		Transcript: func(string) (string, bool, error) { return "/t/s.jsonl", true, nil },
		PrevTurn:   func(string) (string, bool) { return "p0", true },
		Record: func(string, int, string) (bgRecord, error) {
			return bgRecord{PID: 4242, Turn: "p0", Crons: &[]bgCron{}}, nil
		},
		Pending:   func(string, time.Time) ([]string, error) { return nil, nil },
		Ancestors: func() ([]int, error) { return []int{77, 4242}, nil },
		Shells:    func(int) ([]int, error) { return nil, nil },
		Screen:    func() (string, error) { return "", nil },
	}
	saved := standbyBgDepsFor
	standbyBgDepsFor = func(func(string) string) standbyBgDeps { return *d }
	t.Cleanup(func() { standbyBgDepsFor = saved })
	return d
}

func writeBgRecordFile(t *testing.T, dir, session, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, session), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// readBgRecord trusts a record only for this process and the turn just
// before /standby; every other state is a named D33 reason, never "free".
func TestReadBgRecord(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := readBgRecord(dir, "s1", 4242, "p0"); !errors.Is(err, errNoBgRecord) {
		t.Fatalf("absent: %v", err)
	}
	writeBgRecordFile(t, dir, "s1", `{"session_id":"s1","pid":42`)
	if _, err := readBgRecord(dir, "s1", 4242, "p0"); !errors.Is(err, errBgUnreadable) {
		t.Fatalf("cut JSON: %v", err)
	}
	cases := []struct {
		name, body string
		want       error
	}{
		{"other pid", `{"session_id":"s1","pid":1,"turn":"p0","session_crons":[]}`, errStaleBgRecord},
		{"other turn", `{"session_id":"s1","pid":4242,"turn":"p9","session_crons":[]}`, errStaleBgRecord},
		{"pid 0", `{"session_id":"s1","pid":0,"turn":"p0","session_crons":[]}`, errStaleBgRecord},
		{"empty turn", `{"session_id":"s1","pid":4242,"turn":"","session_crons":[]}`, errBgTurnUnknown},
		{"no list", `{"session_id":"s1","pid":4242,"turn":"p0"}`, errNoBgLists},
		{"null list", `{"session_id":"s1","pid":4242,"turn":"p0","session_crons":null}`, errNoBgLists},
	}
	for _, c := range cases {
		writeBgRecordFile(t, dir, "s1", c.body)
		if _, err := readBgRecord(dir, "s1", 4242, "p0"); !errors.Is(err, c.want) {
			t.Fatalf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	// D-B: a Stop that could not read its turn says why, never "another turn".
	writeBgRecordFile(t, dir, "s1", `{"session_id":"s1","pid":4242,"turn":"","turn_error":"transcript introuvable","session_crons":[]}`)
	if _, err := readBgRecord(dir, "s1", 4242, "p0"); !errors.Is(err, errBgTurnUnknown) || !strings.Contains(err.Error(), "transcript introuvable") || strings.Contains(err.Error(), "autre tour") {
		t.Fatalf("turn unknown: %v", err)
	}
	writeBgRecordFile(t, dir, "s1", `{"session_id":"s1","pid":4242,"turn":"p0","session_crons":[]}`)
	if r, err := readBgRecord(dir, "s1", 4242, "p0"); err != nil || r.Crons == nil || len(*r.Crons) != 0 {
		t.Fatalf("empty list: %+v %v", r, err)
	}
	writeBgRecordFile(t, dir, "s1", `{"session_id":"s1","pid":4242,"turn":"p0","session_crons":[{"id":"bcef88fa","schedule":"*/1 * * * *","recurring":true}]}`)
	if r, err := readBgRecord(dir, "s1", 4242, "p0"); err != nil || r.Crons == nil || len(*r.Crons) != 1 || (*r.Crons)[0].ID != "bcef88fa" {
		t.Fatalf("one cron: %+v %v", r, err)
	}
	// A session id that climbs out of the directory reads nothing.
	writeBgRecordFile(t, filepath.Dir(dir), "x", `{"session_id":"x","pid":4242,"turn":"p0","session_crons":[]}`)
	if _, err := readBgRecord(dir, "../x", 4242, "p0"); err == nil {
		t.Fatal("../x read a file outside the directory")
	}
}

// A cron line shows no control character and no quote of the harness text.
func TestBgCronSanitised(t *testing.T) {
	t.Parallel()
	got := bgCron{ID: "ab\x1b[31mcd\n\"", Schedule: "30 15\x07 * * \"*\""}.String()
	if got != "cron ab[31mcd (30 15 * * *)" {
		t.Fatalf("cron line = %q", got)
	}
	if strings.ContainsAny(got, "\x1b\n\x07\"") {
		t.Fatalf("control or quote left: %q", got)
	}
}

// The writer keeps the crons only (D44), with no prompt, and writes the record
// atomically under a valid key only.
func TestRecordStopBackgroundShape(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "bgwork")
	raw := []byte(`{"session_id":"s1","background_tasks":[{"id":"m1","type":"monitor","status":"running"}],"session_crons":[{"id":"c1","schedule":"* * * * *","recurring":true,"prompt":"PRIVATE"}]}`)
	if err := recordStopBackground(dir, raw, "s1", 4242, "p0", nil, time.UnixMilli(5)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "s1"))
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); strings.Contains(s, "PRIVATE") || strings.Contains(s, "background_tasks") || strings.Contains(s, "m1") || !strings.Contains(s, `"c1"`) {
		t.Fatalf("record = %s", s)
	}
	if st, _ := os.Stat(filepath.Join(dir, "s1")); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", st.Mode())
	}
	if s := string(b); strings.Contains(s, "turn_error") {
		t.Fatalf("turn read, yet an error recorded: %s", s)
	}
	// D-B: a turn the Stop could not read is recorded with its reason.
	if err := recordStopBackground(dir, raw, "s1", 4242, "", errors.New("aucune entrée user"), time.UnixMilli(6)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "s1")); !strings.Contains(string(b), `"turn_error":"aucune entrée user"`) {
		t.Fatalf("record without its turn error: %s", b)
	}
	if err := recordStopBackground(dir, raw, "../x", 4242, "p0", nil, time.Now()); err == nil {
		t.Fatal("../x accepted")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "x")); err == nil {
		t.Fatal("../x wrote outside the directory")
	}
}
