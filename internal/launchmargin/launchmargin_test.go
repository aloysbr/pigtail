package launchmargin

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// now is the instant every guard test reads at (a Tuesday).
var now = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

const (
	uM = "11111111-1111-4111-8111-111111111111"
	uJ = "33333333-3333-4333-8333-333333333333"
	uT = "44444444-4444-4444-8444-444444444444"
)

const (
	persoS = "perso"
	workS  = "globex"
)

// win is one cached window: utilization p, reset at now+reset (0 = no resets_at).
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
		r = `"` + now.Add(w.reset).Format("2006-01-02T15:04:05.000000+00:00") + `"`
	}
	return fmt.Sprintf(`{"utilization": %v, "resets_at": %s}`, w.p, r)
}

func usageFile(uuid string, age time.Duration, five, seven *win) []byte {
	return []byte(fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q,"organizationRateLimitTier":"default_claude_max_20x","userRateLimitTier":null},
"cachedUsageUtilization":{"fetchedAtMs":%d,"accountUuid":%q,"utilization":{"five_hour":%s,"seven_day":%s}}}`,
		uuid, now.Add(-age).UnixMilli(), uuid, five.json(), seven.json()))
}

func loggedInNoCache(uuid string) []byte {
	return []byte(fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q}}`, uuid))
}

// fixture: a cell table (cells at /cells/<id>) and their .claude.json files.
type fixture struct {
	tbl   *claudeprofile.Table
	files map[string][]byte
	reads int
	env   map[string]string
}

func newFixture() *fixture {
	return &fixture{tbl: &claudeprofile.Table{Cells: map[string]claudeprofile.Cell{}}, files: map[string][]byte{}, env: map[string]string{}}
}

func (f *fixture) cell(id, sc, account string, rank int, file []byte) *fixture {
	r := rank
	f.tbl.Cells[id] = claudeprofile.Cell{ID: id, ConfigDir: "/cells/" + id, Scope: sc, Account: account, Rank: &r}
	if file != nil {
		f.files[filepath.Join("/cells/"+id, claudeusage.FileName)] = file
	}
	return f
}

func (f *fixture) deps(v Viewer) Deps {
	return Deps{
		Getenv: func(k string) string { return f.env[k] },
		ReadFile: func(p string) ([]byte, error) {
			f.reads++
			b, ok := f.files[p]
			if !ok {
				return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
			}
			return b, nil
		},
		Now:    func() time.Time { return now },
		Cells:  f.tbl,
		Viewer: v,
	}
}

var master = Viewer{Master: true, Scope: persoS}

func free(uuid string) []byte {
	return usageFile(uuid, time.Minute, &win{5, time.Hour}, &win{40, 3 * 24 * time.Hour})
}

// over7d: 7-day window at 97 %, resetting in r.
func over7d(uuid string, r time.Duration) []byte {
	return usageFile(uuid, time.Minute, &win{5, time.Hour}, &win{97, r})
}

// fourCells: m and j, each with a perso and a globex cell; tt a globex-only account.
func fourCells(m, j []byte) *fixture {
	return newFixture().
		cell("mp", persoS, "m", 0, m).cell("jp", persoS, "j", 1, j).
		cell("mt", workS, "m", 2, m).cell("jt", workS, "j", 3, j)
}

func cellOf(t *testing.T, f *fixture, id string) claudeprofile.Cell {
	t.Helper()
	c, ok := f.tbl.Cell(id)
	if !ok {
		t.Fatalf("no cell %s", id)
	}
	return c
}

func TestGuardOkWhenUnderMargin(t *testing.T) {
	f := fourCells(free(uM), usageFile(uJ, time.Minute, &win{5, time.Hour}, &win{40, 24 * time.Hour}))
	got, dec, err := Guard(f.deps(master), cellOf(t, f, "mp"), false)
	if err != nil || got.ID != "mp" || dec.Action != ActionOK || dec.Warning != "" || dec.Margin != "" {
		t.Fatalf("guard = %s %+v %v, want mp ok (j renews first, but mp is under the margin)", got.ID, dec, err)
	}
}

func TestGuardSwitchesToFreeAccountSameScope(t *testing.T) {
	f := fourCells(over7d(uM, 2*24*time.Hour), free(uJ))
	got, dec, err := Guard(f.deps(master), cellOf(t, f, "mp"), false)
	if err != nil || got.ID != "jp" || dec.Action != ActionSwitched || dec.Final.ID != "jp" || dec.Requested.ID != "mp" {
		t.Fatalf("guard = %s %+v %v, want switched to jp", got.ID, dec, err)
	}
	want := "launch margin: cell mp (account m) is over the margin — launching on jp (account j) instead"
	if dec.Warning != want || dec.Margin != "switched mp->jp" {
		t.Fatalf("warning %q margin %q, want %q", dec.Warning, dec.Margin, want)
	}
	// same-scope non-master caller: the full text too
	_, dec, err = Guard(f.deps(Viewer{Scope: workS}), cellOf(t, f, "mt"), false)
	if err != nil || dec.Final.ID != "jt" || !strings.Contains(dec.Warning, "(account m)") || !strings.Contains(dec.Warning, "(account j)") {
		t.Fatalf("same-scope = %+v %v", dec, err)
	}
}

// m declares a 70 % ceiling (select_pct): at 70 % on 7 days a launch on mt
// moves to the free account of the same scope; at 69.9 it stays; forced, it
// stays on mt (the accepted product consequence).
func TestGuardRedirectsAccountAtItsCeiling(t *testing.T) {
	mk := func(p7 float64) *fixture {
		f := fourCells(usageFile(uM, time.Minute, &win{5, time.Hour}, &win{p7, 3 * 24 * time.Hour}), free(uJ))
		for _, id := range []string{"mt", "mp"} {
			c := f.tbl.Cells[id]
			sel := 70.0
			c.SelectPct = &sel
			f.tbl.Cells[id] = c
		}
		return f
	}
	f := mk(70)
	got, dec, err := Guard(f.deps(master), cellOf(t, f, "mt"), false)
	if err != nil || got.ID != "jt" || dec.Action != ActionSwitched {
		t.Fatalf("at 70: guard = %s %s %v, want switched to jt", got.ID, dec.Action, err)
	}
	f = mk(69.9)
	got, dec, err = Guard(f.deps(master), cellOf(t, f, "mt"), false)
	if err != nil || got.ID != "mt" || dec.Action != ActionOK {
		t.Fatalf("at 69.9: guard = %s %s %v, want mt ok", got.ID, dec.Action, err)
	}
	f = mk(70)
	got, dec, err = Guard(f.deps(master), cellOf(t, f, "mt"), true)
	if err != nil || got.ID != "mt" || dec.Action != ActionForced {
		t.Fatalf("forced: guard = %s %s %v, want mt forced", got.ID, dec.Action, err)
	}
}

func TestGuardBoundedRenderingNonMaster(t *testing.T) {
	other := Viewer{Scope: workS}
	f := fourCells(over7d(uM, 2*24*time.Hour), free(uJ))
	_, dec, err := Guard(f.deps(other), cellOf(t, f, "mp"), false)
	if err != nil || dec.Final.ID != "jp" {
		t.Fatalf("guard = %+v %v", dec, err)
	}
	for name, s := range map[string]string{"warning": dec.Warning, "margin": dec.Margin, "summary": dec.Summary} {
		if strings.Contains(s, "account") || strings.Contains(s, persoS) || !strings.Contains(s, "jp") {
			t.Fatalf("bounded %s = %q", name, s)
		}
	}
	// refusal: the release time and the cell, nothing else
	f = fourCells(over7d(uM, 2*24*time.Hour), over7d(uJ, 30*time.Hour))
	_, dec, err = Guard(f.deps(other), cellOf(t, f, "mp"), false)
	var om *OverMarginError
	if !errors.As(err, &om) {
		t.Fatalf("err = %v, want OverMarginError", err)
	}
	release := now.Add(30 * time.Hour).Format("Mon 15:04")
	for name, s := range map[string]string{"error": err.Error(), "margin": dec.Margin, "summary": dec.Summary, "warning": dec.Warning} {
		if strings.Contains(s, "account m") || strings.Contains(s, "first release: j") || strings.Contains(s, persoS) || !strings.Contains(s, "mp") {
			t.Fatalf("bounded refusal %s = %q", name, s)
		}
	}
	if !strings.Contains(err.Error(), release) || om.Scope != "" || om.Account != "" {
		t.Fatalf("bounded refusal = %q %+v, want the release %s and no scope/account", err, om, release)
	}
	// the master keeps the full text
	_, _, err = Guard(f.deps(master), cellOf(t, f, "mp"), false)
	if err == nil || !strings.Contains(err.Error(), "scope "+persoS) || !strings.Contains(err.Error(), "first release: j at "+release) {
		t.Fatalf("master refusal = %v", err)
	}
}

func TestGuardNeverSwitchesAcrossScope(t *testing.T) {
	f := fourCells(over7d(uM, 2*24*time.Hour), over7d(uJ, 30*time.Hour)).cell("tt", workS, "t", 4, free(uT))
	got, dec, err := Guard(f.deps(master), cellOf(t, f, "mp"), false)
	if err == nil || got.ID != "mp" || dec.Action != ActionRefused {
		t.Fatalf("guard = %s %+v %v, want refused (tt is another scope)", got.ID, dec, err)
	}
	// and the globex request does switch to tt
	got, _, err = Guard(f.deps(master), cellOf(t, f, "mt"), false)
	if err != nil || got.ID != "tt" {
		t.Fatalf("globex guard = %s %v, want tt", got.ID, err)
	}
}

func TestGuardRefusesWhenWholeScopeOver(t *testing.T) {
	f := fourCells(over7d(uM, 2*24*time.Hour), over7d(uJ, 30*time.Hour))
	got, dec, err := Guard(f.deps(master), cellOf(t, f, "mp"), false)
	var om *OverMarginError
	if !errors.As(err, &om) || got.ID != "mp" || dec.Action != ActionRefused {
		t.Fatalf("guard = %s %+v %v, want refused", got.ID, dec, err)
	}
	release := now.Add(30 * time.Hour)
	want := "every account of scope perso is over the margin (5h or 7d at or above the account's ceiling); first release: j at " + release.Format("Mon 15:04") + " — pass --over-margin to launch anyway"
	if err.Error() != want {
		t.Fatalf("message = %q, want %q", err, want)
	}
	if dec.FreesAt == nil || !dec.FreesAt.Equal(release) || om.Scope != persoS || om.Account != "j" {
		t.Fatalf("frees at %v, err %+v", dec.FreesAt, om)
	}
	if dec.Margin != "refused perso frees "+release.Format(time.RFC3339) {
		t.Fatalf("margin = %q", dec.Margin)
	}
}

func TestGuardRefusalResetTimeUnknown(t *testing.T) {
	noReset := func(u string) []byte { return usageFile(u, time.Minute, &win{5, time.Hour}, &win{97, 0}) }
	f := fourCells(noReset(uM), noReset(uJ))
	_, dec, err := Guard(f.deps(master), cellOf(t, f, "mp"), false)
	if err == nil || !strings.Contains(err.Error(), "reset time unknown") || dec.FreesAt != nil || !strings.HasSuffix(dec.Margin, "frees unknown") {
		t.Fatalf("guard = %+v %v, want reset time unknown", dec, err)
	}
}

func TestGuardForceKeepsRequested(t *testing.T) {
	for name, j := range map[string][]byte{"whole scope over": over7d(uJ, 30*time.Hour), "a free account exists": free(uJ)} {
		f := fourCells(over7d(uM, 2*24*time.Hour), j)
		got, dec, err := Guard(f.deps(master), cellOf(t, f, "mp"), true)
		if err != nil || got.ID != "mp" || dec.Action != ActionForced || dec.Margin != "forced mp" {
			t.Fatalf("%s: guard = %s %+v %v, want forced mp", name, got.ID, dec, err)
		}
		if f.reads != 0 {
			t.Fatalf("%s: --over-margin read %d usage files, want none", name, f.reads)
		}
	}
}

func TestGuardNoReadingPasses(t *testing.T) {
	for name, m := range map[string][]byte{"no file": nil, "logged in, no cache": loggedInNoCache(uM)} {
		f := fourCells(m, over7d(uJ, 30*time.Hour))
		got, dec, err := Guard(f.deps(master), cellOf(t, f, "mp"), false)
		if err != nil || got.ID != "mp" || dec.Action != ActionOK {
			t.Fatalf("%s: guard = %s %+v %v, want ok", name, got.ID, dec, err)
		}
	}
}

func TestGuardStaleReadingUnderMarginPasses(t *testing.T) {
	f := fourCells(usageFile(uM, 3*24*time.Hour, &win{5, time.Hour}, &win{40, 2 * 24 * time.Hour}), free(uJ))
	got, dec, err := Guard(f.deps(master), cellOf(t, f, "mp"), false)
	if err != nil || got.ID != "mp" || dec.Action != ActionOK {
		t.Fatalf("guard = %s %+v %v, want ok", got.ID, dec, err)
	}
}

func TestGuardStaleReadingOverMarginBlocks(t *testing.T) {
	stale := usageFile(uM, 2*24*time.Hour, &win{5, time.Hour}, &win{97, 2 * 24 * time.Hour})
	f := fourCells(stale, free(uJ))
	got, dec, err := Guard(f.deps(master), cellOf(t, f, "mp"), false)
	if err != nil || got.ID != "jp" || dec.Action != ActionSwitched {
		t.Fatalf("guard = %s %+v %v, want switched to jp", got.ID, dec, err)
	}
	f = fourCells(stale, over7d(uJ, 30*time.Hour))
	if _, dec, err = Guard(f.deps(master), cellOf(t, f, "mp"), false); err == nil || dec.Action != ActionRefused {
		t.Fatalf("all over = %+v %v, want refused", dec, err)
	}
}

func TestGuardUnmeasuredFailsOpen(t *testing.T) {
	f := fourCells(over7d(uM, 2*24*time.Hour), over7d(uJ, 30*time.Hour))
	f.env[EnvRotation] = "off"
	got, dec, err := Guard(f.deps(master), cellOf(t, f, "mp"), false)
	if err != nil || got.ID != "mp" || dec.Action != ActionUnmeasured || dec.Warning == "" || dec.Margin != "unmeasured rotation-off" {
		t.Fatalf("rotation off = %s %+v %v, want unmeasured mp", got.ID, dec, err)
	}
	// no candidate in the scope (no logged-in cell): the scope form says why
	f = newFixture().cell("mp", persoS, "m", 0, nil).cell("jp", persoS, "j", 1, []byte(`{}`))
	_, dec, err = GuardScope(f.deps(master), persoS, false)
	if !errors.Is(err, claudeusage.ErrNoCandidate) || dec.Action != ActionUnmeasured || dec.Warning == "" || dec.Margin != "unmeasured no-candidate" {
		t.Fatalf("no candidate = %+v %v, want unmeasured", dec, err)
	}
	// rotation off in the scope form: the error, unmeasured
	f.env[EnvRotation] = "off"
	if _, dec, err = GuardScope(f.deps(master), persoS, false); !errors.Is(err, ErrRotationOff) || dec.Action != ActionUnmeasured {
		t.Fatalf("scope rotation off = %+v %v", dec, err)
	}
}

func TestGuardSiblingCellOfSaturatedAccountNotChosen(t *testing.T) {
	m := over7d(uM, 2*24*time.Hour)
	f := fourCells(m, free(uJ)).cell("mp2", persoS, "m", 5, m)
	for _, req := range []string{"mp", "mp2"} {
		got, dec, err := Guard(f.deps(master), cellOf(t, f, req), false)
		if err != nil || got.ID != "jp" || dec.Action != ActionSwitched {
			t.Fatalf("%s: guard = %s %+v %v, want jp", req, got.ID, dec, err)
		}
	}
	f = fourCells(m, over7d(uJ, 30*time.Hour)).cell("mp2", persoS, "m", 5, m)
	if got, _, err := Guard(f.deps(master), cellOf(t, f, "mp2"), false); err == nil || got.ID != "mp2" {
		t.Fatalf("all over = %s %v, want refused on mp2 (never the sibling mp)", got.ID, err)
	}
}

func TestGuardScopeRefusesAndForces(t *testing.T) {
	f := fourCells(over7d(uM, 2*24*time.Hour), over7d(uJ, 30*time.Hour))
	got, dec, err := GuardScope(f.deps(master), persoS, false)
	if err == nil || dec.Action != ActionRefused || got.ID != "jp" {
		t.Fatalf("scope all over = %s %+v %v, want refused (pick jp)", got.ID, dec, err)
	}
	got, dec, err = GuardScope(f.deps(master), persoS, true)
	if err != nil || dec.Action != ActionForced || got.ID != "jp" || dec.Margin != "forced jp" {
		t.Fatalf("scope forced = %s %+v %v", got.ID, dec, err)
	}
	f = fourCells(free(uM), free(uJ))
	if got, dec, err = GuardScope(f.deps(master), persoS, false); err != nil || dec.Action != ActionOK || dec.Choice == nil || got.ID == "" {
		t.Fatalf("scope ok = %s %+v %v", got.ID, dec, err)
	}
}

func TestAccountOverSiblingCell(t *testing.T) {
	m := over7d(uM, 2*24*time.Hour)
	f := fourCells(m, free(uJ)).cell("mp2", persoS, "m", 5, m)
	d := f.deps(master)
	_, dec, err := GuardScope(d, persoS, false)
	if err != nil || dec.Choice == nil {
		t.Fatalf("scope = %+v %v", dec, err)
	}
	if !AccountOver(d, cellOf(t, f, "mp2"), dec) || !AccountOver(d, cellOf(t, f, "mp"), dec) {
		t.Fatal("mp2 (sibling of the saturated m) must read over the margin")
	}
	if AccountOver(d, cellOf(t, f, "jp"), dec) {
		t.Fatal("jp is under the margin")
	}
}

// Record (trace primitives).

type journalSink struct {
	rows []*pigtail.JournalEntry
	err  error
}

func (j *journalSink) add(e *pigtail.JournalEntry) error {
	if j.err != nil {
		return j.err
	}
	j.rows = append(j.rows, e)
	return nil
}

func sinks(t *testing.T, j *journalSink, logs *[]string) (Sinks, *spawnreg.Registry) {
	t.Helper()
	reg := spawnreg.Open(filepath.Join(t.TempDir(), "spawns.jsonl"))
	return Sinks{Spawns: reg, Journal: j.add, Logf: func(f string, a ...any) { *logs = append(*logs, fmt.Sprintf(f, a...)) }}, reg
}

func TestRecordSkipsOk(t *testing.T) {
	j := &journalSink{}
	var logs []string
	s, reg := sinks(t, j, &logs)
	Record(s, Decision{Action: ActionOK, Requested: claudeprofile.Cell{ID: "mp"}, At: now})
	es, err := reg.Entries()
	if err != nil || len(es) != 0 || len(j.rows) != 0 {
		t.Fatalf("ok traced: %+v %v %+v", es, err, j.rows)
	}
	f := fourCells(over7d(uM, 2*24*time.Hour), free(uJ))
	_, dec, _ := Guard(f.deps(master), cellOf(t, f, "mp"), false)
	dec.SubjectID = "sub_a"
	Record(s, dec)
	es, err = reg.Entries()
	if err != nil || len(es) != 1 || es[0].Kind != spawnreg.KindMargin || es[0].Margin != "switched mp->jp" || es[0].Result != ActionSwitched || es[0].WorkspaceID != "" || es[0].SubjectID != "sub_a" {
		t.Fatalf("entries = %+v %v", es, err)
	}
	if len(j.rows) != 1 || j.rows[0].Kind != JournalKind || j.rows[0].Summary != dec.Summary || j.rows[0].Scope != "" || !strings.HasPrefix(j.rows[0].ID, "launch-margin:") || !strings.HasSuffix(j.rows[0].ID, ":mp") || j.rows[0].Source != "pigtail:spawns" {
		t.Fatalf("journal = %+v", j.rows)
	}
}

func TestRecordJournalFailureDoesNotBlock(t *testing.T) {
	j := &journalSink{err: errors.New("daemon down")}
	var logs []string
	s, reg := sinks(t, j, &logs)
	Record(s, Decision{Action: ActionForced, Requested: claudeprofile.Cell{ID: "mp"}, Final: claudeprofile.Cell{ID: "mp"}, Margin: "forced mp", At: now})
	es, _ := reg.Entries()
	if len(es) != 1 || len(logs) != 1 || !strings.Contains(logs[0], "daemon down") {
		t.Fatalf("entries %+v logs %q", es, logs)
	}
	// an unwritable registry is logged too, the journal still written
	j2 := &journalSink{}
	logs = nil
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	Record(Sinks{Spawns: spawnreg.Open(filepath.Join(blocker, "spawns.jsonl")), Journal: j2.add, Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }},
		Decision{Action: ActionForced, Requested: claudeprofile.Cell{ID: "mp"}, Margin: "forced mp", At: now})
	if len(j2.rows) != 1 || len(logs) != 1 {
		t.Fatalf("journal %+v logs %q", j2.rows, logs)
	}
}

func TestRecordCopiesBoundedText(t *testing.T) {
	other := Viewer{Scope: workS}
	for name, j := range map[string][]byte{"switch": free(uJ), "refusal": over7d(uJ, 30*time.Hour)} {
		f := fourCells(over7d(uM, 2*24*time.Hour), j)
		_, dec, _ := Guard(f.deps(other), cellOf(t, f, "mp"), false)
		js := &journalSink{}
		var logs []string
		s, reg := sinks(t, js, &logs)
		Record(s, dec)
		es, err := reg.Entries()
		if err != nil || len(es) != 1 || len(js.rows) != 1 {
			t.Fatalf("%s: entries %+v %v journal %+v", name, es, err, js.rows)
		}
		line := fmt.Sprintf("%+v", es[0])
		for _, s := range []string{line, js.rows[0].Summary, js.rows[0].ID} {
			if strings.Contains(s, persoS) || strings.Contains(s, "(account") || strings.Contains(s, "first release: ") {
				t.Fatalf("%s: trace names another scope or an account: %q", name, s)
			}
		}
		if es[0].Margin != dec.Margin || js.rows[0].Summary != dec.Summary {
			t.Fatalf("%s: Record did not copy the decision's texts: %+v %+v", name, es[0], js.rows[0])
		}
	}
}

// TestOverMarginAPIMessage: the API answer is the refusal without the CLI's
// --over-margin advice (Q2: no forcing from the API), both renderings.
func TestOverMarginAPIMessage(t *testing.T) {
	f := fourCells(over7d(uM, 2*24*time.Hour), over7d(uJ, 30*time.Hour))
	release := now.Add(30 * time.Hour).Format("Mon 15:04")
	for name, v := range map[string]Viewer{"master": master, "bounded": {Scope: workS}} {
		_, _, err := Guard(f.deps(v), cellOf(t, f, "mp"), false)
		var om *OverMarginError
		if !errors.As(err, &om) {
			t.Fatalf("%s: err = %v, want OverMarginError", name, err)
		}
		api := om.APIMessage()
		if strings.Contains(api, "over-margin") || !strings.Contains(api, release) || !strings.Contains(api, "is over the margin") {
			t.Fatalf("%s: API message = %q", name, api)
		}
		if !strings.Contains(err.Error(), "--over-margin") {
			t.Fatalf("%s: the CLI message lost its advice: %q", name, err)
		}
	}
}

// midWeek: 3.5 days to the 7-day reset, half the window elapsed; the default
// budget 70 allows 40 % there.
const midWeek = 84 * time.Hour

func overBudget(uuid string) []byte {
	return usageFile(uuid, time.Minute, &win{10, 2 * time.Hour}, &win{60, midWeek})
}

func withinBudget(uuid string) []byte {
	return usageFile(uuid, time.Minute, &win{10, 2 * time.Hour}, &win{30, 4 * 24 * time.Hour})
}

func TestGuardScopeStaffingRefusesOverBudget(t *testing.T) {
	f := newFixture().cell("mp", persoS, "m", 0, overBudget(uM))
	c, dec, err := GuardScopeStaffing(f.deps(Viewer{Master: true}), persoS)
	var ob *OverBudgetError
	if !errors.As(err, &ob) {
		t.Fatalf("err = %v, want *OverBudgetError", err)
	}
	if ob.FreesAt == nil || ob.Mixed || c.ID != "" {
		t.Fatalf("refusal: %+v, cell %q", ob, c.ID)
	}
	msg := ob.APIMessage()
	if !strings.HasPrefix(msg, "every account of scope perso is out of its weekly token budget; first release at ") {
		t.Fatalf("message: %q", msg)
	}
	if strings.Contains(msg, "mp") || strings.Contains(msg, " m ") {
		t.Fatalf("message names a cell or an account: %q", msg)
	}
	if dec.Action != ActionRefused || !strings.HasPrefix(dec.Margin, "refused perso budget frees ") {
		t.Fatalf("decision: %+v", dec)
	}
}

func TestGuardScopeStaffingPicksUnderBudgetAccount(t *testing.T) {
	f := newFixture().cell("mp", persoS, "m", 0, overBudget(uM)).cell("jp", persoS, "j", 1, withinBudget(uJ))
	c, dec, err := GuardScopeStaffing(f.deps(Viewer{Master: true}), persoS)
	if err != nil || c.ID != "jp" || dec.Action != ActionOK {
		t.Fatalf("cell %q, decision %+v, err %v", c.ID, dec, err)
	}
}

func TestGuardScopeStaffingRotationOffIsUnmeasured(t *testing.T) {
	f := newFixture().cell("mp", persoS, "m", 0, withinBudget(uM))
	f.env[EnvRotation] = "off"
	c, dec, err := GuardScopeStaffing(f.deps(Viewer{Master: true}), persoS)
	if !errors.Is(err, ErrRotationOff) || c.ID != "" || dec.Action != ActionUnmeasured {
		t.Fatalf("cell %q, decision %+v, err %v", c.ID, dec, err)
	}
}

// D6: the budget refuses nothing outside Staffing.
func TestGuardScopeUnchangedByBudget(t *testing.T) {
	f := newFixture().cell("mp", persoS, "m", 0, overBudget(uM))
	d := f.deps(Viewer{Master: true})
	c, dec, err := GuardScope(d, persoS, false)
	if err != nil || c.ID != "mp" || dec.Action != ActionOK {
		t.Fatalf("GuardScope: cell %q, decision %+v, err %v", c.ID, dec, err)
	}
	requested := cellOf(t, f, "mp")
	c, dec, err = Guard(d, requested, false)
	if err != nil || c.ID != "mp" || dec.Action != ActionOK {
		t.Fatalf("Guard: cell %q, decision %+v, err %v", c.ID, dec, err)
	}
	c, ch, err := PickIn(f.tbl, persoS, nil, d.getenv, d.ReadFile, now)
	if err != nil || c.ID != "mp" || ch.Group != claudeusage.GroupFresh || ch.Budget != nil {
		t.Fatalf("PickIn: cell %q, choice %+v, err %v", c.ID, ch.Candidate, err)
	}
}

func TestGuardScopeStaffingStaleReadingIsUnmeasured(t *testing.T) {
	stale := usageFile(uM, 31*time.Minute, &win{10, 2 * time.Hour}, &win{1, midWeek})
	f := newFixture().cell("mp", persoS, "m", 0, stale)
	_, dec, err := GuardScopeStaffing(f.deps(Viewer{Master: true}), persoS)
	var ub *UnmeasuredBudgetError
	if !errors.As(err, &ub) {
		t.Fatalf("err = %v, want *UnmeasuredBudgetError", err)
	}
	if ub.APIMessage() != "unmeasured: reading older than 30m" {
		t.Fatalf("message: %q", ub.APIMessage())
	}
	if dec.Action != ActionRefused {
		t.Fatalf("decision: %+v", dec)
	}
	// A cell logged in with no usage cache at all closes the same way.
	f = newFixture().cell("mp", persoS, "m", 0, loggedInNoCache(uM))
	_, _, err = GuardScopeStaffing(f.deps(Viewer{Master: true}), persoS)
	if !errors.As(err, &ub) || !strings.HasPrefix(ub.APIMessage(), "unmeasured: no reading") {
		t.Fatalf("no reading: %v", err)
	}
}

func TestGuardScopeStaffingMixedMarginAndBudget(t *testing.T) {
	marginOnly := usageFile(uJ, time.Minute, &win{95, 2 * time.Hour}, &win{5, 4 * 24 * time.Hour})
	f := newFixture().cell("mp", persoS, "m", 0, overBudget(uM)).cell("jp", persoS, "j", 1, marginOnly)
	_, _, err := GuardScopeStaffing(f.deps(Viewer{Master: true}), persoS)
	var ob *OverBudgetError
	if !errors.As(err, &ob) || !ob.Mixed {
		t.Fatalf("err = %#v", err)
	}
	if !strings.HasPrefix(ob.APIMessage(), "every account of scope perso is out of its margin or weekly budget; first release at ") {
		t.Fatalf("message: %q", ob.APIMessage())
	}
}

func TestGuardScopeStaffingOverMarginOnlyIsMarginError(t *testing.T) {
	marginOnly := usageFile(uM, time.Minute, &win{95, 2 * time.Hour}, &win{5, 4 * 24 * time.Hour})
	f := newFixture().cell("mp", persoS, "m", 0, marginOnly)
	_, _, err := GuardScopeStaffing(f.deps(Viewer{Master: true}), persoS)
	var om *OverMarginError
	if !errors.As(err, &om) {
		t.Fatalf("err = %#v, want *OverMarginError", err)
	}
	var ob *OverBudgetError
	if errors.As(err, &ob) {
		t.Fatalf("a pure margin refusal reads as a budget one: %v", err)
	}
}

func TestGuardScopeStaffingNoFiveHourIsUnmeasured(t *testing.T) {
	f := newFixture().cell("jp", persoS, "j", 0, usageFile(uJ, time.Minute, nil, &win{10, 24 * time.Hour}))
	c, dec, err := GuardScopeStaffing(f.deps(Viewer{Master: true}), persoS)
	var ub *UnmeasuredBudgetError
	if !errors.As(err, &ub) || ub.APIMessage() != "unmeasured: no 5-hour window in the reading" {
		t.Fatalf("err = %v (%T), want *UnmeasuredBudgetError", err, err)
	}
	if c.ID != "" || dec.Action != ActionRefused {
		t.Fatalf("cell %q, decision %+v", c.ID, dec)
	}
}

func TestGuardScopeStaffingFiveHourNotStartedPicks(t *testing.T) {
	f := newFixture().cell("jp", persoS, "j", 0, usageFile(uJ, time.Minute, &win{0, 0}, &win{10, 96 * time.Hour}))
	c, dec, err := GuardScopeStaffing(f.deps(Viewer{Master: true}), persoS)
	if err != nil || c.ID != "jp" || dec.Action != ActionOK {
		t.Fatalf("cell %q, decision %+v, err %v", c.ID, dec, err)
	}
}

// D6: the same reading, read by everything that is not Staffing.
func TestGuardScopeUnchangedByNoFiveHour(t *testing.T) {
	f := newFixture().cell("jp", persoS, "j", 0, usageFile(uJ, time.Minute, nil, &win{10, 24 * time.Hour}))
	d := f.deps(Viewer{Master: true})
	c, dec, err := GuardScope(d, persoS, false)
	if err != nil || c.ID != "jp" || dec.Action != ActionOK {
		t.Fatalf("GuardScope: cell %q, decision %+v, err %v", c.ID, dec, err)
	}
	c, dec, err = Guard(d, cellOf(t, f, "jp"), false)
	if err != nil || c.ID != "jp" || dec.Action != ActionOK {
		t.Fatalf("Guard: cell %q, decision %+v, err %v", c.ID, dec, err)
	}
	c, ch, err := PickIn(f.tbl, persoS, nil, d.getenv, d.ReadFile, now)
	if err != nil || c.ID != "jp" || ch.Group != claudeusage.GroupNoReading || ch.Budget != nil {
		t.Fatalf("PickIn: cell %q, choice %+v, err %v", c.ID, ch.Candidate, err)
	}
}
