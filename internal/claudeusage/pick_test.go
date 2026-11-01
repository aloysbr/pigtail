package claudeusage

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/scope"
)

// pickNow is the instant every Pick test reads at.
var pickNow = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

// win is one cached window: utilization p, reset at pickNow+reset (reset 0 =
// no resets_at at all).
type win struct {
	p     float64
	reset time.Duration
}

func (w *win) json() string {
	if w == nil {
		return "null"
	}
	r := "null"
	if w.reset != 0 {
		r = `"` + pickNow.Add(w.reset).Format("2006-01-02T15:04:05.000000+00:00") + `"`
	}
	return fmt.Sprintf(`{"utilization": %v, "resets_at": %s}`, w.p, r)
}

// usageFile is a logged-in cell's .claude.json whose reading is age old.
func usageFile(uuid string, age time.Duration, five, seven *win) []byte {
	return []byte(fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q,"organizationRateLimitTier":"default_claude_max_20x","userRateLimitTier":null},
"cachedUsageUtilization":{"fetchedAtMs":%d,"accountUuid":%q,"utilization":{"five_hour":%s,"seven_day":%s}}}`,
		uuid, pickNow.Add(-age).UnixMilli(), uuid, five.json(), seven.json()))
}

// rankedCell is a cell at /cells/<id> with a declaration rank.
func rankedCell(id, sc, letter string, rank int) CellInput {
	c := cellAt(id, sc, letter)
	c.Rank = &rank
	return c
}

func mustPick(t *testing.T, cells []CellInput, files map[string][]byte, opt PickOptions) Choice {
	t.Helper()
	ch, err := Pick(snapshots(cells, files), opt, pickNow)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	return ch
}

func rankingLabels(ch Choice) string {
	var ls []string
	for _, c := range ch.Ranking {
		ls = append(ls, c.Label+"="+string(c.Group))
	}
	return strings.Join(ls, " ")
}

const (
	hour = time.Hour
	day  = 24 * time.Hour
)

// A1: under the margin, the soonest 7-day reset wins.
func TestPickEarliestSevenDayReset(t *testing.T) {
	ip, mp, jp := rankedCell("ip", "perso", "i", 0), rankedCell("mp", "perso", "m", 1), rankedCell("jp", "perso", "j", 2)
	files := map[string][]byte{
		fileOf(ip): usageFile(uuidI, time.Minute, &win{10, 2 * hour}, &win{40, 5 * day}),
		fileOf(mp): usageFile(uuidM, time.Minute, &win{10, 2 * hour}, &win{80, 1 * day}),
		fileOf(jp): usageFile(uuidJ, time.Minute, &win{10, 2 * hour}, &win{20, 3 * day}),
	}
	ch := mustPick(t, []CellInput{ip, mp, jp}, files, PickOptions{Scope: "perso"})
	if ch.Label != "m" || ch.Cell.ID != "mp" || ch.Group != "fresh" {
		t.Fatalf("pick = %s/%s/%s, want m/mp/fresh (ranking %s)", ch.Label, ch.Cell.ID, ch.Group, rankingLabels(ch))
	}
	if got := rankingLabels(ch); got != "m=fresh j=fresh i=fresh" {
		t.Fatalf("ranking = %q", got)
	}
	if len(ch.Warnings) != 0 {
		t.Fatalf("warnings = %q, want none for a fresh pick", ch.Warnings)
	}
}

// A1: equal 7-day resets → declaration rank, then id; the account's cell is
// its lowest-ranked one in the scope.
func TestPickTieByRank(t *testing.T) {
	ip, jp, jp2 := rankedCell("ip", "perso", "i", 3), rankedCell("jp", "perso", "j", 1), rankedCell("jq", "perso", "j", 0)
	files := map[string][]byte{
		fileOf(ip):  usageFile(uuidI, time.Minute, &win{10, 2 * hour}, &win{40, 3 * day}),
		fileOf(jp):  usageFile(uuidJ, time.Minute, &win{10, 2 * hour}, &win{40, 3 * day}),
		fileOf(jp2): usageFile(uuidJ, time.Minute, &win{10, 2 * hour}, &win{40, 3 * day}),
	}
	ch := mustPick(t, []CellInput{ip, jp, jp2}, files, PickOptions{Scope: "perso"})
	if ch.Label != "j" || ch.Cell.ID != "jq" {
		t.Fatalf("pick = %s/%s, want j on jq (rank 0)", ch.Label, ch.Cell.ID)
	}
	// Unranked cells: id decides.
	a, b := cellAt("bp", "perso", "b"), cellAt("ap", "perso", "a")
	files = map[string][]byte{
		fileOf(a): usageFile(uuidI, time.Minute, &win{10, 2 * hour}, &win{40, 3 * day}),
		fileOf(b): usageFile(uuidJ, time.Minute, &win{10, 2 * hour}, &win{40, 3 * day}),
	}
	if ch := mustPick(t, []CellInput{a, b}, files, PickOptions{Scope: "perso"}); ch.Cell.ID != "ap" {
		t.Fatalf("unranked tie = %s, want ap (id)", ch.Cell.ID)
	}
}

// A2: 89.9 / 94.9 are under the margin, 90 / 95 are over it.
func TestPickMarginThresholds(t *testing.T) {
	cases := []struct {
		name   string
		sel    *float64
		p5, p7 float64
		group  PickGroup
	}{
		{"defaults: both just under", nil, 89.9, 89.9, "fresh"},
		{"defaults: 5h at 90", nil, 90, 10, "over-margin"},
		{"defaults: 7d at 90", nil, 10, 90, "over-margin"},
		{"ceiling 70: both just under", fp(70), 69.9, 69.9, "fresh"},
		{"ceiling 70: 5h at 70", fp(70), 70, 10, "over-margin"},
		{"ceiling 70: 7d at 70", fp(70), 10, 70, "over-margin"},
	}
	for _, c := range cases {
		ip := rankedCell("ip", "perso", "i", 0)
		ip.SelectPct = c.sel
		files := map[string][]byte{fileOf(ip): usageFile(uuidI, time.Minute, &win{c.p5, 2 * hour}, &win{c.p7, 3 * day})}
		ch := mustPick(t, []CellInput{ip}, files, PickOptions{Scope: "perso"})
		if ch.Group != c.group {
			t.Errorf("%s: group = %s, want %s", c.name, ch.Group, c.group)
		}
	}
}

// An account at or above its own ceiling is never offered while another is
// under its own, even when its 7-day window renews first.
func TestPickNeverOffersOverCeiling(t *testing.T) {
	mp, ip := rankedCell("mp", "perso", "m", 0), rankedCell("ip", "perso", "i", 1)
	mp.SelectPct, mp.SwitchPct = fp(70), fp(78)
	files := map[string][]byte{
		fileOf(mp): usageFile(uuidM, time.Minute, &win{10, hour}, &win{72, day}),
		fileOf(ip): usageFile(uuidI, time.Minute, &win{10, hour}, &win{50, 5 * day}),
	}
	ch := mustPick(t, []CellInput{mp, ip}, files, PickOptions{Scope: "perso"})
	if ch.Label != "i" {
		t.Fatalf("pick = %s, want i (m at 72 %% is over its 70 %% ceiling) — %s", ch.Label, rankingLabels(ch))
	}
	files[fileOf(mp)] = usageFile(uuidM, time.Minute, &win{10, hour}, &win{69, day})
	ch = mustPick(t, []CellInput{mp, ip}, files, PickOptions{Scope: "perso"})
	if ch.Label != "m" {
		t.Fatalf("pick = %s, want m (69 %% under its ceiling, renews first) — %s", ch.Label, rankingLabels(ch))
	}
	if ch.Thresholds != (Thresholds{70, 78}) {
		t.Fatalf("candidate thresholds = %+v", ch.Thresholds)
	}
}

// Two cells of one account disagree (a half-regenerated table): the
// strictest value wins, field by field.
func TestPickUsesStrictestCellThresholds(t *testing.T) {
	a, b := rankedCell("mp", "perso", "m", 0), rankedCell("mt", "gx", "m", 1)
	a.SelectPct = fp(70)
	files := map[string][]byte{
		fileOf(a): usageFile(uuidM, time.Minute, &win{10, hour}, &win{75, day}),
		fileOf(b): usageFile(uuidM, time.Minute, &win{10, hour}, &win{75, day}),
	}
	for _, sc := range []scope.Scope{"perso", "globex"} {
		ch := mustPick(t, []CellInput{a, b}, files, PickOptions{Scope: sc})
		if ch.Group != GroupOverMargin || ch.Thresholds.SelectPct != 70 || ch.Thresholds.SwitchPct != 98 {
			t.Errorf("%s: group %s thresholds %+v, want over-margin {70 98}", sc, ch.Group, ch.Thresholds)
		}
	}
}

func TestPickOverMarginWarningCitesRule(t *testing.T) {
	ip := rankedCell("ip", "perso", "i", 0)
	files := map[string][]byte{fileOf(ip): usageFile(uuidI, time.Minute, &win{10, hour}, &win{92, day})}
	ch := mustPick(t, []CellInput{ip}, files, PickOptions{Scope: "perso"})
	if len(ch.Warnings) != 1 || !strings.Contains(ch.Warnings[0], OverMarginRule) || strings.Contains(ch.Warnings[0], "95") {
		t.Fatalf("warnings = %q, want the rule %q and no 95", ch.Warnings, OverMarginRule)
	}
}

// A2: a window whose reset has passed counts 0 %.
func TestPickResetPassedIsZero(t *testing.T) {
	ip, mp := rankedCell("ip", "perso", "i", 0), rankedCell("mp", "perso", "m", 1)
	files := map[string][]byte{
		fileOf(ip): usageFile(uuidI, time.Minute, &win{100, -time.Minute}, &win{99, -hour}),
		fileOf(mp): usageFile(uuidM, time.Minute, &win{95, hour}, &win{10, 2 * day}),
	}
	ch := mustPick(t, []CellInput{ip, mp}, files, PickOptions{Scope: "perso"})
	if ch.Label != "i" || ch.Group != "fresh" {
		t.Fatalf("pick = %s/%s, want i/fresh (its 100 %% windows have reset) — ranking %s", ch.Label, ch.Group, rankingLabels(ch))
	}
}

// A3: a fresh account with a far 7-day reset beats a stale one with a close reset.
func TestPickFreshBeforeStale(t *testing.T) {
	ip, mp := rankedCell("ip", "perso", "i", 0), rankedCell("mp", "perso", "m", 1)
	files := map[string][]byte{
		fileOf(ip): usageFile(uuidI, 31*time.Minute, &win{10, hour}, &win{10, 1 * day}),
		fileOf(mp): usageFile(uuidM, 30*time.Minute, &win{10, hour}, &win{10, 6 * day}),
	}
	ch := mustPick(t, []CellInput{ip, mp}, files, PickOptions{Scope: "perso"})
	if got := rankingLabels(ch); got != "m=fresh i=stale" {
		t.Fatalf("ranking = %q, want m fresh (30 min) before i stale (31 min)", got)
	}
	if ch.Age == nil || *ch.Age != 30*time.Minute {
		t.Fatalf("age = %v, want 30m", ch.Age)
	}
	// Only stale ones left → picked with a warning naming its age.
	files[fileOf(mp)] = usageFile(uuidM, hour, &win{10, hour}, &win{10, 6 * day})
	ch = mustPick(t, []CellInput{ip, mp}, files, PickOptions{Scope: "perso"})
	if ch.Label != "i" || ch.Group != "stale" {
		t.Fatalf("pick = %s/%s, want i/stale", ch.Label, ch.Group)
	}
	if len(ch.Warnings) != 1 || !strings.Contains(ch.Warnings[0], "i's reading is 31m old") || !strings.Contains(ch.Warnings[0], "no fresh account under the margin") {
		t.Fatalf("warnings = %q", ch.Warnings)
	}
}

// A3: no reading (no cache, or a window missing) comes after stale.
func TestPickNoReadingAfterStale(t *testing.T) {
	ip, mp, jp := rankedCell("ip", "perso", "i", 0), rankedCell("mp", "perso", "m", 1), rankedCell("jp", "perso", "j", 2)
	files := map[string][]byte{
		fileOf(ip): noCacheCell(uuidI),
		fileOf(mp): usageFile(uuidM, 3*hour, &win{10, hour}, &win{10, 6 * day}),
		fileOf(jp): usageFile(uuidJ, time.Minute, nil, &win{10, day}),
	}
	ch := mustPick(t, []CellInput{ip, mp, jp}, files, PickOptions{Scope: "perso"})
	if got := rankingLabels(ch); got != "m=stale j=no-reading i=no-reading" { // j: known 7-day reset first
		t.Fatalf("ranking = %q", got)
	}
	delete(files, fileOf(mp))
	files[fileOf(mp)] = noCacheCell(uuidM)
	ch = mustPick(t, []CellInput{ip, mp, jp}, files, PickOptions{Scope: "perso"})
	if ch.Group != "no-reading" || len(ch.Warnings) != 1 || !strings.Contains(ch.Warnings[0], "no usage reading") {
		t.Fatalf("pick = %s/%s warnings %q, want no-reading with a warning", ch.Label, ch.Group, ch.Warnings)
	}
}

// A4/D9: all over the margin → the account that frees up first, with a warning.
func TestPickAllOverMarginEarliestRelease(t *testing.T) {
	ip, mp := rankedCell("ip", "perso", "i", 0), rankedCell("mp", "perso", "m", 1)
	files := map[string][]byte{
		fileOf(ip): usageFile(uuidI, time.Minute, &win{95, 3 * hour}, &win{50, day}),
		fileOf(mp): usageFile(uuidM, time.Minute, &win{92, 1 * hour}, &win{50, 5 * day}),
	}
	ch := mustPick(t, []CellInput{ip, mp}, files, PickOptions{Scope: "perso"})
	if ch.Label != "m" || ch.Group != "over-margin" {
		t.Fatalf("pick = %s/%s, want m (frees up in 1 h)", ch.Label, ch.Group)
	}
	if ch.FreesAt == nil || !ch.FreesAt.Equal(pickNow.Add(hour)) {
		t.Fatalf("FreesAt = %v, want +1h", ch.FreesAt)
	}
	if len(ch.Warnings) != 1 {
		t.Fatalf("warnings = %q, want one", ch.Warnings)
	}
	w := ch.Warnings[0]
	for _, want := range []string{"every account of scope perso", "over the margin", "m, whose limits free up first"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning %q lacks %q", w, want)
		}
	}
}

// A4/D9: full on 7 days but free on 5 h frees up at its 7-day reset — loses.
func TestPickAllOverMarginSevenDayFullLoses(t *testing.T) {
	a, b := rankedCell("ap", "perso", "a", 0), rankedCell("bp", "perso", "b", 1)
	files := map[string][]byte{
		fileOf(a): usageFile(uuidI, time.Minute, &win{10, hour}, &win{100, 3 * day}),
		fileOf(b): usageFile(uuidJ, time.Minute, &win{95, 2 * hour}, &win{50, 4 * day}),
	}
	ch := mustPick(t, []CellInput{a, b}, files, PickOptions{Scope: "perso"})
	if ch.Label != "b" {
		t.Fatalf("pick = %s, want b (frees up in 2 h; a only in 3 days)", ch.Label)
	}
	if fa := ch.Ranking[1].FreesAt; ch.Ranking[1].Label != "a" || fa == nil || !fa.Equal(pickNow.Add(3*day)) {
		t.Fatalf("a FreesAt = %v, want +3d", fa)
	}
}

// A4/D9: both windows blocking → the later of the two resets.
func TestPickAllOverMarginBothBlocking(t *testing.T) {
	a := rankedCell("ap", "perso", "a", 0)
	files := map[string][]byte{fileOf(a): usageFile(uuidI, time.Minute, &win{91, 2 * hour}, &win{96, day})}
	ch := mustPick(t, []CellInput{a}, files, PickOptions{Scope: "perso"})
	if ch.FreesAt == nil || !ch.FreesAt.Equal(pickNow.Add(day)) {
		t.Fatalf("FreesAt = %v, want +1d (the later of +2h and +1d)", ch.FreesAt)
	}
	files[fileOf(a)] = usageFile(uuidI, time.Minute, &win{91, 2 * day}, &win{96, day})
	ch = mustPick(t, []CellInput{a}, files, PickOptions{Scope: "perso"})
	if ch.FreesAt == nil || !ch.FreesAt.Equal(pickNow.Add(2*day)) {
		t.Fatalf("FreesAt = %v, want +2d", ch.FreesAt)
	}
}

// A4/D9: a blocking window with no reset → free-up unknown, after the known.
func TestPickAllOverMarginUnknownReleaseLast(t *testing.T) {
	a, b := rankedCell("ap", "perso", "a", 0), rankedCell("bp", "perso", "b", 1)
	files := map[string][]byte{
		fileOf(a): usageFile(uuidI, time.Minute, &win{99, 0}, &win{10, day}),
		fileOf(b): usageFile(uuidJ, time.Minute, &win{10, hour}, &win{99, 6 * day}),
	}
	ch := mustPick(t, []CellInput{a, b}, files, PickOptions{Scope: "perso"})
	if ch.Label != "b" || ch.Ranking[1].FreesAt != nil {
		t.Fatalf("ranking = %s, a FreesAt %v — want b first, a unknown", rankingLabels(ch), ch.Ranking[1].FreesAt)
	}
	// Only unknown → its warning says so.
	ch = mustPick(t, []CellInput{a}, files, PickOptions{Scope: "perso"})
	if len(ch.Warnings) != 1 || !strings.Contains(ch.Warnings[0], "(free-up time unknown)") {
		t.Fatalf("warnings = %q", ch.Warnings)
	}
}

// A5: the scope's cells only, logged-in only; unreadable cells are no candidates.
func TestPickScopeAndAuth(t *testing.T) {
	ip, it := rankedCell("ip", "perso", "i", 0), rankedCell("it", "gx", "i", 1)
	jp, jt := rankedCell("jp", "perso", "j", 2), rankedCell("jt", "gx", "j", 3)
	mp := rankedCell("mp", "perso", "m", 4)
	files := map[string][]byte{
		fileOf(ip): usageFile(uuidI, time.Minute, &win{10, hour}, &win{10, day}),
		fileOf(it): usageFile(uuidI, time.Minute, &win{10, hour}, &win{10, day}),
		fileOf(jp): []byte(`{"numStartups": 3}`), // not logged in
		fileOf(jt): usageFile(uuidJ, time.Minute, &win{10, hour}, &win{10, 2 * hour}),
		// mp: no file at all → unreadable
	}
	cells := []CellInput{ip, it, jp, jt, mp}
	ch := mustPick(t, cells, files, PickOptions{Scope: "globex"})
	if ch.Label != "j" || ch.Cell.ID != "jt" {
		t.Fatalf("globex pick = %s/%s, want j on jt", ch.Label, ch.Cell.ID)
	}
	for _, c := range ch.Ranking {
		if c.Cell.Scope != "gx" {
			t.Errorf("globex ranking holds cell %s of scope %s", c.Cell.ID, c.Cell.Scope)
		}
	}
	ch = mustPick(t, cells, files, PickOptions{Scope: "perso"})
	if got := rankingLabels(ch); got != "i=fresh" {
		t.Fatalf("perso ranking = %q, want i alone (jp logged out, mp unreadable)", got)
	}
}

// A5: an account's reading may come from a cell of another scope; its label
// never names a cell (D19).
func TestPickReadingFromOtherScopeCell(t *testing.T) {
	mp, mt := rankedCell("mp", "perso", "m", 0), rankedCell("mt", "gx", "m", 1)
	files := map[string][]byte{
		fileOf(mp): noCacheCell(uuidM),
		fileOf(mt): usageFile(uuidM, time.Minute, &win{10, hour}, &win{42, day}),
	}
	ch := mustPick(t, []CellInput{mp, mt}, files, PickOptions{Scope: "perso"})
	if ch.Cell.ID != "mp" || ch.Group != "fresh" || ch.SevenDay == nil || ch.SevenDay.Percent != 42 {
		t.Fatalf("pick = %s/%s 7d %+v, want mp fresh with mt's 42 %%", ch.Cell.ID, ch.Group, ch.SevenDay)
	}
	// No letter anywhere: a jp cell and an mt cell of another scope.
	jp, xt := rankedCell("jp", "perso", "", 0), rankedCell("mt", "gx", "", 1)
	files = map[string][]byte{
		fileOf(jp): usageFile(uuidJ, time.Minute, &win{10, hour}, &win{10, day}),
		fileOf(xt): usageFile(uuidJ, 2*time.Minute, &win{10, hour}, &win{10, day}),
	}
	for _, sc := range []string{"perso", "globex"} {
		ch = mustPick(t, []CellInput{jp, xt}, files, PickOptions{Scope: scope.Scope(sc)})
		if ch.Label != "other-33333333" || strings.Contains(ch.Label, "jp") || strings.Contains(ch.Label, "mt") || utf8.RuneCountInString(ch.Label) > 14 {
			t.Fatalf("%s: label = %q, want other-33333333", sc, ch.Label)
		}
	}
}

// A6: cap (extra credits spent) excludes nothing.
func TestPickIgnoresCap(t *testing.T) {
	tc := rankedCell("t", "gx", "t", 1)
	jt := rankedCell("jt", "gx", "j", 0)
	// The team reading (org_spend_cap_reached), moved to pickNow: fresh,
	// 7 d at 1 % renewing in a day.
	team := strings.NewReplacer(
		`"utilization": 69`, `"utilization": 1`,
		"1791192360000", fmt.Sprint(pickNow.Add(-time.Minute).UnixMilli()),
		"2026-10-05T14:20:00.324313+00:00", pickNow.Add(hour).Format(time.RFC3339),
		"2026-10-10T19:00:00.324333+00:00", pickNow.Add(day).Format(time.RFC3339),
	).Replace(string(teamCell(t)))
	files := map[string][]byte{
		fileOf(tc): []byte(team),
		fileOf(jt): usageFile(uuidJ, time.Minute, &win{10, hour}, &win{10, 3 * day}),
	}
	ch := mustPick(t, []CellInput{tc, jt}, files, PickOptions{Scope: "globex"})
	snaps := snapshots([]CellInput{tc}, files)
	if !snaps[0].CapReached {
		t.Fatal("fixture: team cell should have cap reached")
	}
	if ch.Label != "t" {
		t.Fatalf("pick = %s (ranking %s), want t (cap reached, 7d at 1 %%, soonest reset)", ch.Label, rankingLabels(ch))
	}
}

// A7: an account limited until a future instant is over the margin; once that
// instant has passed, it is a candidate again.
func TestPickLimitedUntil(t *testing.T) {
	ip, mp := rankedCell("ip", "perso", "i", 0), rankedCell("mp", "perso", "m", 1)
	files := map[string][]byte{
		fileOf(ip): usageFile(uuidI, time.Minute, &win{10, hour}, &win{10, day}),
		fileOf(mp): usageFile(uuidM, time.Minute, &win{10, hour}, &win{10, 3 * day}),
	}
	ch := mustPick(t, []CellInput{ip, mp}, files, PickOptions{Scope: "perso", Limited: map[string]time.Time{uuidI: pickNow.Add(hour)}})
	if got := rankingLabels(ch); got != "m=fresh i=over-margin" {
		t.Fatalf("ranking = %q, want i excluded", got)
	}
	if lu := ch.Ranking[1].LimitedUntil; lu == nil || !lu.Equal(pickNow.Add(hour)) {
		t.Fatalf("LimitedUntil = %v", lu)
	}
	ch = mustPick(t, []CellInput{ip, mp}, files, PickOptions{Scope: "perso", Limited: map[string]time.Time{uuidI: pickNow.Add(-time.Minute)}})
	if ch.Label != "i" {
		t.Fatalf("pick = %s, want i (limit over)", ch.Label)
	}
	// Limited alone: frees up at until.
	ch = mustPick(t, []CellInput{ip}, files, PickOptions{Scope: "perso", Limited: map[string]time.Time{uuidI: pickNow.Add(2 * hour)}})
	if ch.FreesAt == nil || !ch.FreesAt.Equal(pickNow.Add(2*hour)) {
		t.Fatalf("FreesAt = %v, want until", ch.FreesAt)
	}
}

// A8: no candidate → ErrNoCandidate naming the scope and each cell's reason.
func TestPickNoCandidate(t *testing.T) {
	ip, jp, it := rankedCell("ip", "perso", "i", 0), rankedCell("jp", "perso", "j", 1), rankedCell("it", "gx", "i", 2)
	files := map[string][]byte{
		fileOf(jp): []byte(`{}`),
		fileOf(it): usageFile(uuidI, time.Minute, &win{10, hour}, &win{10, day}),
	}
	_, err := Pick(snapshots([]CellInput{ip, jp, it}, files), PickOptions{Scope: "perso"}, pickNow)
	if !errors.Is(err, ErrNoCandidate) {
		t.Fatalf("err = %v, want ErrNoCandidate", err)
	}
	msg := err.Error()
	for _, want := range []string{"scope perso", "cell jp: not logged in", "cell ip: read /cells/ip/.claude.json"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "cell it") {
		t.Errorf("error %q names a cell of another scope", msg)
	}
	_, err = Pick(nil, PickOptions{Scope: "globex"}, pickNow)
	if !errors.Is(err, ErrNoCandidate) || !strings.Contains(err.Error(), "scope globex") {
		t.Fatalf("empty: err = %v", err)
	}
}

// A at 60 % mid-week (budget 70: allowed 40) renews first; B at 30 % does not.
func TestPickBudgetedSkipsOverBudgetAccount(t *testing.T) {
	a, b := rankedCell("mp", "perso", "m", 0), rankedCell("ip", "perso", "i", 1)
	files := map[string][]byte{
		fileOf(a): usageFile(uuidM, time.Minute, &win{10, 2 * hour}, &win{60, midWeek}),
		fileOf(b): usageFile(uuidI, time.Minute, &win{10, 2 * hour}, &win{30, 4 * day}),
	}
	ch := mustPick(t, []CellInput{a, b}, files, PickOptions{Scope: "perso", Budgeted: true})
	if ch.Label != "i" || ch.Group != GroupFresh || ch.Budget == nil || ch.Budget.State != BudgetWithin {
		t.Fatalf("budgeted pick: %+v", ch.Candidate)
	}
	over := ch.Ranking[1]
	if over.Label != "m" || over.Group != GroupOverMargin || over.FreesAt == nil || over.Budget == nil || over.Budget.State != BudgetOver {
		t.Fatalf("over-budget candidate: %+v", over)
	}
	if !over.FreesAt.Equal(*over.Budget.FreesAt) {
		t.Fatalf("FreesAt %v, budget %v", over.FreesAt, over.Budget.FreesAt)
	}
	ch = mustPick(t, []CellInput{a, b}, files, PickOptions{Scope: "perso"})
	if ch.Label != "m" || ch.Group != GroupFresh || ch.Budget != nil {
		t.Fatalf("unbudgeted pick moved: %+v", ch.Candidate)
	}
}

func TestPickBudgetedAllOverIsOverMargin(t *testing.T) {
	a, b := rankedCell("mp", "perso", "m", 0), rankedCell("ip", "perso", "i", 1)
	files := map[string][]byte{
		fileOf(a): usageFile(uuidM, time.Minute, &win{10, 2 * hour}, &win{60, midWeek}),
		fileOf(b): usageFile(uuidI, time.Minute, &win{10, 2 * hour}, &win{50, midWeek}),
	}
	ch := mustPick(t, []CellInput{a, b}, files, PickOptions{Scope: "perso", Budgeted: true})
	if ch.Group != GroupOverMargin || ch.Label != "i" || ch.FreesAt == nil {
		t.Fatalf("%+v", ch.Candidate)
	}
	for _, c := range ch.Ranking[1:] {
		if c.FreesAt == nil || c.FreesAt.Before(*ch.FreesAt) {
			t.Fatalf("not the nearest release: %v then %v", ch.FreesAt, c.FreesAt)
		}
	}
}

// The block of a window past the ceiling and the budget's add up: the latest wins.
func TestPickBudgetedFreesAtIsTheLatestBlock(t *testing.T) {
	a := rankedCell("mp", "perso", "m", 0)
	files := map[string][]byte{fileOf(a): usageFile(uuidM, time.Minute, &win{95, hour}, &win{50, midWeek})}
	ch := mustPick(t, []CellInput{a}, files, PickOptions{Scope: "perso", Budgeted: true})
	if ch.Group != GroupOverMargin || ch.FreesAt == nil || !ch.FreesAt.Equal(*ch.Budget.FreesAt) || !ch.FreesAt.After(pickNow.Add(hour)) {
		t.Fatalf("%+v", ch.Candidate)
	}
}

func TestPickBudgetedUnmeasuredBlocks(t *testing.T) {
	a := rankedCell("mp", "perso", "m", 0)
	stale := map[string][]byte{fileOf(a): usageFile(uuidM, 31*time.Minute, &win{10, 2 * hour}, &win{1, midWeek})}
	none := map[string][]byte{fileOf(a): []byte(fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q}}`, uuidM))}
	for name, files := range map[string]map[string][]byte{"stale": stale, "absent": none} {
		ch := mustPick(t, []CellInput{a}, files, PickOptions{Scope: "perso", Budgeted: true})
		if ch.Group != GroupOverMargin || ch.FreesAt != nil || ch.Budget == nil || ch.Budget.State != BudgetUnmeasured {
			t.Fatalf("%s: %+v", name, ch.Candidate)
		}
	}
}

func TestPickUnbudgetedUnchanged(t *testing.T) {
	ip, mp, jp := rankedCell("ip", "perso", "i", 0), rankedCell("mp", "perso", "m", 1), rankedCell("jp", "perso", "j", 2)
	files := map[string][]byte{
		fileOf(ip): usageFile(uuidI, time.Minute, &win{10, 2 * hour}, &win{40, 5 * day}),
		fileOf(mp): usageFile(uuidM, 31*time.Minute, &win{10, 2 * hour}, &win{80, 1 * day}),
		fileOf(jp): usageFile(uuidJ, time.Minute, &win{95, 2 * hour}, &win{20, 3 * day}),
	}
	ch := mustPick(t, []CellInput{ip, mp, jp}, files, PickOptions{Scope: "perso"})
	if got := rankingLabels(ch); got != "i=fresh m=stale j=over-margin" {
		t.Fatalf("ranking: %s", got)
	}
	b, err := json.Marshal(ch)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != unbudgetedPickJSON {
		t.Fatalf("not byte for byte:\n got %s\nwant %s", b, unbudgetedPickJSON)
	}
}

// unbudgetedPickJSON is this fixture's Choice as marshalled by the code before
// the weekly budget (2155ba32), at the fixed pickNow. A difference is fixed in
// the code, never here (D6).
const unbudgetedPickJSON = `{"label":"i","account_uuid":"22222222-2222-4222-8222-222222222222","cell":{"ID":"ip","ConfigDir":"/cells/ip","Scope":"perso","Account":"i","Rank":0},"group":"fresh","five_hour":{"percent":10,"raw_percent":10,"resets_at":"2026-10-20T11:00:00Z","reset_passed":false},"seven_day":{"percent":40,"raw_percent":40,"resets_at":"2026-10-25T09:00:00Z","reset_passed":false},"age_ns":60000000000,"thresholds":{"select_pct":90,"switch_pct":98},"ranking":[{"label":"i","account_uuid":"22222222-2222-4222-8222-222222222222","cell":{"ID":"ip","ConfigDir":"/cells/ip","Scope":"perso","Account":"i","Rank":0},"group":"fresh","five_hour":{"percent":10,"raw_percent":10,"resets_at":"2026-10-20T11:00:00Z","reset_passed":false},"seven_day":{"percent":40,"raw_percent":40,"resets_at":"2026-10-25T09:00:00Z","reset_passed":false},"age_ns":60000000000,"thresholds":{"select_pct":90,"switch_pct":98}},{"label":"m","account_uuid":"11111111-1111-4111-8111-111111111111","cell":{"ID":"mp","ConfigDir":"/cells/mp","Scope":"perso","Account":"m","Rank":1},"group":"stale","five_hour":{"percent":10,"raw_percent":10,"resets_at":"2026-10-20T11:00:00Z","reset_passed":false},"seven_day":{"percent":80,"raw_percent":80,"resets_at":"2026-10-21T09:00:00Z","reset_passed":false},"age_ns":1860000000000,"thresholds":{"select_pct":90,"switch_pct":98}},{"label":"j","account_uuid":"33333333-3333-4333-8333-333333333333","cell":{"ID":"jp","ConfigDir":"/cells/jp","Scope":"perso","Account":"j","Rank":2},"group":"over-margin","five_hour":{"percent":95,"raw_percent":95,"resets_at":"2026-10-20T11:00:00Z","reset_passed":false},"seven_day":{"percent":20,"raw_percent":20,"resets_at":"2026-10-23T09:00:00Z","reset_passed":false},"age_ns":60000000000,"frees_at":"2026-10-20T11:00:00Z","thresholds":{"select_pct":90,"switch_pct":98}}],"warnings":[]}`

// A reading without a 5-hour window cannot say the account is under its
// ceiling: under Budgeted it blocks, its release unknown. Without Budgeted it
// stays where it always was (D6).
func TestPickBudgetedNoFiveHourBlocks(t *testing.T) {
	jp := rankedCell("jp", "perso", "j", 0)
	files := map[string][]byte{fileOf(jp): usageFile(uuidJ, time.Minute, nil, &win{10, 1 * day})}
	ch := mustPick(t, []CellInput{jp}, files, PickOptions{Scope: "perso", Budgeted: true})
	if ch.Group != GroupOverMargin || ch.FreesAt != nil || ch.Budget == nil || ch.Budget.State != BudgetUnmeasured {
		t.Fatalf("budgeted: %+v", ch.Candidate)
	}
	if plain := mustPick(t, []CellInput{jp}, files, PickOptions{Scope: "perso"}); plain.Group != GroupNoReading || plain.Budget != nil {
		t.Fatalf("unbudgeted: %+v", plain.Candidate)
	}
}

// A 5-hour window that has not started: an object, percent 0, a null reset.
func TestPickBudgetedFiveHourNotStartedChoosable(t *testing.T) {
	jp := rankedCell("jp", "perso", "j", 0)
	files := map[string][]byte{fileOf(jp): usageFile(uuidJ, time.Minute, &win{0, 0}, &win{10, 4 * day})}
	ch := mustPick(t, []CellInput{jp}, files, PickOptions{Scope: "perso", Budgeted: true})
	if ch.Group != GroupFresh || ch.Budget == nil || ch.Budget.State != BudgetWithin {
		t.Fatalf("%+v", ch.Candidate)
	}
}
