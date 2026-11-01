package claudeusage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
)

// ranked is order_test.go's helper (2664eaa), never redeclared here:
// ranked(cells, order...) returns a copy of cells, each listed id's Rank set to
// its index in order — below, the launcher config's declaration order (the rank rule).

func targetIDs(ts []RefreshTarget) string {
	ids := make([]string, 0, len(ts))
	for _, t := range ts {
		ids = append(ids, t.Cell.ID)
	}
	return strings.Join(ids, ",")
}

// S2/S3/from the master scope, one cell per account, the caller's own
// scope first, then the lowest rank; t has only a globex cell and is refreshed.
func TestRefreshTargetsPerso(t *testing.T) {
	cells, files := researchCells(t)
	ts := RefreshTargets(snapshots(ranked(cells, "mt", "mp", "it", "ip", "jt", "jp", "t", "p"), files), scope.Master())
	if got := targetIDs(ts); got != "mp,ip,jp,t" {
		t.Fatalf("targets = %s, want mp,ip,jp,t", got)
	}
	if ts[3].AccountUUID != uuidT || ts[0].AccountUUID != uuidM {
		t.Fatalf("accounts = %s, %s", ts[0].AccountUUID, ts[3].AccountUUID)
	}
	if !ts[2].Before.HasReading || !ts[2].Before.FetchedAt.Equal(now0.Add(-time.Minute)) {
		t.Fatalf("jp before = %+v", ts[2].Before)
	}
}

// a non-master caller only ever targets cells of its own scope.
func TestRefreshTargetsWork(t *testing.T) {
	cells, files := researchCells(t)
	ts := RefreshTargets(snapshots(ranked(cells, "mt", "mp", "it", "ip", "jt", "jp", "t", "p"), files), "globex")
	if got := targetIDs(ts); got != "mt,it,jt,t" {
		t.Fatalf("targets = %s, want mt,it,jt,t", got)
	}
	for _, tg := range ts {
		if sc, _ := scope.Parse(tg.Cell.Scope); sc != "globex" {
			t.Errorf("globex caller targets %s (scope %s)", tg.Cell.ID, tg.Cell.Scope)
		}
	}
}

// An account whose only cells are hidden is not targeted at all.
func TestRefreshTargetsSkipsHiddenOnlyAccount(t *testing.T) {
	cells, files := researchCells(t)
	// drop every globex cell of m: m is now perso-only, hence invisible from globex
	var kept []CellInput
	for _, c := range cells {
		if c.ID != "mt" {
			kept = append(kept, c)
		}
	}
	ts := RefreshTargets(snapshots(ranked(kept, "mt", "mp", "it", "ip", "jt", "jp", "t", "p"), files), "globex")
	for _, tg := range ts {
		if tg.AccountUUID == uuidM {
			t.Fatalf("m targeted via %s from globex", tg.Cell.ID)
		}
	}
}

// Unreadable or logged-out cells are never launched. Another usable cell of
// the caller's scope is (m: p); none left in that scope, the account fails
// without a launch — never a cell of another scope (i: never it). S2.
func TestRefreshTargetsSkipsUnreadableAndLoggedOut(t *testing.T) {
	cells, files := researchCells(t)
	files[fileOf(cells[4])] = files[fileOf(cells[4])][:50]    // mp unreadable
	files[fileOf(cells[0])] = []byte(`{"oauthAccount":null}`) // ip logged out
	ts := RefreshTargets(snapshots(ranked(cells, "mt", "mp", "it", "ip", "jt", "jp", "t", "p"), files), scope.Master())
	// m: mp unreadable → p (perso, rank 7), never mt (globex, rank 0)
	// i: ip logged out, i's only perso cell → failed on ip, it not launched
	// ordered by the chosen cells' ranks: ip 3, jp 5, t 6, p 7
	if got := targetIDs(ts); got != "ip,jp,t,p" {
		t.Fatalf("targets = %s, want ip,jp,t,p", got)
	}
	if ts[0].AccountUUID != uuidI || ts[0].Reason != NoUsableCell || ts[0].Label != "i" {
		t.Fatalf("i target = %+v, want reason %q", ts[0], NoUsableCell)
	}
	for _, tg := range ts[1:] {
		if tg.Reason != "" {
			t.Errorf("%s: reason %q, want a launch", tg.Cell.ID, tg.Reason)
		}
	}
}

// S2 case 2 vs 3: every perso cell of m unusable → m fails on mp, mt is never
// chosen; t has no perso cell at all → t is launched.
func TestRefreshTargetsNoFallbackToOtherScope(t *testing.T) {
	cells, files := researchCells(t)
	files[fileOf(cells[4])] = []byte(`{"oauthAccount":`) // mp unreadable
	files[fileOf(cells[6])] = []byte(`{}`)               // p logged out
	ts := RefreshTargets(snapshots(ranked(cells, "mt", "mp", "it", "ip", "jt", "jp", "t", "p"), files), scope.Master())
	if got := targetIDs(ts); got != "mp,ip,jp,t" {
		t.Fatalf("targets = %s, want mp,ip,jp,t", got)
	}
	if ts[0].AccountUUID != uuidM || ts[0].Reason != NoUsableCell {
		t.Fatalf("m target = %+v", ts[0])
	}
	if ts[3].Cell.ID != "t" || ts[3].Reason != "" {
		t.Fatalf("t target = %+v, want a launch", ts[3])
	}
}

// Gap 2 of review 2: seen from globex, it unreadable and ip (perso, hidden)
// readable. Build lists i through ip; the target must make i failed on it —
// the uuid learnt from the hidden ip, nothing of ip returned — never skipped.
func TestRefreshTargetsWorkUnusableOwnCellWithHiddenUsable(t *testing.T) {
	cells, files := researchCells(t)
	files[fileOf(cells[1])] = []byte(`{"oauthAccount":`) // it unreadable; ip untouched
	ts := RefreshTargets(snapshots(ranked(cells, "mt", "mp", "it", "ip", "jt", "jp", "t", "p"), files), "globex")
	if got := targetIDs(ts); got != "mt,it,jt,t" {
		t.Fatalf("targets = %s, want mt,it,jt,t", got)
	}
	if tg := ts[1]; tg.AccountUUID != uuidI || tg.Reason != NoUsableCell || tg.Label != "i" || tg.Before.HasReading {
		t.Fatalf("i target = %+v, want failed %q on it", tg, NoUsableCell)
	}
	for _, tg := range ts {
		if sc, _ := scope.Parse(tg.Cell.Scope); sc != "globex" {
			t.Errorf("globex caller targets %s (scope %s)", tg.Cell.ID, tg.Cell.Scope)
		}
	}
}

// Without ranks (a table older than the rank key), order and choice fall back to ids.
func TestRefreshTargetsUnranked(t *testing.T) {
	cells, files := researchCells(t)
	ts := RefreshTargets(snapshots(cells, files), scope.Master())
	if got := targetIDs(ts); got != "ip,jp,mp,t" {
		t.Fatalf("targets = %s, want ip,jp,mp,t", got)
	}
}

// fakeLaunch rewrites a cell's file as if Claude Code had fetched at `at`,
// unless behave[configDir] says otherwise; it records every call in order.
type fakeLaunch struct {
	files  map[string][]byte
	at     time.Time
	behave map[string]func(ctx context.Context) error // nil → write a fresh reading
	calls  []string
	busy   bool
	t      *testing.T
}

func (f *fakeLaunch) launch(ctx context.Context, dir string) error {
	if f.busy {
		f.t.Fatalf("launch of %s while another one runs", dir)
	}
	f.busy = true
	defer func() { f.busy = false }()
	f.calls = append(f.calls, dir)
	if b := f.behave[dir]; b != nil {
		return b(ctx)
	}
	p := dir + "/" + FileName
	var uuid string
	if s := ReadCell(CellInput{ConfigDir: dir}, func(string) ([]byte, error) { return f.files[p], nil }); s.AccountUUID != "" {
		uuid = s.AccountUUID
	}
	f.files[p] = maxCell(f.t, cellFx{UUID: uuid, Fetched: f.at, P7: 5, R7: "2026-10-11T17:00:00+00:00", P5: 1, R5: "2026-10-05T15:00:00+00:00"})
	return nil
}

func refreshDeps(f *fakeLaunch, now time.Time) RefreshDeps {
	fs := &fakeFS{files: f.files}
	return RefreshDeps{Launch: f.launch, ReadFile: fs.read, Now: func() time.Time { return now }, Timeout: 60 * time.Second}
}

func byCell(res []RefreshResult) map[string]RefreshResult {
	m := map[string]RefreshResult{}
	for _, r := range res {
		m[r.Cell] = r
	}
	return m
}

func TestRefreshAllOK(t *testing.T) {
	cells, files := researchCells(t)
	ts := RefreshTargets(snapshots(ranked(cells, "mt", "mp", "it", "ip", "jt", "jp", "t", "p"), files), scope.Master())
	f := &fakeLaunch{files: files, at: now0.Add(5 * time.Second), t: t}
	res := byCell(Refresh(context.Background(), ts, refreshDeps(f, now0)))
	for _, id := range []string{"mp", "ip", "jp"} {
		r := res[id]
		if r.Status != RefreshOK || r.Error != "" || r.FetchedAfter == nil || !r.FetchedAfter.Equal(now0.Add(5*time.Second)) {
			t.Errorf("%s = %+v", id, r)
		}
	}
	// jp's reading is exactly 60 s old: Claude Code's guard is "< 60 s", so it is launched (AC6)
	if len(f.calls) != 4 {
		t.Fatalf("calls = %v, want 4 (mp ip jp t)", f.calls)
	}
}

// AC6: a reading younger than 60 s is fresh — no launch, not an error.
func TestRefreshFreshGuard(t *testing.T) {
	cells, files := researchCells(t)
	ts := RefreshTargets(snapshots(ranked(cells, "mt", "mp", "it", "ip", "jt", "jp", "t", "p"), files), scope.Master())
	f := &fakeLaunch{files: files, at: now0, t: t}
	// jp was fetched at now0-60s; seen at now0-1s it is 59 s old
	res := byCell(Refresh(context.Background(), ts, refreshDeps(f, now0.Add(-time.Second))))
	if r := res["jp"]; r.Status != RefreshFresh || r.Error != "" || r.FetchedBefore == nil || r.FetchedAfter == nil || !r.FetchedAfter.Equal(*r.FetchedBefore) {
		t.Fatalf("jp = %+v, want fresh with fetched_after = fetched_before", r)
	}
	for _, c := range f.calls {
		if strings.HasSuffix(c, "/jp") {
			t.Fatal("a fresh cell was launched")
		}
	}
}

// AC5: one launch at a time, in target order.
func TestRefreshIsSequential(t *testing.T) {
	cells, files := researchCells(t)
	ts := RefreshTargets(snapshots(ranked(cells, "mt", "mp", "it", "ip", "jt", "jp", "t", "p"), files), scope.Master())
	f := &fakeLaunch{files: files, at: now0.Add(time.Second), t: t} // busy guard fails the test on overlap
	Refresh(context.Background(), ts, refreshDeps(f, now0))
	if got := strings.Join(f.calls, ","); got != "/cells/mp,/cells/ip,/cells/jp,/cells/t" {
		t.Fatalf("calls = %s", got)
	}
}

// AC2: every failure kind is a failed result with its exact reason, and the
// other accounts still refresh.
func TestRefreshFailures(t *testing.T) {
	cases := map[string]struct {
		behave func(ctx context.Context) error
		want   string
	}{
		"exit":      {func(context.Context) error { return errors.New("claude exited 3: not logged in") }, "claude exited 3: not logged in"},
		"unchanged": {func(context.Context) error { return nil }, "usage cache unchanged after claude -p /usage"},
		"not found": {func(context.Context) error { return errors.New("claude not found on PATH (cmux shim skipped)") }, "claude not found on PATH (cmux shim skipped)"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			cells, files := researchCells(t)
			ts := RefreshTargets(snapshots(ranked(cells, "mt", "mp", "it", "ip", "jt", "jp", "t", "p"), files), scope.Master())
			f := &fakeLaunch{files: files, at: now0.Add(time.Second), t: t, behave: map[string]func(context.Context) error{"/cells/t": c.behave}}
			res := byCell(Refresh(context.Background(), ts, refreshDeps(f, now0)))
			if r := res["t"]; r.Status != RefreshFailed || r.Error != c.want || r.FetchedBefore == nil {
				t.Fatalf("t = %+v, want failed %q", r, c.want)
			}
			// re-read after the launch, failed or not: the cache did not move
			if r := res["t"]; r.FetchedAfter == nil || !r.FetchedAfter.Equal(*r.FetchedBefore) {
				t.Fatalf("t fetched_after = %v, want the re-read reading %v", r.FetchedAfter, r.FetchedBefore)
			}
			if res["mp"].Status != RefreshOK {
				t.Fatalf("mp = %+v: one failure must not stop the others", res["mp"])
			}
		})
	}
}

// Review Focus 3: a launch that outlives its deadline is "timed out after 60s".
func TestRefreshTimeout(t *testing.T) {
	cells, files := researchCells(t)
	ts := RefreshTargets(snapshots(ranked(cells, "mt", "mp", "it", "ip", "jt", "jp", "t", "p"), files), scope.Master())
	f := &fakeLaunch{files: files, at: now0.Add(time.Second), t: t, behave: map[string]func(context.Context) error{
		"/cells/t": func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
	}}
	d := refreshDeps(f, now0)
	d.Timeout = 10 * time.Millisecond
	res := byCell(Refresh(context.Background(), ts, d))
	if r := res["t"]; r.Status != RefreshFailed || r.Error != "timed out after 0s" || r.FetchedAfter == nil || !r.FetchedAfter.Equal(*r.FetchedBefore) {
		t.Fatalf("t = %+v", r)
	}
	// the message prints whole seconds: the CLI's 60 s reads "timed out after 60s"
	if got := timeoutReason(60 * time.Second); got != "timed out after 60s" {
		t.Fatalf("timeoutReason = %q", got)
	}
}

// Review Focus 2: the file is rewritten (truncated) under us — failed, re-read reason.
func TestRefreshRereadFails(t *testing.T) {
	cells, files := researchCells(t)
	ts := RefreshTargets(snapshots(ranked(cells, "mt", "mp", "it", "ip", "jt", "jp", "t", "p"), files), scope.Master())
	f := &fakeLaunch{files: files, at: now0.Add(time.Second), t: t}
	f.behave = map[string]func(context.Context) error{"/cells/mp": func(context.Context) error {
		files["/cells/mp/"+FileName] = []byte(`{"oauthAccount":`)
		return nil
	}}
	res := byCell(Refresh(context.Background(), ts, refreshDeps(f, now0)))
	if r := res["mp"]; r.Status != RefreshFailed || !strings.HasPrefix(r.Error, "re-read failed: parse /cells/mp/.claude.json: ") || r.FetchedAfter != nil {
		t.Fatalf("mp = %+v (fetched_after null: the cell has no reading any more)", r)
	}
}

// AC11 / S2 case 2: a target with a Reason fails without any launch, and
// without a progress line.
func TestRefreshNoUsableCellNotLaunched(t *testing.T) {
	cells, files := researchCells(t)
	files[fileOf(cells[4])] = []byte(`{"oauthAccount":`) // mp unreadable
	files[fileOf(cells[6])] = []byte(`{}`)               // p logged out
	ts := RefreshTargets(snapshots(ranked(cells, "mt", "mp", "it", "ip", "jt", "jp", "t", "p"), files), scope.Master())
	f := &fakeLaunch{files: files, at: now0.Add(time.Second), t: t}
	d := refreshDeps(f, now0)
	var seen []string
	d.Progress = func(tg RefreshTarget) { seen = append(seen, tg.Cell.ID) }
	res := byCell(Refresh(context.Background(), ts, d))
	if r := res["mp"]; r.Status != RefreshFailed || r.Error != NoUsableCell || r.FetchedBefore != nil || r.FetchedAfter != nil {
		t.Fatalf("mp = %+v", r)
	}
	for _, c := range f.calls {
		if c == "/cells/mp" || c == "/cells/mt" || c == "/cells/p" {
			t.Fatalf("launched %s for m", c)
		}
	}
	if got := strings.Join(seen, ","); got != "ip,jp,t" {
		t.Fatalf("progress = %s", got)
	}
}

// The cache now belongs to another account: failed, named by its uuid8.
func TestRefreshCacheOfAnotherAccount(t *testing.T) {
	cells, files := researchCells(t)
	ts := RefreshTargets(snapshots(ranked(cells, "mt", "mp", "it", "ip", "jt", "jp", "t", "p"), files), scope.Master())
	f := &fakeLaunch{files: files, at: now0.Add(time.Second), t: t}
	f.behave = map[string]func(context.Context) error{"/cells/ip": func(context.Context) error {
		files["/cells/ip/"+FileName] = maxCell(t, cellFx{UUID: uuidI, CacheUUID: uuidJ, Fetched: now0, P7: 1, R7: "2026-10-11T00:00:00+00:00", P5: 1, R5: "2026-10-05T15:00:00+00:00"})
		return nil
	}}
	res := byCell(Refresh(context.Background(), ts, refreshDeps(f, now0)))
	if r := res["ip"]; r.Status != RefreshFailed || r.Error != fmt.Sprintf("usage cache written for another account (%s)", uuidJ[:8]) {
		t.Fatalf("ip = %+v", r)
	}
}

// Progress is called once per launched cell, never for a fresh one.
func TestRefreshProgress(t *testing.T) {
	cells, files := researchCells(t)
	ts := RefreshTargets(snapshots(ranked(cells, "mt", "mp", "it", "ip", "jt", "jp", "t", "p"), files), scope.Master())
	f := &fakeLaunch{files: files, at: now0, t: t}
	d := refreshDeps(f, now0.Add(-time.Second)) // jp fresh
	var seen []string
	d.Progress = func(tg RefreshTarget) { seen = append(seen, tg.Cell.ID) }
	Refresh(context.Background(), ts, d)
	if got := strings.Join(seen, ","); got != "mp,ip,t" {
		t.Fatalf("progress = %s", got)
	}
}

// AttachRefresh: each account gets its result; an account with no target is skipped.
func TestAttachRefreshSkipsUntargeted(t *testing.T) {
	cells, files := researchCells(t)
	snaps := snapshots(ranked(cells, "mt", "mp", "it", "ip", "jt", "jp", "t", "p"), files)
	r := Build(snaps, "globex", now0)
	res := []RefreshResult{{Status: RefreshOK, Cell: "jt", AccountUUID: uuidJ}, {Status: RefreshFailed, Cell: "t", Error: "x", AccountUUID: uuidT}}
	r.AttachRefresh(res)
	if a := account(t, r, "j"); a.Refresh == nil || a.Refresh.Status != RefreshOK {
		t.Fatalf("j refresh = %+v", a.Refresh)
	}
	if a := account(t, r, "i"); a.Refresh == nil || a.Refresh.Status != RefreshSkipped || a.Refresh.Error != "no visible cell" || a.Refresh.Cell != "" {
		t.Fatalf("i refresh = %+v", a.Refresh)
	}
	if failed, attempted := RefreshCounts(res); failed != 1 || attempted != 2 {
		t.Fatalf("counts = %d of %d", failed, attempted)
	}
	if len(r.RefreshFailures) != 0 {
		t.Fatalf("refresh_failures = %+v, want none: every result's account is in the report", r.RefreshFailures)
	}
}

// Gap 1 of review 1: t has one cell; its re-read fails, so Build drops t. The
// failure must stay visible (RefreshFailures) and counted (RefreshCounts).
func TestAttachRefreshAccountGone(t *testing.T) {
	cells, files := researchCells(t)
	files[fileOf(cells[7])] = []byte(`{"oauthAccount":`) // t unreadable at the final re-read
	r := Build(snapshots(ranked(cells, "mt", "mp", "it", "ip", "jt", "jp", "t", "p"), files), scope.Master(), now0)
	res := []RefreshResult{
		{Status: RefreshOK, Cell: "mp", AccountUUID: uuidM, Label: "m"},
		{Status: RefreshFailed, Cell: "t", Error: "re-read failed: x", AccountUUID: uuidT, Label: "t"},
	}
	r.AttachRefresh(res)
	if len(r.RefreshFailures) != 1 || r.RefreshFailures[0].Label != "t" || r.RefreshFailures[0].Cell != "t" ||
		r.RefreshFailures[0].AccountUUID != uuidT || r.RefreshFailures[0].Error != "re-read failed: x" {
		t.Fatalf("refresh_failures = %+v", r.RefreshFailures)
	}
	if failed, attempted := RefreshCounts(res); failed != 1 || attempted != 2 {
		t.Fatalf("counts = %d of %d", failed, attempted)
	}
	b, _ := json.Marshal(r)
	if !strings.Contains(string(b), `"refresh_failures":[{"label":"t","account_uuid":"`+uuidT+`","status":"failed","cell":"t"`) {
		t.Fatalf("json lacks the failure:\n%s", b)
	}
}

// AC9: without --refresh, no account carries a refresh key.
func TestBuildHasNoRefreshByDefault(t *testing.T) {
	cells, files := researchCells(t)
	r := Build(snapshots(cells, files), scope.Master(), now0)
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), `"refresh"`) || strings.Contains(string(b), `"refresh_failures"`) {
		t.Fatal("refresh key present without --refresh")
	}
}

// staleIDs is the cell id of each target StaleTargets keeps.
func staleIDs(ts []RefreshTarget) string { return targetIDs(ts) }

// Lot 2: the sweep launches only the accounts whose reading — the
// newest of all their cells — is older than the window or absent; a target
// that fails without a launch (Reason) is kept, to be said.
func TestStaleTargets(t *testing.T) {
	cells := []CellInput{
		rankedCell("ap", "perso", "a", 0), // fresh: 5 min
		rankedCell("bp", "perso", "b", 1), // stale: 45 min
		rankedCell("cp", "perso", "c", 2), // logged in, no reading
		rankedCell("dp", "perso", "d", 3), // exactly at the window: fresh
	}
	files := map[string][]byte{
		fileOf(cells[0]): usageFile("aaaaaaaa-0000-4000-8000-000000000000", 5*time.Minute, &win{10, hour}, &win{10, day}),
		fileOf(cells[1]): usageFile("bbbbbbbb-0000-4000-8000-000000000000", 45*time.Minute, &win{10, hour}, &win{10, day}),
		fileOf(cells[2]): noCacheCell("cccccccc-0000-4000-8000-000000000000"),
		fileOf(cells[3]): usageFile("dddddddd-0000-4000-8000-000000000000", PickFreshFor, &win{10, hour}, &win{10, day}),
	}
	snaps := snapshots(cells, files)
	targets := RefreshTargets(snaps, scope.Master())
	targets = append(targets, RefreshTarget{AccountUUID: "eeeeeeee", Label: "e", Cell: rankedCell("ep", "perso", "e", 4), Reason: NoUsableCell})
	got := StaleTargets(targets, Build(snaps, scope.Master(), pickNow).Accounts, pickNow, PickFreshFor)
	if ids := staleIDs(got); ids != "bp,cp,ep" {
		t.Fatalf("stale targets = %s, want bp,cp,ep", ids)
	}
}

// The age is the account's, never the target cell's: mp is the target of m
// (own scope first) and its cache is 3 h old, but mt — same account — read
// 10 min ago. m is fresh: nothing to launch.
func TestStaleTargetsAccountFreshViaOtherCell(t *testing.T) {
	const u = "11111111-1111-4111-8111-111111111111"
	cells := []CellInput{rankedCell("mp", "perso", "m", 1), rankedCell("mt", "gx", "m", 0)}
	files := map[string][]byte{
		fileOf(cells[0]): usageFile(u, 3*hour, &win{10, hour}, &win{10, day}),
		fileOf(cells[1]): usageFile(u, 10*time.Minute, &win{10, hour}, &win{10, day}),
	}
	snaps := snapshots(cells, files)
	targets := RefreshTargets(snaps, scope.Master())
	if ids := targetIDs(targets); ids != "mp" {
		t.Fatalf("precondition: targets = %s, want mp", ids)
	}
	if got := StaleTargets(targets, Build(snaps, scope.Master(), pickNow).Accounts, pickNow, PickFreshFor); len(got) != 0 {
		t.Fatalf("stale targets = %s, want none (mt's reading is fresh)", targetIDs(got))
	}
}
