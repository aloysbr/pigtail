package claudeusage

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
)

func account(t *testing.T, r Report, label string) Account {
	t.Helper()
	for _, a := range r.Accounts {
		if a.Label == label {
			return a
		}
	}
	t.Fatalf("no account %q in %+v", label, r.Accounts)
	return Account{}
}

func hasWarning(r Report, want string) bool {
	for _, w := range r.Warnings {
		if w == want {
			return true
		}
	}
	return false
}

func TestBuildGroupsByAccountSortedByLabel(t *testing.T) {
	cells, files := researchCells(t)
	r := Build(snapshots(cells, files), "perso", now0)
	var labels []string
	for _, a := range r.Accounts {
		labels = append(labels, a.Label)
	}
	if got := strings.Join(labels, " "); got != "i j m t" {
		t.Fatalf("labels = %q, want \"i j m t\"", got)
	}
	m := account(t, r, "m")
	var ids []string
	for _, c := range m.Cells {
		ids = append(ids, c.ID)
	}
	if got := strings.Join(ids, ","); got != "mp,mt,p" || m.AccountUUID != uuidM {
		t.Fatalf("m cells = %q uuid %s", got, m.AccountUUID)
	}
	if !r.GeneratedAt.Equal(now0) || r.CallerScope != "perso" {
		t.Fatalf("GeneratedAt %v CallerScope %q", r.GeneratedAt, r.CallerScope)
	}
}

// Same account, two cells, divergent %: the most recent reading wins, never a
// mean (jp 91 % one minute ago, jt 73 % 23 h ago → 91, not 82).
func TestBuildKeepsMostRecentReadingNeverAMean(t *testing.T) {
	cells, files := researchCells(t)
	j := account(t, Build(snapshots(cells, files), "perso", now0), "j")
	if j.Reading == nil || j.Reading.Cell != "jp" || j.Reading.SevenDay.Percent != 91 {
		t.Fatalf("j reading = %+v", j.Reading)
	}
}

func TestBuildSevenDayResetPassedShowsZero(t *testing.T) {
	c := cellAt("mp", "perso", "m")
	files := map[string][]byte{fileOf(c): maxCell(t, cellFx{UUID: uuidM, Fetched: now0.Add(-30 * time.Hour),
		P7: 64, R7: "2026-10-05T02:00:00.177378+00:00", P5: 1, R5: "2026-10-04T06:00:00+00:00"})}
	m := account(t, Build(snapshots([]CellInput{c}, files), "perso", now0), "m")
	v := m.Reading.SevenDay
	if v.Percent != 0 || v.RawPercent != 64 || !v.ResetPassed || v.ResetsAt == nil {
		t.Fatalf("SevenDay = %+v, want 0 %% shown, raw 64, reset passed, reset kept", v)
	}
}

func TestBuildAgeAndStaleness(t *testing.T) {
	for _, tc := range []struct {
		age   time.Duration
		stale bool
	}{{time.Hour, false}, {time.Hour + time.Second, true}, {time.Minute, false}} {
		c := cellAt("jp", "perso", "j")
		files := map[string][]byte{fileOf(c): maxCell(t, cellFx{UUID: uuidJ, Fetched: now0.Add(-tc.age),
			P7: 10, R7: "2026-10-06T02:00:00+00:00", P5: 1, R5: "2026-10-05T12:00:00+00:00"})}
		rd := account(t, Build(snapshots([]CellInput{c}, files), "perso", now0), "j").Reading
		if rd.AgeSeconds != int64(tc.age/time.Second) || rd.Stale != tc.stale {
			t.Errorf("age %v: AgeSeconds %d Stale %v, want %d %v", tc.age, rd.AgeSeconds, rd.Stale, int64(tc.age/time.Second), tc.stale)
		}
	}
}

func TestBuildFutureReadingAgeZeroAndWarns(t *testing.T) {
	c := cellAt("jp", "perso", "j")
	files := map[string][]byte{fileOf(c): maxCell(t, cellFx{UUID: uuidJ, Fetched: now0.Add(3 * time.Minute),
		P7: 10, R7: "2026-10-06T02:00:00+00:00", P5: 1, R5: "2026-10-05T12:00:00+00:00"})}
	r := Build(snapshots([]CellInput{c}, files), "perso", now0)
	if rd := account(t, r, "j").Reading; rd.AgeSeconds != 0 || rd.Stale {
		t.Fatalf("reading = %+v, want age 0", rd)
	}
	if want := "cell jp: usage reading is 3m in the future (clock skew?) — age shown as 0"; !hasWarning(r, want) {
		t.Fatalf("warnings %q lack %q", r.Warnings, want)
	}
}

// Same fetchedAtMs on two cells of one account: the first cell by id wins.
func TestBuildEqualFetchedAtFirstCellByIDWins(t *testing.T) {
	cj, ck := cellAt("jp", "perso", "j"), cellAt("jt", "gx", "j")
	at := now0.Add(-time.Minute)
	files := map[string][]byte{
		fileOf(cj): maxCell(t, cellFx{UUID: uuidJ, Fetched: at, P7: 91, R7: "2026-10-06T02:00:00+00:00", P5: 1, R5: "2026-10-05T12:00:00+00:00"}),
		fileOf(ck): maxCell(t, cellFx{UUID: uuidJ, Fetched: at, P7: 73, R7: "2026-10-06T02:00:00+00:00", P5: 1, R5: "2026-10-05T12:00:00+00:00"}),
	}
	j := account(t, Build(snapshots([]CellInput{cj, ck}, files), "perso", now0), "j")
	if j.Reading.Cell != "jp" || j.Reading.SevenDay.Percent != 91 {
		t.Fatalf("reading = %+v, want jp's (first cell by id)", j.Reading)
	}
}

func TestBuildMismatchDropsReadingAndWarns(t *testing.T) {
	c := cellAt("mp", "perso", "m")
	files := map[string][]byte{fileOf(c): maxCell(t, cellFx{UUID: uuidM, CacheUUID: uuidJ, Fetched: now0,
		P7: 80, R7: "2026-10-06T02:00:00+00:00", P5: 1, R5: "2026-10-05T12:00:00+00:00"})}
	r := Build(snapshots([]CellInput{c}, files), "perso", now0)
	m := account(t, r, "m")
	if m.Reading != nil || !contains(m.Flags, "no-reading") {
		t.Fatalf("m = %+v, want no reading", m)
	}
	if want := "cell mp: usage cache is for account 33333333, cell is logged in as 11111111 — reading ignored"; !hasWarning(r, want) {
		t.Fatalf("warnings %q lack %q", r.Warnings, want)
	}
}

func TestBuildUnreadableCellNeverZero(t *testing.T) {
	cells, files := researchCells(t)
	files[fileOf(cells[2])] = files[fileOf(cells[2])][:100] // jp truncated mid-rewrite
	r := Build(snapshots(cells, files), "perso", now0)
	if len(r.Unreadable) != 1 || r.Unreadable[0].ID != "jp" || r.Unreadable[0].ConfigDir != "/cells/jp" {
		t.Fatalf("Unreadable = %+v", r.Unreadable)
	}
	j := account(t, r, "j") // still there through jt, with jt's reading
	if j.Reading.Cell != "jt" || j.Reading.SevenDay.Percent != 73 {
		t.Fatalf("j reading = %+v", j.Reading)
	}
	found := false
	for _, w := range r.Warnings {
		found = found || (strings.HasPrefix(w, "cell jp: parse /cells/jp/.claude.json: ") && strings.HasSuffix(w, " — not counted"))
	}
	if !found {
		t.Fatalf("no unreadable warning for jp in %q", r.Warnings)
	}
}

func TestBuildNoCacheAccountHasNoReading(t *testing.T) {
	c := cellAt("p", "perso", "")
	r := Build(snapshots([]CellInput{c}, map[string][]byte{fileOf(c): noCacheCell(uuidM)}), "perso", now0)
	a := account(t, r, "p")
	if a.Reading != nil || !contains(a.Flags, "no-reading") || a.Counted {
		t.Fatalf("a = %+v", a)
	}
}

func TestBuildNotLoggedInWarns(t *testing.T) {
	c := cellAt("p", "perso", "m")
	r := Build(snapshots([]CellInput{c}, map[string][]byte{fileOf(c): []byte(`{"numStartups": 3}`)}), "perso", now0)
	if len(r.Accounts) != 0 || len(r.Unreadable) != 0 {
		t.Fatalf("got %+v", r)
	}
	if want := "cell p: not logged in (no oauthAccount.accountUuid) — not counted"; !hasWarning(r, want) {
		t.Fatalf("warnings %q lack %q", r.Warnings, want)
	}
}

func TestBuildLabelFallsBackToCellIDs(t *testing.T) {
	cells, files := researchCells(t)
	for i := range cells {
		cells[i].Account = "" // table written by a the launcher tooling without the key
	}
	r := Build(snapshots(cells, files), "perso", now0)
	var labels []string
	for _, a := range r.Accounts {
		labels = append(labels, a.Label)
		if !a.LabelFromCells {
			t.Errorf("%s: LabelFromCells false", a.Label)
		}
	}
	if got := strings.Join(labels, " "); got != "ip+it jp+jt mp+mt+p t" {
		t.Fatalf("labels = %q", got)
	}
}

func TestBuildLetterConflictWarnsFirstWins(t *testing.T) {
	cells, files := researchCells(t)
	cells[6].Account = "p" // the launcher config: cell p declares account p, but is logged into m
	r := Build(snapshots(cells, files), "perso", now0)
	account(t, r, "m")
	if want := "account 11111111: cells disagree on its letter (mp=m, p=p) — using m"; !hasWarning(r, want) {
		t.Fatalf("warnings %q lack %q", r.Warnings, want)
	}
}

func TestBuildCapacityAndTier(t *testing.T) {
	cells, files := researchCells(t)
	r := Build(snapshots(cells, files), "globex", now0)
	if m := account(t, r, "m"); m.Capacity == nil || *m.Capacity != 20 || m.Tier != "default_claude_max_20x" {
		t.Fatalf("m capacity %v tier %q", m.Capacity, m.Tier)
	}
	if tt := account(t, r, "t"); tt.Capacity == nil || *tt.Capacity != 5 || tt.Tier != "default_claude_max_5x" {
		t.Fatalf("t capacity %v tier %q", tt.Capacity, tt.Tier)
	}
}

func TestBuildEmptyCollectionsAreArrays(t *testing.T) {
	r := Build(nil, "globex", now0)
	if r.Accounts == nil || r.Unreadable == nil || r.Warnings == nil {
		t.Fatalf("nil slice in %+v — JSON would say null", r)
	}
}

func TestFormatAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0m", 59 * time.Second: "0m", time.Minute: "1m", 59 * time.Minute: "59m",
		time.Hour: "1h00", 12*time.Hour + 36*time.Minute: "12h36", 47*time.Hour + 59*time.Minute: "47h59",
		48 * time.Hour: "2d", 100 * time.Hour: "4d",
	} {
		if got := FormatAge(d); got != want {
			t.Errorf("FormatAge(%v) = %q, want %q", d, got, want)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// the weighted 7-day capacity left counts every account with a
// known capacity and a 7-day reading, whatever the caller's scope. From perso
// the Team seat is still flagged not-perso (not usable), but counted.
// R2: the weighted 7-day USED, rounded: (9+91+1)×20 + 69×5 = 2365 / 65 = 36.4 → 36.
func TestBuildWeightedCountsEveryAccountInAnyScope(t *testing.T) {
	cells, files := researchCells(t)
	for _, sc := range []scope.Scope{"perso", "globex"} {
		r := Build(snapshots(cells, files), sc, now0)
		if r.Weighted7dUsed == nil || *r.Weighted7dUsed != 36 {
			t.Fatalf("%s: Weighted7dUsed = %v, want 36", sc, pct(r.Weighted7dUsed))
		}
		if m := account(t, r, "m"); !m.Usable || !m.Counted {
			t.Fatalf("%s: m = %+v", sc, m)
		}
	}
	tt := account(t, Build(snapshots(cells, files), "perso", now0), "t")
	if tt.Usable || !tt.Counted || !contains(tt.Flags, "not-perso") {
		t.Fatalf("t from perso = %+v, want flagged not-perso, not usable, counted", tt)
	}
}

func TestBuildWorkCountsEveryAccount(t *testing.T) {
	cells, files := researchCells(t)
	r := Build(snapshots(cells, files), "globex", now0)
	// (9+91+1)×20 + 69×5 = 2365 / 65 = 36.4 → 36.
	if r.Weighted7dUsed == nil || *r.Weighted7dUsed != 36 {
		t.Fatalf("Weighted7dUsed = %v, want 36", pct(r.Weighted7dUsed))
	}
	if tt := account(t, r, "t"); !tt.Usable || !tt.Counted || contains(tt.Flags, "not-perso") {
		t.Fatalf("t = %+v", tt)
	}
}

// from perso an account with no perso cell is counted in the total.
// t 0 % ×20, j 95 % ×20: (0 + 95)×20 / 40 = 47.5 → 48 (rounded, not floored).
func TestBuildPersoCountsWorkOnlyAccount(t *testing.T) {
	ct, cj := cellAt("t", "gx", "t"), cellAt("jp", "perso", "j")
	files := map[string][]byte{
		fileOf(ct): maxCell(t, cellFx{UUID: uuidT, Fetched: now0, P7: 0, R7: "2026-10-10T19:00:00+00:00", P5: 0, R5: "2026-10-05T14:00:00+00:00"}),
		fileOf(cj): maxCell(t, cellFx{UUID: uuidJ, Fetched: now0, P7: 95, R7: "2026-10-06T02:00:00+00:00", P5: 0, R5: "2026-10-05T14:00:00+00:00"}),
	}
	r := Build(snapshots([]CellInput{cj, ct}, files), "perso", now0)
	if r.Weighted7dUsed == nil || *r.Weighted7dUsed != 48 {
		t.Fatalf("Weighted7dUsed = %v, want 48 (t counted from perso)", pct(r.Weighted7dUsed))
	}
}

func TestBuildUnknownCapacityExcluded(t *testing.T) {
	ci, cj := cellAt("ip", "perso", "i"), cellAt("jp", "perso", "j")
	files := map[string][]byte{
		fileOf(ci): maxCell(t, cellFx{UUID: uuidI, OrgTier: "default_claude_pro", Fetched: now0, P7: 0, R7: "2026-10-11T08:00:00+00:00", P5: 0, R5: "2026-10-05T14:00:00+00:00"}),
		fileOf(cj): maxCell(t, cellFx{UUID: uuidJ, Fetched: now0, P7: 50, R7: "2026-10-06T02:00:00+00:00", P5: 0, R5: "2026-10-05T14:00:00+00:00"}),
	}
	r := Build(snapshots([]CellInput{ci, cj}, files), "perso", now0)
	i := account(t, r, "i")
	if i.Capacity != nil || i.Counted || !contains(i.Flags, "unknown-capacity") {
		t.Fatalf("i = %+v", i)
	}
	if r.Weighted7dUsed == nil || *r.Weighted7dUsed != 50 {
		t.Fatalf("Weighted %v, want 50", pct(r.Weighted7dUsed))
	}
}

func TestBuildNoCountedAccount(t *testing.T) {
	c := cellAt("p", "perso", "m")
	r := Build(snapshots([]CellInput{c}, map[string][]byte{fileOf(c): noCacheCell(uuidM)}), "perso", now0)
	if r.Weighted7dUsed != nil {
		t.Fatalf("Weighted %v, want nil", pct(r.Weighted7dUsed))
	}
}

func TestBuildFiveHourWindow(t *testing.T) {
	cells, files := researchCells(t)
	r := Build(snapshots(cells, files), "perso", now0)
	j := account(t, r, "j").Reading.FiveHour // jp: 8 %, resets 11:50Z (ahead)
	if j == nil || j.Percent != 8 || j.ResetPassed {
		t.Fatalf("j 5h = %+v", j)
	}
	m := account(t, r, "m").Reading.FiveHour // mt: 4 %, reset 03:00Z passed
	if m == nil || m.Percent != 0 || m.RawPercent != 4 || !m.ResetPassed {
		t.Fatalf("m 5h = %+v", m)
	}
}

// Spec 13: a reading without a seven_day window is not counted (Spec 8) and
// is not "no-reading": its 5-hour window is still shown.
func TestBuildReadingWithoutSevenDay(t *testing.T) {
	c := cellAt("jp", "perso", "j")
	b := maxCell(t, cellFx{UUID: uuidJ, Fetched: now0.Add(-time.Minute), P7: 40, R7: "2026-10-06T02:00:00+00:00", P5: 8, R5: "2026-10-05T11:50:00.177348+00:00"})
	b = []byte(strings.Replace(string(b), `"seven_day": {"utilization": 40,`, `"seven_day": null, "seven_day_was": {"utilization": 40,`, 1))
	r := Build(snapshots([]CellInput{c}, map[string][]byte{fileOf(c): b}), "perso", now0)
	j := account(t, r, "j")
	if j.Reading == nil || j.Reading.SevenDay != nil || j.Reading.FiveHour == nil || j.Reading.FiveHour.Percent != 8 {
		t.Fatalf("reading = %+v, want no 7-day window and the 5-hour one", j.Reading)
	}
	if j.Counted || contains(j.Flags, "no-reading") {
		t.Fatalf("j = %+v, want not counted and no no-reading flag", j)
	}
	if r.Weighted7dUsed != nil {
		t.Fatalf("Weighted %v, want nil", pct(r.Weighted7dUsed))
	}
}

func TestBuildSecondarySources(t *testing.T) {
	cells, files := researchCells(t)
	r := Build(snapshots(cells, files), "perso", now0)
	tt := account(t, r, "t")
	rd := tt.Reading
	if !rd.CapReached || !contains(tt.Flags, "cap") {
		t.Fatalf("t = %+v, want cap reached and flagged", tt)
	}
	if strings.Join(tt.Flags, ",") != "not-perso,cap" {
		t.Fatalf("t flags = %q, want fixed order not-perso,cap", tt.Flags)
	}
	if len(rd.WeeklyScoped) != 1 || rd.WeeklyScoped[0].Model != "Fable" {
		t.Fatalf("WeeklyScoped = %+v", rd.WeeklyScoped)
	}
	if !strings.Contains(string(rd.ExtraUsage), `"used_credits": 10500`) || !strings.Contains(string(rd.Spend), `"severity": "critical"`) {
		t.Fatalf("extra_usage/spend not passed through verbatim: %s / %s", rd.ExtraUsage, rd.Spend)
	}
	if j := account(t, r, "j"); j.Reading.CapReached || contains(j.Flags, "cap") {
		t.Fatalf("j = %+v, a Max account with extra usage off is not capped", j)
	}
}

// pct prints a *int percentage by value, "<nil>" when absent (test messages).
func pct(p *int) string {
	if p == nil {
		return "<nil>"
	}
	return strconv.Itoa(*p)
}
