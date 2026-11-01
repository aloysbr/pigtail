package daemon

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/daemon/usagehistory"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

var ovNow = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

const (
	uuidOvM = "11111111-1111-4111-8111-111111111111"
	uuidOvI = "22222222-2222-4222-8222-222222222222"
	uuidOvJ = "33333333-3333-4333-8333-333333333333"
	uuidOvT = "44444444-4444-4444-8444-444444444444"
	uuidOvX = "55555555-5555-4555-8555-555555555555"
)

// ovCell is one cell of an overview fixture and its .claude.json.
type ovCell struct {
	id, scope, letter, uuid string
	rank                    int
	noReading               bool
	fetchedAgo              time.Duration
	p7, p5                  float64
	r7, r5                  time.Time
	cap                     bool
	fable                   *float64
	selectPct               *float64 // claude-cells.yaml select_pct
}

func (c ovCell) input() claudeusage.CellInput {
	r := c.rank
	return claudeusage.CellInput{ID: c.id, ConfigDir: "/cells/" + c.id, Scope: c.scope, Account: c.letter, Rank: &r, SelectPct: c.selectPct}
}

func (c ovCell) file(t *testing.T) []byte {
	t.Helper()
	f := map[string]any{"oauthAccount": map[string]any{"accountUuid": c.uuid,
		"organizationRateLimitTier": "default_claude_max_20x", "userRateLimitTier": nil}}
	if !c.noReading {
		win := func(p float64, r time.Time) map[string]any {
			return map[string]any{"utilization": p, "resets_at": r.Format(time.RFC3339Nano)}
		}
		util := map[string]any{"seven_day": win(c.p7, c.r7), "five_hour": win(c.p5, c.r5), "limits": []any{}}
		if c.fable != nil {
			util["limits"] = []any{map[string]any{"kind": "weekly_scoped", "percent": *c.fable, "severity": "normal",
				"resets_at": c.r7.Format(time.RFC3339Nano), "scope": map[string]any{"model": map[string]any{"display_name": "Fable"}}}}
		}
		f["cachedUsageUtilization"] = map[string]any{"fetchedAtMs": ovNow.Add(-c.fetchedAgo).UnixMilli(),
			"accountUuid": c.uuid, "utilization": util}
	}
	if c.cap {
		f["cachedExtraUsageDisabledReason"] = "org_spend_cap_reached"
	}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ovSource is a source over the given cells, with an empty history and state
// dir unless the test sets them.
func ovSource(t *testing.T, cells []ovCell) *usageOverviewSource {
	t.Helper()
	files := map[string][]byte{}
	var inputs []claudeusage.CellInput
	for _, c := range cells {
		in := c.input()
		inputs = append(inputs, in)
		files[filepath.Join(in.ConfigDir, claudeusage.FileName)] = c.file(t)
	}
	return &usageOverviewSource{
		loadCells: func() ([]claudeusage.CellInput, error) { return inputs, nil },
		readFile: func(p string) ([]byte, error) {
			if b, ok := files[p]; ok {
				return b, nil
			}
			return nil, os.ErrNotExist
		},
		stateDir: t.TempDir(),
	}
}

// fourAccounts: m (mp perso, mt work), i (ip perso, it work), j (jp perso),
// t (tt work only, credits cap reached). Ranked m, i, j, t.
func fourAccounts() []ovCell {
	r7 := ovNow.Add(3 * 24 * time.Hour)
	r5 := ovNow.Add(2 * time.Hour)
	return []ovCell{
		{id: "zq-mp", scope: "perso", letter: "m", uuid: uuidOvM, rank: 0, fetchedAgo: 5 * time.Minute, p7: 40, p5: 10, r7: r7, r5: r5},
		{id: "zq-mt", scope: "gx", letter: "m", uuid: uuidOvM, rank: 1, fetchedAgo: 2 * time.Hour, p7: 30, p5: 5, r7: r7, r5: r5},
		{id: "zq-ip", scope: "perso", letter: "i", uuid: uuidOvI, rank: 2, fetchedAgo: 10 * time.Minute, p7: 20, p5: 0, r7: r7.Add(24 * time.Hour), r5: r5},
		{id: "zq-it", scope: "gx", letter: "i", uuid: uuidOvI, rank: 3, fetchedAgo: time.Hour, p7: 10, p5: 0, r7: r7.Add(24 * time.Hour), r5: r5},
		{id: "zq-jp", scope: "perso", letter: "j", uuid: uuidOvJ, rank: 4, fetchedAgo: 15 * time.Minute, p7: 60, p5: 20, r7: r7.Add(48 * time.Hour), r5: r5},
		{id: "zq-tt", scope: "gx", letter: "t", uuid: uuidOvT, rank: 5, fetchedAgo: 20 * time.Minute, p7: 5, p5: 1, r7: r7, r5: r5, cap: true},
	}
}

func overview(t *testing.T, src *usageOverviewSource) pigtail.UsageOverview {
	t.Helper()
	ov, err := src.Overview(ovNow)
	if err != nil {
		t.Fatal(err)
	}
	return ov
}

func ovAccount(t *testing.T, ov pigtail.UsageOverview, label string) pigtail.UsageAccount {
	t.Helper()
	for _, a := range ov.Accounts {
		if a.Label == label {
			return a
		}
	}
	t.Fatalf("no account %q in %+v", label, ov.Accounts)
	return pigtail.UsageAccount{}
}

func TestUsageOverviewAccountsInOrder(t *testing.T) {
	ov := overview(t, ovSource(t, fourAccounts()))
	var labels []string
	for _, a := range ov.Accounts {
		labels = append(labels, a.Label)
	}
	if got := strings.Join(labels, " "); got != "m i j t" {
		t.Fatalf("labels = %q, want \"m i j t\"", got)
	}
	if !ov.GeneratedAt.Equal(ovNow) || ov.Verdict == nil {
		t.Fatalf("generated_at %v, verdict %+v (set since lot 2)", ov.GeneratedAt, ov.Verdict)
	}
	m := ovAccount(t, ov, "m")
	if m.AccountUUID != uuidOvM || !m.InVerdict || m.Unusable != "" || m.FetchedAt == nil ||
		!m.FetchedAt.Equal(ovNow.Add(-5*time.Minute).Truncate(time.Millisecond)) || m.SevenDay == nil || m.SevenDay.Percent != 40 {
		t.Fatalf("m = %+v", m)
	}
	tt := ovAccount(t, ov, "t")
	if tt.Unusable != "cap" || tt.InVerdict {
		t.Fatalf("t = %+v, want unusable cap, out of the verdict", tt)
	}
}

func TestUsageOverviewNoReadingIsUnusable(t *testing.T) {
	cells := fourAccounts()[:1]
	cells = append(cells, ovCell{id: "zq-np", scope: "perso", letter: "n", uuid: uuidOvX, rank: 9, noReading: true, cap: true})
	n := ovAccount(t, overview(t, ovSource(t, cells)), "n")
	if n.Unusable != "no-reading" || n.InVerdict || n.SevenDay != nil || n.FetchedAt != nil {
		t.Fatalf("n = %+v, want no-reading first, out of the verdict", n)
	}
}

func TestUsageOverviewFullFlags(t *testing.T) {
	c := fourAccounts()[0]
	c.p5, c.p7 = 90, 90
	m := ovAccount(t, overview(t, ovSource(t, []ovCell{c})), "m")
	if !m.FiveHour.Full || !m.SevenDay.Full {
		t.Fatalf("defaults: 5 h 90 / 7 d 90 = %+v / %+v, want full", m.FiveHour, m.SevenDay)
	}
	c.p5, c.p7 = 89.9, 89.9
	m = ovAccount(t, overview(t, ovSource(t, []ovCell{c})), "m")
	if m.FiveHour.Full || m.SevenDay.Full {
		t.Fatalf("defaults: 5 h 89.9 / 7 d 89.9 = %+v / %+v, want not full", m.FiveHour, m.SevenDay)
	}
	// An account declaring select_pct: 70 in the cell table is full at 70.
	ceil := 70.0
	c70 := c
	c70.selectPct = &ceil
	c70.p5, c70.p7 = 70, 70
	m = ovAccount(t, overview(t, ovSource(t, []ovCell{c70})), "m")
	if !m.FiveHour.Full || !m.SevenDay.Full {
		t.Fatalf("ceiling 70: 5 h 70 / 7 d 70 = %+v / %+v, want full", m.FiveHour, m.SevenDay)
	}
	c70.p5, c70.p7 = 69.9, 69.9
	m = ovAccount(t, overview(t, ovSource(t, []ovCell{c70})), "m")
	if m.FiveHour.Full || m.SevenDay.Full {
		t.Fatalf("ceiling 70: 5 h 69.9 / 7 d 69.9 = %+v / %+v, want not full", m.FiveHour, m.SevenDay)
	}
	c.p5, c.r5 = 99, ovNow.Add(-time.Minute) // reset passed
	m = ovAccount(t, overview(t, ovSource(t, []ovCell{c})), "m")
	if m.FiveHour.Percent != 0 || m.FiveHour.Full {
		t.Fatalf("passed 5 h = %+v, want 0 %% and not full", m.FiveHour)
	}
	fable := 90.0
	c.fable = &fable
	m = ovAccount(t, overview(t, ovSource(t, []ovCell{c})), "m")
	if len(m.Scoped) != 1 || m.Scoped[0].Model != "Fable" || m.Scoped[0].Percent != 90 || !m.Scoped[0].Full {
		t.Fatalf("scoped = %+v, want Fable 90 full", m.Scoped)
	}
}

func TestUsageOverviewWindowLeftPassedReset(t *testing.T) {
	c := fourAccounts()[0]
	c.r7 = ovNow.Add(-10 * time.Minute)
	m := ovAccount(t, overview(t, ovSource(t, []ovCell{c})), "m")
	if m.WindowLeft == nil || math.Abs(*m.WindowLeft-(1-10.0/(7*24*60))) > 1e-9 {
		t.Fatalf("window_left = %v, want ≈ 0.999", m.WindowLeft)
	}
	if m.SevenDay.Percent != 0 {
		t.Fatalf("passed 7 d shown %d %%", m.SevenDay.Percent)
	}
}

func TestUsageOverviewFast(t *testing.T) {
	week := 7 * 24 * time.Hour
	c := fourAccounts()[0]
	c.p7, c.r7 = 64, ovNow.Add(time.Duration(0.643*float64(week))) // dispo 36, part 0.643
	m := ovAccount(t, overview(t, ovSource(t, []ovCell{c})), "m")
	if !m.Fast || m.WindowLeft == nil || math.Abs(*m.WindowLeft-0.643) > 1e-6 {
		t.Fatalf("dispo 36 / 0.643 = fast %v left %v, want fast", m.Fast, m.WindowLeft)
	}
	c.p7, c.r7 = 20, ovNow.Add(time.Duration(0.696*float64(week))) // dispo 80, part 0.696
	m = ovAccount(t, overview(t, ovSource(t, []ovCell{c})), "m")
	if m.Fast {
		t.Fatalf("dispo 80 / 0.696 = fast, want not")
	}
}

func TestUsageOverviewRefreshFailed(t *testing.T) {
	src := ovSource(t, fourAccounts())
	st := claudeusage.BackgroundState{At: ovNow, Results: []claudeusage.BackgroundResult{
		{Label: "j", AccountUUID: uuidOvJ, Status: claudeusage.RefreshFailed, Error: "claude exited 1"},
		{Label: "i", AccountUUID: uuidOvI, Status: claudeusage.RefreshOK},
	}}
	b, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(src.stateDir, claudeusage.BackgroundStateName), b, 0o600); err != nil {
		t.Fatal(err)
	}
	ov := overview(t, src)
	if !ovAccount(t, ov, "j").RefreshFailed || ovAccount(t, ov, "i").RefreshFailed || ovAccount(t, ov, "m").RefreshFailed {
		t.Fatalf("refresh_failed wrong: %+v", ov.Accounts)
	}
	// No state file: no failure.
	src.stateDir = t.TempDir()
	if ovAccount(t, overview(t, src), "j").RefreshFailed {
		t.Fatal("refresh failed without a state file")
	}
}

func snapsOf(t *testing.T, src *usageOverviewSource) []claudeusage.Snapshot {
	t.Helper()
	cells, err := src.loadCells()
	if err != nil {
		t.Fatal(err)
	}
	return claudeusage.ReadTable(cells, src.readFile)
}

func TestUsageOverviewNextSessionMatchesPick(t *testing.T) {
	src := ovSource(t, fourAccounts())
	ov := overview(t, src)
	names := scope.Active().Names()
	if len(ov.NextSession) != len(names) {
		t.Fatalf("next_session = %+v, want one per scope %v", ov.NextSession, names)
	}
	for i, p := range ov.NextSession {
		if p.Scope != string(names[i]) {
			t.Fatalf("next_session[%d].scope = %q, want %q (table order)", i, p.Scope, names[i])
		}
		ch, err := claudeusage.Pick(snapsOf(t, src), claudeusage.PickOptions{Scope: names[i]}, ovNow)
		if err != nil {
			t.Fatal(err)
		}
		if p.AccountUUID != ch.AccountUUID || p.Error != "" || p.Master != scope.SeesAll(names[i]) {
			t.Fatalf("%s: %+v, Pick chose %s", p.Scope, p, ch.AccountUUID)
		}
	}
	// A scope without any cell: no candidate, said without naming anything.
	ov = overview(t, ovSource(t, fourAccounts()[4:5])) // jp only: no work cell
	for _, p := range ov.NextSession {
		if !scope.SeesAll(scope.Scope(p.Scope)) && (p.Error != "no-candidate" || p.AccountUUID != "") {
			t.Fatalf("%s = %+v, want no-candidate", p.Scope, p)
		}
	}
}

func TestUsageOverviewNextSessionDisplay(t *testing.T) {
	display := func(ov pigtail.UsageOverview) map[string]string {
		out := map[string]string{}
		for _, p := range ov.NextSession {
			out[p.Scope] = p.Display
		}
		return out
	}
	got := display(overview(t, ovSource(t, fourAccounts())))
	if got["globex"] != "gx" || got["perso"] != "perso" {
		t.Fatalf("display = %v, want work for globex, perso for perso", got)
	}
	tbl, err := scope.ParseTable([]byte(`default: globex
scopes:
  perso: {vault_roots: [Perso/], sees_all: true}
  globex: {vault_roots: [Globex/], aliases: [ts]}
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scope.Use(tbl))
	cells := fourAccounts()
	for i := range cells {
		if cells[i].scope == "gx" {
			cells[i].scope = "ts"
		}
	}
	got = display(overview(t, ovSource(t, cells)))
	if got["globex"] != "ts" || got["perso"] != "perso" {
		t.Fatalf("display = %v, want the table's alias ts", got)
	}
}

func TestUsageOverviewNextSessionNeverAlias(t *testing.T) {
	for _, p := range overview(t, ovSource(t, fourAccounts())).NextSession {
		for _, a := range scope.Active().Aliases(scope.Scope(p.Scope)) {
			if p.Scope == a {
				t.Fatalf("scope %q is an alias", p.Scope)
			}
		}
		if p.Scope == "gx" {
			t.Fatalf("scope = work, the alias")
		}
	}
}

func TestUsageOverviewNamesNoCell(t *testing.T) {
	cells := fourAccounts()
	// An account whose only cell carries no letter: Build would label it with
	// the cell id; the route says other-<short uuid>.
	cells = append(cells, ovCell{id: "zq-anon", scope: "perso", uuid: uuidOvX, rank: 8, fetchedAgo: time.Minute, p7: 1, p5: 1,
		r7: ovNow.Add(time.Hour), r5: ovNow.Add(time.Hour)})
	ov := overview(t, ovSource(t, cells))
	b, err := json.Marshal(ov)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cells {
		if strings.Contains(string(b), c.id) {
			t.Fatalf("the overview names cell %s: %s", c.id, b)
		}
	}
	if strings.Contains(string(b), `"cell`) {
		t.Fatalf("the overview carries a cell field: %s", b)
	}
	if a := ovAccount(t, ov, "other-"+uuidOvX[:8]); a.AccountUUID != uuidOvX {
		t.Fatalf("anon = %+v", a)
	}
}

// I11: next_session designates its account by uuid, never by label. The work
// pick lands on an account lettered on its perso cell only: Pick labels it
// other-…, accounts[] gives its letter.
func TestUsageOverviewNextSessionDesignatesAccountByUUID(t *testing.T) {
	r := ovNow.Add(48 * time.Hour)
	cells := []ovCell{
		{id: "zq-xp", scope: "perso", letter: "x", uuid: uuidOvX, rank: 0, fetchedAgo: time.Minute, p7: 10, p5: 1, r7: r, r5: r, cap: true},
		{id: "zq-xt", scope: "gx", uuid: uuidOvX, rank: 1, fetchedAgo: time.Minute, p7: 10, p5: 1, r7: r, r5: r, cap: true},
		{id: "zq-jp", scope: "perso", letter: "j", uuid: uuidOvJ, rank: 2, fetchedAgo: time.Minute, p7: 50, p5: 1, r7: r, r5: r},
	}
	src := ovSource(t, cells)
	ov := overview(t, src)
	for _, p := range ov.NextSession {
		if p.Error != "" {
			continue
		}
		n := 0
		for _, a := range ov.Accounts {
			if a.AccountUUID == p.AccountUUID {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%s designates %q, found %d times in accounts[]", p.Scope, p.AccountUUID, n)
		}
	}
	var work *pigtail.UsageScopePick
	for i := range ov.NextSession {
		if !ov.NextSession[i].Master {
			work = &ov.NextSession[i]
		}
	}
	if work == nil || work.AccountUUID != uuidOvX {
		t.Fatalf("work pick = %+v, want x by uuid", work)
	}
	x := ovAccount(t, ov, "x")
	if x.AccountUUID != uuidOvX || x.Unusable != "cap" {
		t.Fatalf("x = %+v", x)
	}
	ch, err := claudeusage.Pick(snapsOf(t, src), claudeusage.PickOptions{Scope: scope.Scope(work.Scope)}, ovNow)
	if err != nil || ch.AccountUUID != uuidOvX || ch.Label == "x" {
		t.Fatalf("Pick = %+v, %v; want x's uuid under a label that is not its letter", ch.Candidate, err)
	}
}

func TestUsageOverviewHistorySince(t *testing.T) {
	src := ovSource(t, fourAccounts())
	// nil history → null.
	if a := ovAccount(t, overview(t, src), "m"); a.HistorySince != nil {
		t.Fatalf("history_since without history = %v", a.HistorySince)
	}
	h, err := usagehistory.Open(filepath.Join(t.TempDir(), usagehistory.FileName), ovNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, ago := range []time.Duration{50 * time.Hour, 30 * time.Hour, time.Hour} {
		if err := h.Append([]claudeusage.Sample{{At: ovNow.Add(-ago), AccountUUID: uuidOvM, Label: "m"}}); err != nil {
			t.Fatal(err)
		}
	}
	src.history = h
	ov := overview(t, src)
	if a := ovAccount(t, ov, "m"); a.HistorySince == nil || !a.HistorySince.Equal(ovNow.Add(-50*time.Hour)) {
		t.Fatalf("m history_since = %v, want now-50h", a.HistorySince)
	}
	if a := ovAccount(t, ov, "j"); a.HistorySince != nil {
		t.Fatalf("j history_since = %v, want null", a.HistorySince)
	}
}

func TestUsageOverviewTableError(t *testing.T) {
	src := ovSource(t, nil)
	src.loadCells = func() ([]claudeusage.CellInput, error) { return nil, errors.New("no claude-cells.yaml") }
	if _, err := src.Overview(ovNow); err == nil || !strings.Contains(err.Error(), "claude-cells.yaml") {
		t.Fatalf("err = %v", err)
	}
}

// A cell whose .claude.json cannot be parsed is counted, not dropped, and
// never named (D19).
func TestUsageOverviewCountsUnreadableCells(t *testing.T) {
	cells := fourAccounts()
	src := ovSource(t, cells)
	inputs, _ := src.loadCells()
	bad := claudeusage.CellInput{ID: "zq-broken", ConfigDir: "/cells/zq-broken", Scope: "perso", Account: "x", Rank: new(int)}
	src.loadCells = func() ([]claudeusage.CellInput, error) { return append(inputs, bad), nil }
	inner := src.readFile
	src.readFile = func(p string) ([]byte, error) {
		if p == filepath.Join(bad.ConfigDir, claudeusage.FileName) {
			return []byte("{not json"), nil
		}
		return inner(p)
	}
	ov := overview(t, src)
	if ov.UnreadableCount != 1 {
		t.Fatalf("unreadable_count = %d, want 1", ov.UnreadableCount)
	}
	if len(ov.Accounts) != 4 {
		t.Fatalf("accounts = %d, want 4", len(ov.Accounts))
	}
	b, err := json.Marshal(ov)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"unreadable_count":1`) {
		t.Fatalf("no unreadable_count in %s", b)
	}
	for _, leak := range []string{"zq-broken", "/cells", "not json", "invalid character"} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("the overview leaks %q: %s", leak, b)
		}
	}
}

func TestUsageOverviewNoUnreadableOmitsField(t *testing.T) {
	ov := overview(t, ovSource(t, fourAccounts()))
	b, err := json.Marshal(ov)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "unreadable_count") {
		t.Fatalf("unreadable_count present at 0: %s", b)
	}
}

// verdictOf is the overview's verdict, which must be set.
func verdictOf(t *testing.T, ov pigtail.UsageOverview) pigtail.UsageVerdict {
	t.Helper()
	if ov.Verdict == nil {
		t.Fatal("the overview carries no verdict")
	}
	return *ov.Verdict
}

// fiftyHours records one sample per hour for 50 h for m, i and j, flat.
func fiftyHours(t *testing.T, src *usageOverviewSource, cells []ovCell) {
	t.Helper()
	h, err := usagehistory.Open(filepath.Join(t.TempDir(), usagehistory.FileName), ovNow)
	if err != nil {
		t.Fatal(err)
	}
	for ago := 50; ago >= 0; ago-- {
		at := ovNow.Add(-time.Duration(ago) * time.Hour)
		var batch []claudeusage.Sample
		for _, c := range cells {
			if c.letter == "t" || c.scope != "perso" {
				continue
			}
			batch = append(batch, claudeusage.Sample{At: at, AccountUUID: c.uuid, Label: c.letter, FetchedAt: at,
				SevenDay: &claudeusage.SampleWindow{Percent: c.p7, ResetsAt: &c.r7},
				FiveHour: &claudeusage.SampleWindow{Percent: c.p5, ResetsAt: &c.r5}})
		}
		if err := h.Append(batch); err != nil {
			t.Fatal(err)
		}
	}
	src.history = h
}

func TestUsageOverviewVerdictProvisionalWithoutHistory(t *testing.T) {
	v := verdictOf(t, overview(t, ovSource(t, fourAccounts())))
	if !v.Provisional || v.State == "" {
		t.Fatalf("verdict = %+v, want provisional with a state", v)
	}
}

func TestUsageOverviewVerdictFromHistory(t *testing.T) {
	cells := fourAccounts()
	src := ovSource(t, cells)
	fiftyHours(t, src, cells)
	v := verdictOf(t, overview(t, src))
	if v.Provisional {
		t.Fatalf("verdict = %+v, want from history (50 h of samples)", v)
	}
}

// The mockup's "Conso normale", with no history: the 5 h rates are 0 (I12), so
// the verdict holds ("ok"), where 5 h means would have blocked it on the 24th.
func TestUsageOverviewVerdictNormalProvisionalOK(t *testing.T) {
	mock := time.Date(2026, 10, 20, 21, 0, 0, 0, time.UTC)
	ago := ovNow.Sub(mock) + 5*time.Minute
	d := func(day, hh, mm int) time.Time { return time.Date(2026, 10, day, hh, mm, 0, 0, time.UTC) }
	cells := []ovCell{
		{id: "zq-mp", scope: "perso", letter: "m", uuid: uuidOvM, rank: 0, fetchedAgo: ago, p7: 20, p5: 16, r7: d(25, 17, 59), r5: d(20, 23, 19)},
		{id: "zq-ip", scope: "perso", letter: "i", uuid: uuidOvI, rank: 1, fetchedAgo: ago, p7: 64, p5: 69, r7: d(25, 8, 59), r5: d(20, 23, 29)},
		{id: "zq-jp", scope: "perso", letter: "j", uuid: uuidOvJ, rank: 2, fetchedAgo: ago, p7: 2, p5: 0, r7: d(27, 2, 59), r5: d(21, 2, 0)},
	}
	ov, err := ovSource(t, cells).Overview(mock)
	if err != nil {
		t.Fatal(err)
	}
	v := verdictOf(t, ov)
	if v.State != "ok" || !v.Provisional {
		t.Fatalf("verdict = %+v, want ok and provisional", v)
	}
	if v.NextRenewal == nil || v.NextRenewal.Label != "i" || !v.NextRenewal.At.Equal(d(25, 8, 59)) {
		t.Fatalf("next renewal = %+v, want i 25/10 08:59", v.NextRenewal)
	}
}

func TestUsageOverviewVerdictExcludesCapAndWorkOnly(t *testing.T) {
	with := verdictOf(t, overview(t, ovSource(t, fourAccounts())))
	var without []ovCell
	for _, c := range fourAccounts() {
		if c.letter != "t" {
			without = append(without, c)
		}
	}
	if got := verdictOf(t, overview(t, ovSource(t, without))); !reflect.DeepEqual(got, with) {
		t.Fatalf("verdict with t = %+v, without = %+v", with, got)
	}
}

func TestUsageOverviewOldestReading(t *testing.T) {
	v := verdictOf(t, overview(t, ovSource(t, fourAccounts())))
	// m 5 min, i 10 min, j 15 min; t (20 min, capped) is not in the verdict.
	if v.OldestReadingAt == nil || !v.OldestReadingAt.Equal(ovNow.Add(-15*time.Minute)) {
		t.Fatalf("oldest reading = %v, want now-15m", v.OldestReadingAt)
	}
}

func TestUsageOverviewVerdictNone(t *testing.T) {
	var cells []ovCell
	for _, c := range fourAccounts() {
		c.noReading = true
		cells = append(cells, c)
	}
	if v := verdictOf(t, overview(t, ovSource(t, cells))); v.State != "none" || v.Provisional {
		t.Fatalf("no reading: verdict = %+v, want none", v)
	}
	if v := verdictOf(t, overview(t, ovSource(t, nil))); v.State != "none" {
		t.Fatalf("no cell: verdict = %+v, want none", v)
	}
}

// A cell with no letter reaches the verdict as other-<short uuid>, never as its
// id (nor the "+"-joined ids Build makes).
func TestUsageOverviewVerdictLabelNamesNoCell(t *testing.T) {
	cells := append(fourAccounts(), ovCell{id: "zq-anon", scope: "perso", uuid: uuidOvX, rank: 8, fetchedAgo: time.Minute,
		p7: 1, p5: 1, r7: ovNow.Add(time.Hour), r5: ovNow.Add(time.Hour)})
	ov := overview(t, ovSource(t, cells))
	v := verdictOf(t, ov)
	if v.NextRenewal == nil || v.NextRenewal.Label != "other-"+uuidOvX[:8] {
		t.Fatalf("verdict = %+v, want the renewal of the anonymous account", v)
	}
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "zq-anon") || strings.Contains(string(b), "+") {
		t.Fatalf("the verdict names a cell: %s", b)
	}
}
