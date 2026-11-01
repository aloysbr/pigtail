package gates

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
)

// Shape copied from `gate list --json` (the gate tool, 2026-10-08) plus fired_at.
const fixture = `[
 {"id":"old-fired","title":"Old","check":"x","source":"s","action":"do the old thing","subject":null,
  "scope":"gx","theme":"thm_t","not_before":null,"expires":null,"ts":"2026-10-01T09:00:00+02:00",
  "added":"2026-10-01T09:00:00+02:00","status":"fired","errors":0,"value":"v=1","broken":false,"error_since":null,
  "last_check":"2026-10-03T09:00:00+02:00","last_run":"2026-10-08T08:00:00+02:00",
  "subject_id":"sub_x000000000000034","fired_at":"2026-10-03T09:00:01+02:00"},
 {"id":"new-fired","title":"New","action":"do the new thing","scope":"perso","added":"2026-10-05T09:00:00+02:00",
  "status":"fired","errors":0,"value":"v=2","last_check":"2026-10-07T09:00:00+02:00","last_run":"2026-10-08T08:00:00+02:00",
  "subject_id":"sub_x000000000000038","fired_at":"2026-10-07T09:00:01+02:00"},
 {"id":"firing","title":"Interrupted","action":"a","scope":"perso","added":"2026-10-06T09:00:00+02:00",
  "status":"firing","errors":0,"value":"v=3","last_check":null,"last_run":"2026-10-08T08:00:00+02:00","subject_id":null,"fired_at":"x"},
 {"id":"waiting","title":"Waiting","action":"a","scope":"perso","added":"2026-10-02T09:00:00+02:00",
  "status":"open","errors":80,"broken":true,"error_since":"2026-10-05T09:00:00+02:00","value":"plans=3/5",
  "last_check":"2026-10-06T08:00:00+02:00","last_run":"2026-10-08T08:00:00+02:00","subject_id":null,"fired_at":null},
 {"id":"gone","title":"Closed","action":"a","scope":"perso","added":"2026-10-01T09:00:00+02:00","status":"closed","errors":0}
]`

var now = time.Date(2026, 10, 8, 9, 0, 0, 0, time.FixedZone("CEST", 2*3600))

func all(scope.Scope) bool { return true }

func TestBuildSortsCountsAndAges(t *testing.T) {
	gs, gerr := Parse([]byte(fixture))
	if gerr != nil {
		t.Fatal(gerr)
	}
	r := Build(gs, all, true, now)
	if !r.Configured || !r.Complete || r.Error != nil || r.Counts == nil {
		t.Fatalf("header = %+v", r)
	}
	if r.Counts.Fired != 3 || r.Counts.Open != 1 || r.Counts.Broken != 1 {
		t.Fatalf("counts = %+v, want fired 3 open 1 broken 1 (closed excluded)", *r.Counts)
	}
	var ids []string
	for _, g := range r.Fired {
		ids = append(ids, g.ID)
	}
	if strings.Join(ids, ",") != "old-fired,new-fired,firing" {
		t.Fatalf("fired order = %v, want oldest fire first, unknown fired_at last", ids)
	}
	if a := r.Fired[0].FiredAgeSeconds; a == nil || *a != int((5*24*time.Hour-time.Second).Seconds()) {
		t.Fatalf("old-fired age = %v", a)
	}
	if r.Fired[2].FiredAt != nil || r.Fired[2].FiredAgeSeconds != nil {
		t.Fatalf("unparseable fired_at must read as unknown, got %+v", r.Fired[2])
	}
	if r.Fired[0].Scope != "globex" {
		t.Fatalf("scope = %q, want the canonical globex", r.Fired[0].Scope)
	}
	if r.LastCheckAgeSeconds == nil || *r.LastCheckAgeSeconds != 3600 {
		t.Fatalf("last_check_age = %v, want 3600 (last_run), not the latest gate last_check (2 days old)", r.LastCheckAgeSeconds)
	}
}

// "Contrôles arrêtés" must read the pass of `gate check`, never the gates' own
// last_check: no gate due = no last_check moving, while the check runs fine.
func TestLastCheckIsTheLastRunNotAGateCheck(t *testing.T) {
	old := `[{"id":"w","title":"W","action":"a","scope":"perso","added":"2026-09-01T09:00:00+02:00","status":"open",
	  "errors":0,"last_check":"2026-09-02T09:00:00+02:00","last_run":"2026-10-08T08:50:00+02:00","broken":false}]`
	gs, gerr := Parse([]byte(old))
	if gerr != nil {
		t.Fatal(gerr)
	}
	r := Build(gs, all, true, now)
	if r.LastCheckAgeSeconds == nil || *r.LastCheckAgeSeconds != 600 {
		t.Fatalf("age = %v, want 600: the last pass was 10 min ago although the gate's own check is a month old", r.LastCheckAgeSeconds)
	}
	noRun := strings.ReplaceAll(old, `"last_run":"2026-10-08T08:50:00+02:00",`, "")
	gs, _ = Parse([]byte(noRun))
	if r := Build(gs, all, true, now); r.LastCheckAt != nil || r.LastCheckAgeSeconds != nil {
		t.Fatalf("no last_run (older bin/gate): the age is unknown, never derived from last_check: %+v", r)
	}
}

func TestBuildFiltersByScope(t *testing.T) {
	gs, _ := Parse([]byte(fixture))
	globex := scope.Canonical("gx")
	r := Build(gs, func(s scope.Scope) bool { return s == globex }, false, now)
	if r.Complete || r.Counts.Fired != 1 || r.Counts.Open != 0 || len(r.Fired) != 1 || r.Fired[0].ID != "old-fired" {
		t.Fatalf("globex view = %+v", r)
	}
}

func TestParseRefusesABrokenContract(t *testing.T) {
	for name, out := range map[string]string{
		"not json":       "Traceback (most recent call last):",
		"null":           "null",
		"missing scope":  `[{"id":"g","title":"T","action":"a","status":"open","added":"2026-10-01T09:00:00+02:00"}]`,
		"missing id":     `[{"title":"T","action":"a","scope":"perso","status":"open","added":"2026-10-01T09:00:00+02:00"}]`,
		"bad added":      `[{"id":"g","title":"T","action":"a","scope":"perso","status":"open","added":"yesterday"}]`,
	} {
		if _, gerr := Parse([]byte(out)); gerr == nil || gerr.Kind != ErrInvalidOutput {
			t.Errorf("%s: err = %+v, want invalid_output", name, gerr)
		}
	}
}

// One gate written without a title or an action must not turn the whole list
// into invalid_output: only the keys the daemon needs to place a gate are required.
func TestParseAcceptsEmptyTitleAndAction(t *testing.T) {
	out := `[{"id":"g","title":"","scope":"perso","status":"open","added":"2026-10-01T09:00:00+02:00"},
	         {"id":"h","title":"T","action":"","scope":"perso","status":"fired","added":"2026-10-01T09:00:00+02:00"}]`
	gs, gerr := Parse([]byte(out))
	if gerr != nil {
		t.Fatalf("err = %+v, want the two gates", gerr)
	}
	if len(gs) != 2 || gs[0].Title != "" || gs[1].Action != "" {
		t.Fatalf("gates = %+v", gs)
	}
}

// The timeout is hard: a gate command that leaves a child holding stdout open
// must not keep Output() waiting past the deadline (cmd.WaitDelay).
func TestExecRunnerTimeoutIsHard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := ExecRunner(ctx, "sh", "-c", "sleep 6 & sleep 6")
	if err == nil {
		t.Fatal("want an error from a killed command")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("ExecRunner returned after %v: a child holding stdout outlived the timeout", d)
	}
}

func TestReadClassifiesFailures(t *testing.T) {
	dir := t.TempDir()
	script := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ok := script("ok", "cat <<'EOF'\n"+fixture+"\nEOF")
	fail := script("fail", "echo 'gate: store unreadable' >&2; exit 2")
	// The real processes (an *exec.ExitError, a missing file, a missing PATH
	// entry) never have to expire: a bound far above a loaded machine's
	// shell start keeps them out of the timeout's way.
	const wide = 60 * time.Second
	cases := []struct {
		cmd  string
		kind string
	}{
		{ok, ""},
		{filepath.Join(dir, "absent"), ErrNotFound},
		{"gate-not-on-path-zqx", ErrNotFound},
		{fail, ErrFailed},
	}
	for _, c := range cases {
		gs, gerr := Source{Cmd: c.cmd, Timeout: wide}.Read(context.Background())
		switch {
		case c.kind == "" && (gerr != nil || len(gs) != 4):
			t.Errorf("%s: gates=%d err=%+v, want 4 gates", c.cmd, len(gs), gerr)
		case c.kind != "" && (gerr == nil || gerr.Kind != c.kind):
			t.Errorf("%s: err=%+v, want %s", c.cmd, gerr, c.kind)
		}
	}
	_, gerr := Source{Cmd: fail, Timeout: wide}.Read(context.Background())
	if gerr == nil || !strings.Contains(gerr.Message, "store unreadable") {
		t.Errorf("failed: message %+v must carry stderr's last line", gerr)
	}

	// The one case that must expire runs in a synctest bubble: a runner that
	// hangs until its context ends, the 300 ms bound on virtual time — the
	// machine's load cannot move it. ExecRunner's own hard timeout on a real
	// process is TestExecRunnerTimeoutIsHard's.
	synctest.Test(t, func(t *testing.T) {
		hang := func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		start := time.Now()
		_, gerr := Source{Cmd: "hang", Timeout: 300 * time.Millisecond, Run: hang}.Read(context.Background())
		if gerr == nil || gerr.Kind != ErrTimeout {
			t.Errorf("hang: err=%+v, want %s", gerr, ErrTimeout)
		}
		if d := time.Since(start); d != 300*time.Millisecond {
			t.Errorf("hang: answered after %v of virtual time, want the 300 ms bound", d)
		}
	})
}

func TestReadPassesListJSON(t *testing.T) {
	var got []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		got = append([]string{name}, args...)
		return []byte("[]"), nil
	}
	gs, gerr := Source{Cmd: "/x/gate", Run: run}.Read(context.Background())
	if gerr != nil || len(gs) != 0 || strings.Join(got, " ") != "/x/gate list --json" {
		t.Fatalf("got %v, %v, %+v", got, gs, gerr)
	}
	run2 := func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("boom") }
	if _, gerr := (Source{Cmd: "g", Run: run2}).Read(context.Background()); gerr == nil || gerr.Kind != ErrFailed {
		t.Fatalf("plain error: %+v, want failed", gerr)
	}
}
