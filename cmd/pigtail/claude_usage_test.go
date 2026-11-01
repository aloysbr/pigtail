package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/launchmargin"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

var (
	usageNow = time.Date(2026, 10, 5, 11, 32, 0, 0, time.UTC)
	cest     = time.FixedZone("CEST", 2*3600)
	cols     = regexp.MustCompile(`\s{2,}`)
)

const tmplPath = "../../internal/claudeusage/testdata/max-cell.json.tmpl"

// usageHome builds a throwaway HOME with three cells (ke perso on m, kd work
// on j, t work on the Team seat) plus two baits that must never be read: the
// bare ~/.claude.json and ~/.claude-bait, absent from the table.
func usageHome(t *testing.T, letters bool) (usageDeps, *bytes.Buffer, *bytes.Buffer, *[]string) {
	t.Helper()
	testutil.SerialOnly(t) // swaps flagScope
	flagScope = ""
	home := t.TempDir()
	cfg := filepath.Join(home, ".config", "pigtail")
	team, err := os.ReadFile("../../internal/claudeusage/testdata/team-cell.json")
	if err != nil {
		t.Fatal(err)
	}
	maxCell := func(uuid string, fetched time.Time, p7 int, r7 string, p5 int, r5 string) []byte {
		return maxCellFile(t, uuid, fetched, p7, r7, p5, r5)
	}
	write := func(rel string, b []byte) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".claude-ke/.claude.json", maxCell("11111111-1111-4111-8111-111111111111", usageNow.Add(-(12*time.Hour+36*time.Minute)), 1, "2026-10-11T17:00:00.512+00:00", 4, "2026-10-05T03:00:00.5+00:00"))
	write(".claude-kd/.claude.json", maxCell("33333333-3333-4333-8333-333333333333", usageNow.Add(-time.Minute), 91, "2026-10-06T02:00:00.177378+00:00", 8, "2026-10-05T11:50:00.177348+00:00"))
	write(".claude-w4/.claude.json", team)
	write(".claude.json", maxCell("99999999-9999-4999-8999-999999999999", usageNow, 0, "2026-10-11T00:00:00+00:00", 0, "2026-10-05T12:00:00+00:00"))
	write(".claude-bait/.claude.json", maxCell("88888888-8888-4888-8888-888888888888", usageNow, 0, "2026-10-11T00:00:00+00:00", 0, "2026-10-05T12:00:00+00:00"))
	acc := func(l string) string {
		if letters {
			return ", account: " + l
		}
		return ""
	}
	write(".config/pigtail/claude-cells.yaml", []byte("cells:\n"+
		"  kd: { config_dir: "+filepath.Join(home, ".claude-kd")+", scope: globex"+acc("j")+" }\n"+
		"  ke: { config_dir: "+filepath.Join(home, ".claude-ke")+", scope: perso"+acc("m")+" }\n"+
		"  t:  { config_dir: "+filepath.Join(home, ".claude-w4")+", scope: globex"+acc("t")+" }\n"))
	var out, errw bytes.Buffer
	asked := &[]string{}
	env := map[string]string{"CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude-ke")}
	d := usageDeps{
		cellsDir: cfg,
		getenv:   func(k string) string { return env[k] },
		readFile: func(p string) ([]byte, error) { *asked = append(*asked, p); return os.ReadFile(p) },
		now:      func() time.Time { return usageNow },
		loc:      cest,
		out:      &out, errw: &errw,
		// the daemon answers with an empty session table unless a test says otherwise
		peerSessions: func() ([]pigtail.PeerSession, error) { return nil, nil },
		stateDir:     filepath.Join(home, ".local", "state", "pigtail"),
		refreshLock:  claudeusage.TryRefreshLock,
	}
	return d, &out, &errw, asked
}

// maxCellFile fills testdata/max-cell.json.tmpl, the same holes as the
// claudeusage fixtures (Max ×20, userRateLimitTier null).
func maxCellFile(t *testing.T, uuid string, fetched time.Time, p7 int, r7 string, p5 int, r5 string) []byte {
	t.Helper()
	tmpl, err := os.ReadFile(tmplPath)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(strings.NewReplacer("{{UUID}}", uuid, "{{CACHE_UUID}}", uuid,
		"{{ORG_TIER}}", "default_claude_max_20x", "{{USER_TIER}}", "null",
		"{{FETCHED_MS}}", strconv.FormatInt(fetched.UnixMilli(), 10),
		"{{P7}}", strconv.Itoa(p7), "{{P5}}", strconv.Itoa(p5), "{{R7}}", r7, "{{R5}}", r5).Replace(string(tmpl)))
}

// researchHome is the research reading of 2026-10-05 11:32Z — the 8 cells,
// letters set , same values as claudeusage's
// researchCells. It reuses usageHome's deps (caller = cell ke, perso) and
// rewrites the cell files and the table.
func researchHome(t *testing.T) (usageDeps, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	d, out, errw, _ := usageHome(t, true)
	home := filepath.Dir(filepath.Dir(d.cellsDir))
	team, err := os.ReadFile("../../internal/claudeusage/testdata/team-cell.json")
	if err != nil {
		t.Fatal(err)
	}
	const (
		uM = "11111111-1111-4111-8111-111111111111"
		uI = "22222222-2222-4222-8222-222222222222"
		uJ = "33333333-3333-4333-8333-333333333333"
	)
	ago := func(d time.Duration) time.Time { return usageNow.Add(-d) }
	cells := []struct {
		id, dir, scope, letter string
		body                   []byte
	}{
		{"ka", ".claude-ka", "perso", "i", maxCellFile(t, uI, ago(21*time.Hour), 0, "2026-10-11T08:00:00.411+00:00", 2, "2026-10-04T19:20:00.1+00:00")},
		{"kb", ".claude-kb", "globex", "i", maxCellFile(t, uI, ago(15*time.Hour+12*time.Minute), 9, "2026-10-11T07:59:59.735093+00:00", 11, "2026-10-05T01:00:00.735+00:00")},
		{"kc", ".claude-kc", "perso", "j", maxCellFile(t, uJ, ago(time.Minute), 91, "2026-10-06T02:00:00.177378+00:00", 8, "2026-10-05T11:50:00.177348+00:00")},
		{"kd", ".claude-kd", "globex", "j", maxCellFile(t, uJ, ago(23*time.Hour+36*time.Minute), 73, "2026-10-06T02:00:00.177378+00:00", 25, "2026-10-04T16:00:00.2+00:00")},
		{"ke", ".claude-ke", "perso", "m", maxCellFile(t, uM, ago(13*time.Hour), 1, "2026-10-11T17:00:00.512+00:00", 3, "2026-10-05T02:30:00.5+00:00")},
		{"kf", ".claude-kf", "globex", "m", maxCellFile(t, uM, ago(12*time.Hour+36*time.Minute), 1, "2026-10-11T17:00:00.512+00:00", 4, "2026-10-05T03:00:00.5+00:00")},
		{"p", ".claude-perso", "perso", "m", []byte(`{"oauthAccount":{"accountUuid":"` + uM + `","organizationRateLimitTier":"default_claude_max_20x","userRateLimitTier":null}}`)},
		{"t", ".claude-w4", "globex", "t", team},
	}
	var table strings.Builder
	table.WriteString("cells:\n")
	for _, c := range cells {
		dir := filepath.Join(home, c.dir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".claude.json"), c.body, 0o600); err != nil {
			t.Fatal(err)
		}
		// rank = the cell's declaration index in the launcher config, as the table records it
		fmt.Fprintf(&table, "  %s: { config_dir: %s, scope: %s, account: %s, rank: %d }\n", c.id, dir, c.scope, c.letter, profilesRank[c.id])
	}
	if err := os.WriteFile(filepath.Join(d.cellsDir, "claude-cells.yaml"), []byte(table.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	// The session table of the same morning, as the daemon returns it to a
	// perso caller: i 1/2, j 1/5, m 1/8, t 0/9.
	var ps []pigtail.PeerSession
	add := func(cell string, n, busy int) {
		for k := 0; k < n; k++ {
			st := "idle"
			if k < busy {
				st = "busy"
			}
			ps = append(ps, pigtail.PeerSession{SessionID: fmt.Sprintf("%s-%d", cell, k), CellID: cell, Status: st})
		}
	}
	add("ka", 1, 1)
	add("kb", 1, 0)
	add("kc", 3, 1)
	add("kd", 2, 0)
	add("ke", 3, 0)
	add("kf", 4, 1)
	add("p", 1, 0)
	add("t", 9, 0)
	d.peerSessions = func() ([]pigtail.PeerSession, error) { return ps, nil }
	return d, out, errw, home
}

// profilesRank is the launcher config's declaration order of the cells .
var profilesRank = map[string]int{"kf": 0, "ke": 1, "kb": 2, "ka": 3, "kd": 4, "kc": 5, "t": 6, "p": 7}

// researchGoldenWork is the Spec's work block, verbatim: sessions as the
// daemon would filter them for work (the fake returns all; Build keeps the
// visible cells only).
const researchGoldenWork = `7d used (weighted): 36%

ACCOUNT  7D   7D RESET     5H  5H RESET     BUDGET                       AGE     SESSIONS*  CAP  CELLS  FLAGS
m        1%   11/10 19:00  0%  passed       ?                            12h36!  1/4        ×20  kf
i        9%   11/10 09:59  0%  passed       ?                            15h12!  0/1        ×20  kb
j        91%  06/10 04:00  8%  05/10 13:50  91/70% over until Tue 04:00  1m      0/2        ×20  kd
t        69%  10/10 21:00  0%  05/10 16:20  ?                            2h06!   0/9        ×5   t      credits=105/100$
budget: m: unmeasured: reading older than 30m — Staffing will not launch on this account
budget: i: unmeasured: reading older than 30m — Staffing will not launch on this account
budget: t: unmeasured: reading older than 30m — Staffing will not launch on this account

! reading older than 1h: Claude Code itself ignores it — pigtail usage --refresh refreshes it
* globex-visible sessions only
`

// researchGolden is the Spec's text block, verbatim. A difference is fixed in
// the code, never here. The BUDGET column and the budget: lines are the weekly
// token budget's addition to that block.
const researchGolden = `7d used (weighted): 36%

ACCOUNT  7D   7D RESET     5H  5H RESET     BUDGET                       AGE     SESSIONS  CAP  CELLS    FLAGS
m        1%   11/10 19:00  0%  passed       ?                            12h36!  1/8       ×20  ke,kf,p
i        9%   11/10 09:59  0%  passed       ?                            15h12!  1/2       ×20  ka,kb
j        91%  06/10 04:00  8%  05/10 13:50  91/70% over until Tue 04:00  1m      1/5       ×20  kc,kd
t        69%  10/10 21:00  0%  05/10 16:20  ?                            2h06!   0/9       ×5   t        not-perso,credits=105/100$
budget: m: unmeasured: reading older than 30m — Staffing will not launch on this account
budget: i: unmeasured: reading older than 30m — Staffing will not launch on this account
budget: t: unmeasured: reading older than 30m — Staffing will not launch on this account

! reading older than 1h: Claude Code itself ignores it — pigtail usage --refresh refreshes it
`

// AC1: the research reading, rendered from a perso cell, is the Spec's perso
// block byte for byte; from a work cell or outside any cell, its work block.
func TestUsageGoldenResearchReading(t *testing.T) {
	d, out, errw, home := researchHome(t)
	if err := runUsage(d, false); err != nil {
		t.Fatalf("runUsage: %v (stderr %s)", err, errw)
	}
	if out.String() != researchGolden {
		t.Fatalf("stdout:\n%s\nwant:\n%s", out, researchGolden)
	}
	if errw.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errw)
	}
	for name, cfgDir := range map[string]string{"work cell kf": filepath.Join(home, ".claude-kf"), "no cell": ""} {
		out.Reset()
		d.getenv = func(k string) string {
			if k == "CLAUDE_CONFIG_DIR" {
				return cfgDir
			}
			return ""
		}
		if err := runUsage(d, false); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if out.String() != researchGoldenWork {
			t.Errorf("%s: stdout:\n%s\nwant:\n%s", name, out, researchGoldenWork)
		}
	}
}

func rows(s string) [][]string {
	var out [][]string
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		out = append(out, cols.Split(l, -1))
	}
	return out
}

func TestUsageTextFromPersoCell(t *testing.T) {
	d, out, errw, _ := usageHome(t, true)
	if err := runUsage(d, false); err != nil {
		t.Fatalf("runUsage: %v (stderr %s)", err, errw)
	}
	lines := strings.Split(out.String(), "\n")
	// every account with a known capacity counts, whatever the scope:
	// R2, used: (1+91)×20 + 69×5 = 2185 / 45 = 48.6 → 49 (j and t still flagged not-perso).
	if want := "7d used (weighted): 49%"; lines[0] != want {
		t.Fatalf("head = %q\nwant   %q", lines[0], want)
	}
	if lines[1] != "" || lines[2] != "ACCOUNT  7D   7D RESET     5H  5H RESET     BUDGET                       AGE     SESSIONS  CAP  CELLS  FLAGS" {
		t.Fatalf("header block = %q", lines[1:3])
	}
	got := rows(strings.Join(lines[3:6], "\n"))
	want := [][]string{
		{"j", "91%", "06/10 04:00", "8%", "05/10 13:50", "91/70% over until Tue 04:00", "1m", "0/0", "×20", "kd", "not-perso"},
		{"m", "1%", "11/10 19:00", "0%", "passed", "?", "12h36!", "0/0", "×20", "ke"},
		{"t", "69%", "10/10 21:00", "0%", "05/10 16:20", "?", "2h06!", "0/0", "×5", "t", "not-perso,credits=105/100$"},
	}
	for i := range want {
		if strings.Join(got[i], "|") != strings.Join(want[i], "|") {
			t.Errorf("row %d = %q\nwant    %q", i, got[i], want[i])
		}
	}
	for _, l := range lines {
		if l != strings.TrimRight(l, " ") {
			t.Errorf("trailing spaces in %q", l)
		}
	}
	if !strings.Contains(out.String(), "\n! reading older than 1h: Claude Code itself ignores it — pigtail usage --refresh refreshes it\n") {
		t.Fatalf("no stale footer in\n%s", out)
	}
	if errw.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errw)
	}
}

func TestUsageOutsideCellIsWorkView(t *testing.T) {
	d, out, _, _ := usageHome(t, true)
	d.getenv = func(string) string { return "" } // no CLAUDE_CONFIG_DIR: the bare ~/.claude
	if err := runUsage(d, false); err != nil {
		t.Fatal(err)
	}
	// (1+91)×20 + 69×5 = 2185 / 45 = 48.6 → 49, the same head from any view;
	// the view shows in the table: work = partial SESSIONS*, no not-perso flag.
	lines := strings.Split(out.String(), "\n")
	if lines[0] != "7d used (weighted): 49%" || !strings.Contains(lines[2], "SESSIONS*") || strings.Contains(out.String(), "not-perso") {
		t.Fatalf("outside a cell, want the work view:\n%s", out)
	}
	out.Reset()
	d.getenv = func(k string) string {
		if k == envScope {
			return "perso"
		}
		return ""
	}
	if err := runUsage(d, false); err != nil {
		t.Fatal(err)
	}
	lines = strings.Split(out.String(), "\n")
	if lines[0] != "7d used (weighted): 49%" || strings.Contains(lines[2], "SESSIONS*") || !strings.Contains(out.String(), "not-perso") {
		t.Fatalf("PIGTAIL_SCOPE=perso outside a cell, want the perso view:\n%s", out)
	}
}

func TestUsageLabelsWithoutAccountKey(t *testing.T) {
	d, out, _, _ := usageHome(t, false)
	if err := runUsage(d, false); err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{"kd", "ke", "t"} {
		if !regexp.MustCompile(`(?m)^` + l + `\s{2,}\d+%`).MatchString(out.String()) {
			t.Errorf("no row labelled %q (cell ids) in\n%s", l, out)
		}
	}
}

func TestUsageJSON(t *testing.T) {
	d, out, _, _ := usageHome(t, true)
	if err := runUsage(d, true); err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.TrimRight(out.String(), "\n"), "\n") != 0 {
		t.Fatalf("JSON must be one line:\n%s", out)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, k := range []string{"generated_at", "caller_scope", "weighted_7d_used_percent", "sessions_available", "sessions_complete", "accounts", "unreadable_cells", "warnings"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
	if _, ok := got["best"]; ok {
		t.Error("key best present: the best account is not counted")
	}
	if got["generated_at"] != "2026-10-05T11:32:00Z" || got["weighted_7d_used_percent"] != float64(49) {
		t.Fatalf("generated_at %v weighted %v", got["generated_at"], got["weighted_7d_used_percent"])
	}
	var tAcc map[string]any
	for _, a := range got["accounts"].([]any) {
		if a.(map[string]any)["label"] == "t" {
			tAcc = a.(map[string]any)
		}
	}
	rd := tAcc["reading"].(map[string]any)
	if rd["cap_reached"] != true || len(rd["weekly_scoped"].([]any)) != 1 {
		t.Fatalf("t reading = %v", rd)
	}
	if rd["extra_usage"].(map[string]any)["used_credits"] != float64(10500) {
		t.Fatalf("extra_usage = %v", rd["extra_usage"])
	}
	if sd := rd["seven_day"].(map[string]any); sd["resets_at"] != "2026-10-10T19:00:00.324333Z" {
		t.Fatalf("seven_day.resets_at = %v, want UTC RFC 3339", sd["resets_at"])
	}
}

// AC2: extra_usage and spend carry the cache's JSON values, compacted, with
// nothing HTML-escaped (json.Encoder escapes <>& unless told not to).
func TestUsageJSONKeepsExtraUsageAndSpend(t *testing.T) {
	d, out, _, _ := usageHome(t, true)
	p := filepath.Join(filepath.Dir(filepath.Dir(d.cellsDir)), ".claude-w4", ".claude.json")
	src, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	src = bytes.Replace(src, []byte(`"can_toggle": false}`), []byte(`"can_toggle": false, "note": "R&D <seat>"}`), 1)
	if err := os.WriteFile(p, src, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runUsage(d, true); err != nil {
		t.Fatal(err)
	}
	var want struct {
		Cache struct {
			U struct {
				ExtraUsage json.RawMessage `json:"extra_usage"`
				Spend      json.RawMessage `json:"spend"`
			} `json:"utilization"`
		} `json:"cachedUsageUtilization"`
	}
	if err := json.Unmarshal(src, &want); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Accounts []struct {
			Label   string `json:"label"`
			Reading struct {
				ExtraUsage json.RawMessage `json:"extra_usage"`
				Spend      json.RawMessage `json:"spend"`
			} `json:"reading"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	compact := func(b []byte) string {
		var buf bytes.Buffer
		if err := json.Compact(&buf, b); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	found := false
	for _, a := range got.Accounts {
		if a.Label != "t" {
			continue
		}
		found = true
		if g, w := string(a.Reading.ExtraUsage), compact(want.Cache.U.ExtraUsage); g != w {
			t.Errorf("extra_usage = %s\nwant          %s", g, w)
		}
		if g, w := string(a.Reading.Spend), compact(want.Cache.U.Spend); g != w {
			t.Errorf("spend = %s\nwant    %s", g, w)
		}
	}
	if !found {
		t.Fatal("no account t in the JSON")
	}
	if !strings.Contains(out.String(), `"note":"R&D <seat>"`) {
		t.Fatal("JSON output HTML-escaped the cache's values")
	}
}

func TestUsageNeverReadsRootOrUnlistedProfiles(t *testing.T) {
	d, out, _, asked := usageHome(t, true)
	if err := runUsage(d, false); err != nil {
		t.Fatal(err)
	}
	home := filepath.Dir(filepath.Dir(d.cellsDir))
	want := map[string]bool{
		filepath.Join(home, ".claude-ke", ".claude.json"): true,
		filepath.Join(home, ".claude-kd", ".claude.json"): true,
		filepath.Join(home, ".claude-w4", ".claude.json"): true,
	}
	if len(*asked) != len(want) {
		t.Fatalf("read %q, want exactly the 3 cell files", *asked)
	}
	for _, p := range *asked {
		if !want[p] {
			t.Errorf("read %s — not a cell file (bare ~/.claude.json or ~/.claude-bait?)", p)
		}
	}
	if strings.Contains(out.String(), "99999999") || strings.Contains(out.String(), "88888888") {
		t.Fatal("a bait account reached the output")
	}
}

func TestUsagePartialUnreadableExitsZero(t *testing.T) {
	d, out, errw, _ := usageHome(t, true)
	p := filepath.Join(filepath.Dir(d.cellsDir), "..", ".claude-kd", ".claude.json")
	b, _ := os.ReadFile(p)
	if err := os.WriteFile(p, b[:len(b)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runUsage(d, false); err != nil {
		t.Fatalf("one unreadable cell must not fail the command: %v", err)
	}
	clean := filepath.Clean(p)
	if want := "warning: cell kd: parse " + clean + ": unexpected end of JSON input — not counted\n"; errw.String() != want {
		t.Fatalf("stderr = %q\nwant     %q", errw, want)
	}
	if !strings.Contains(out.String(), "\nunreadable: kd — parse "+clean+": unexpected end of JSON input — not counted\n") {
		t.Fatalf("no unreadable line in\n%s", out)
	}
	if strings.Contains(out.String(), "\nj ") {
		t.Fatal("the unreadable cell's account must not appear as 0 %")
	}
}

func TestUsageAllUnreadableExitsOne(t *testing.T) {
	d, _, errw, _ := usageHome(t, true)
	d.readFile = func(string) ([]byte, error) { return nil, os.ErrPermission }
	err := runUsage(d, false)
	if err == nil || err.Error() != "no cell could be read (3 unreadable)" || exitCode(err) != 1 {
		t.Fatalf("err = %v (exit %d)", err, exitCode(err))
	}
	if strings.Count(errw.String(), "warning: cell ") != 3 {
		t.Fatalf("stderr = %q, want one warning per cell", errw)
	}
}

func TestUsageMissingTable(t *testing.T) {
	d, _, _, _ := usageHome(t, true)
	d.cellsDir = t.TempDir()
	err := runUsage(d, false)
	if err == nil || !strings.HasPrefix(err.Error(), "read "+filepath.Join(d.cellsDir, "claude-cells.yaml")+": ") ||
		!strings.HasSuffix(err.Error(), "see docs/CONFIG.md § claude-cells.yaml") {
		t.Fatalf("err = %v", err)
	}
}

func TestUsageEmptyTable(t *testing.T) {
	d, _, _, _ := usageHome(t, true)
	if err := os.WriteFile(filepath.Join(d.cellsDir, "claude-cells.yaml"), []byte("cells: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runUsage(d, false); err == nil || err.Error() != filepath.Join(d.cellsDir, "claude-cells.yaml")+" lists no cell — see docs/CONFIG.md § claude-cells.yaml" {
		t.Fatalf("err = %v", err)
	}
}

func TestUsageWorkCellRefusesPersoView(t *testing.T) {
	d, _, _, _ := usageHome(t, true)
	workDir := filepath.Join(filepath.Dir(filepath.Dir(d.cellsDir)), ".claude-w4")
	d.getenv = func(k string) string {
		return map[string]string{"CLAUDE_CONFIG_DIR": workDir, envScope: "perso"}[k]
	}
	err := runUsage(d, false)
	if err == nil || !strings.HasPrefix(err.Error(), "--scope perso refused: this session runs on a globex cell") {
		t.Fatalf("err = %v", err)
	}
}

// from a work cell, nothing of the perso cells reaches stdout,
// stderr or the JSON — not their ids, not the word perso — even when one of
// them is unreadable.
func TestUsageWorkViewHidesOtherScopes(t *testing.T) {
	d, out, errw, home := researchHome(t)
	kc := filepath.Join(home, ".claude-kc", ".claude.json")
	b, err := os.ReadFile(kc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kc, b[:100], 0o600); err != nil {
		t.Fatal(err)
	}
	d.getenv = func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return filepath.Join(home, ".claude-kf")
		}
		return ""
	}
	leak := regexp.MustCompile(`perso|\b(ka|kc|ke|p)\b`)
	for _, asJSON := range []bool{false, true} {
		out.Reset()
		errw.Reset()
		if err := runUsage(d, asJSON); err != nil {
			t.Fatalf("json=%v: %v", asJSON, err)
		}
		if m := leak.FindString(out.String()); m != "" {
			t.Errorf("json=%v: stdout leaks %q:\n%s", asJSON, m, out)
		}
		if m := leak.FindString(errw.String()); m != "" {
			t.Errorf("json=%v: stderr leaks %q: %s", asJSON, m, errw)
		}
	}
	out.Reset()
	if err := runUsage(d, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]string{{"i", "kb"}, {"j", "kd"}, {"m", "kf"}, {"t", "t"}} {
		if !regexp.MustCompile(`(?m)^` + want[0] + `\s{2,}.*\s{2,}` + want[1] + `(\s{2,}\S+)?$`).MatchString(out.String()) {
			t.Errorf("account %s: CELLS must be %s:\n%s", want[0], want[1], out)
		}
	}
}

// The text renderer's edge cases, on hand-built Reports: nothing counted,
// a 7-day window with an unknown reset, unknown
// capacity, a no-reading account, a reading without a 7-day window.
func TestRenderUsageTextEdgeCases(t *testing.T) {
	t.Parallel()
	ptr := func(i int) *int { return &i }
	reset := time.Date(2026, 10, 5, 11, 50, 0, 0, time.UTC) // 13:50 CEST
	five := &claudeusage.WindowView{Percent: 8, RawPercent: 8, ResetsAt: &reset}
	cells := func(id string) []claudeusage.AccountCell { return []claudeusage.AccountCell{{ID: id}} }
	for _, tc := range []struct {
		name string
		r    claudeusage.Report
		head string
		row  []string
	}{
		{"nothing counted, no-reading, capacity unknown",
			claudeusage.Report{CallerScope: "perso", Accounts: []claudeusage.Account{
				{Label: "xp", Flags: []string{"no-reading", "unknown-capacity"}, Cells: cells("xp")}}},
			"7d used (weighted): —",
			[]string{"xp", "—", "—", "—", "—", "?", "—", "—", "?", "xp", "no-reading,unknown-capacity"}},
		{"7-day reset unknown",
			claudeusage.Report{CallerScope: "globex", Weighted7dUsed: ptr(60),
				Accounts: []claudeusage.Account{{Label: "j", Capacity: ptr(20), Counted: true, Cells: cells("kc"),
					Reading: &claudeusage.Reading{Cell: "kc", AgeSeconds: 60,
						SevenDay: &claudeusage.WindowView{Percent: 40, RawPercent: 40}, FiveHour: five}}}},
			"7d used (weighted): 60%",
			[]string{"j", "40%", "?", "8%", "05/10 13:50", "?", "1m", "—", "×20", "kc"}},
		{"reading without a 7-day window",
			claudeusage.Report{CallerScope: "perso", Accounts: []claudeusage.Account{{Label: "j", Capacity: ptr(20), Cells: cells("kc"),
				Reading: &claudeusage.Reading{Cell: "kc", AgeSeconds: 60, FiveHour: five}}}},
			"7d used (weighted): —",
			[]string{"j", "—", "—", "8%", "05/10 13:50", "?", "1m", "—", "×20", "kc"}},
		{"5-hour window not started: no reset yet, not unknown",
			claudeusage.Report{CallerScope: "perso", Accounts: []claudeusage.Account{{Label: "k", Capacity: ptr(20), Cells: cells("kt"),
				Reading: &claudeusage.Reading{Cell: "kt", AgeSeconds: 60, FiveHour: &claudeusage.WindowView{}}}}},
			"7d used (weighted): —",
			[]string{"k", "—", "—", "0%", "—", "?", "1m", "—", "×20", "kt"}},
		{"cap: the extra-usage credits spent, not the plan",
			claudeusage.Report{CallerScope: "perso", Accounts: []claudeusage.Account{{Label: "t", Capacity: ptr(5), Cells: cells("tt"),
				Flags: []string{"cap"},
				Reading: &claudeusage.Reading{Cell: "tt", AgeSeconds: 60, FiveHour: five, CapReached: true,
					Spend: json.RawMessage(`{"used":{"amount_minor":10500,"currency":"USD","exponent":2},"limit":{"amount_minor":6000,"currency":"USD","exponent":2}}`)}}}},
			"7d used (weighted): —",
			[]string{"t", "—", "—", "8%", "05/10 13:50", "?", "1m", "—", "×5", "tt", "credits=105/60$"}},
	} {
		var buf bytes.Buffer
		renderUsageText(&buf, tc.r, cest)
		lines := strings.Split(buf.String(), "\n")
		if lines[0] != tc.head {
			t.Errorf("%s: head = %q\nwant         %q", tc.name, lines[0], tc.head)
		}
		if got := rows(lines[3])[0]; strings.Join(got, "|") != strings.Join(tc.row, "|") {
			t.Errorf("%s: row = %q\nwant      %q", tc.name, got, tc.row)
		}
	}
}

// Ended and dead rows are not open; busy is Claude's native status busy or
// shell (a Bash-tool command running: a turn in progress).
func TestSessionRefs(t *testing.T) {
	t.Parallel()
	no, yes := false, true
	end := usageNow
	got := sessionRefs([]pigtail.PeerSession{
		{SessionID: "a", CellID: "kc", Status: "busy"},
		{SessionID: "b", CellID: "kc", Status: "idle", Alive: &yes},
		{SessionID: "c", CellID: "kc", Status: "busy", Alive: &no},
		{SessionID: "d", CellID: "kc", Status: "busy", EndedAt: &end},
		{SessionID: "e", CellID: "kc", Status: "shell"},
		{SessionID: "f", CellID: "kc", Status: "shell", Alive: &no},
	})
	want := []claudeusage.SessionRef{
		{ID: "a", CellID: "kc", Open: true, Busy: true},
		{ID: "b", CellID: "kc", Open: true},
		{ID: "c", CellID: "kc"},
		{ID: "d", CellID: "kc"},
		{ID: "e", CellID: "kc", Open: true, Busy: true},
		{ID: "f", CellID: "kc"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("sessionRefs = %+v\nwant          %+v", got, want)
	}
}

// daemon down → "—" in SESSIONS, one stderr line, the % and the exit code
// unchanged; JSON says unavailable, counts null.
func TestUsageSessionsUnavailable(t *testing.T) {
	d, out, errw, _ := usageHome(t, true)
	d.peerSessions = func() ([]pigtail.PeerSession, error) {
		return nil, fmt.Errorf("dial unix /tmp/x/pigtail.sock: connect: no such file or directory")
	}
	if err := runUsage(d, false); err != nil {
		t.Fatalf("an unreachable daemon must not fail the command: %v", err)
	}
	if want := "warning: sessions unavailable (dial unix /tmp/x/pigtail.sock: connect: no such file or directory) — SESSIONS shown as —\n"; errw.String() != want {
		t.Fatalf("stderr = %q\nwant     %q", errw, want)
	}
	lines := strings.Split(out.String(), "\n")
	if lines[0] != "7d used (weighted): 49%" {
		t.Fatalf("head = %q, the %% must stay", lines[0])
	}
	if got := rows(lines[4])[0]; strings.Join(got[:9], "|") != "m|1%|11/10 19:00|0%|passed|?|12h36!|—|×20" {
		t.Fatalf("m row = %q, want SESSIONS —", got)
	}
	out.Reset()
	errw.Reset()
	if err := runUsage(d, true); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Available *bool `json:"sessions_available"`
		Accounts  []struct {
			Busy *int `json:"sessions_busy"`
			Open *int `json:"sessions_open"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Available == nil || *got.Available || got.Accounts[0].Busy != nil || got.Accounts[0].Open != nil {
		t.Fatalf("JSON = %s, want sessions_available false and null counts", out)
	}

	// From a work cell (kf), daemon down: still SESSIONS* and the partial note.
	d2, out2, _, home := researchHome(t)
	d2.peerSessions = d.peerSessions
	d2.getenv = func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return filepath.Join(home, ".claude-kf")
		}
		return ""
	}
	if err := runUsage(d2, false); err != nil {
		t.Fatal(err)
	}
	tl := strings.Split(strings.TrimRight(out2.String(), "\n"), "\n")
	if !regexp.MustCompile(`\sSESSIONS\*\s`).MatchString(tl[2]) {
		t.Errorf("kf, daemon down: header = %q, want SESSIONS*", tl[2])
	}
	if tl[len(tl)-1] != "* globex-visible sessions only" {
		t.Errorf("kf, daemon down: last line = %q, want the partial-view note", tl[len(tl)-1])
	}
}

// from a cell that is not perso, the daemon's table is partial — the
// header and a note say so, naming the caller's scope as pigtail resolves it.
func TestUsageSessionsPartialViewMarked(t *testing.T) {
	d, out, _, _ := researchHome(t)
	workDir := filepath.Join(filepath.Dir(filepath.Dir(d.cellsDir)), ".claude-kf")
	d.getenv = func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return workDir
		}
		return ""
	}
	if err := runUsage(d, false); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if !regexp.MustCompile(`\sSESSIONS\*\s`).MatchString(lines[2]) {
		t.Fatalf("header = %q, want SESSIONS*", lines[2])
	}
	if last := lines[len(lines)-1]; last != "* globex-visible sessions only" {
		t.Fatalf("last line = %q, want the partial-view note", last)
	}
	out.Reset()
	d.getenv = func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return filepath.Join(filepath.Dir(filepath.Dir(d.cellsDir)), ".claude-ke")
		}
		return ""
	}
	if err := runUsage(d, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "SESSIONS*") || strings.Contains(out.String(), "-visible sessions only") {
		t.Fatalf("perso view is complete, no partial marker:\n%s", out)
	}
}

func TestUsageHelp(t *testing.T) {
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"usage", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	h := buf.String()
	if !regexp.MustCompile(`--json\s+emit the full report as JSON \(per-model weekly limit, extra usage\)`).MatchString(h) {
		t.Fatalf("help:\n%s", h)
	}
	// The exit codes, word for word (whitespace folded: the help is wrapped).
	exit := "Exit 1 when the cell table is missing or empty, when a cell of another scope asks for the perso view (--scope perso), or when no cell could be read."
	if !strings.Contains(strings.Join(strings.Fields(h), " "), exit) {
		t.Fatalf("help lacks %q:\n%s", exit, h)
	}
	// S9 — whitespace folded: the Long wraps "a session\nstarted on…" across lines.
	// t.Errorf, not Fatalf: each assertion reports on its own (red per assertion).
	flat := strings.Join(strings.Fields(h), " ")
	if strings.Contains(flat, "a session started on that account refreshes it") {
		t.Error("stale help text still there")
	}
	if s := `A reading older than 1h is marked "!": Claude Code itself ignores it; pigtail usage --refresh refreshes it.`; !strings.Contains(flat, s) {
		t.Errorf("help lacks %q:\n%s", s, h)
	}
	// --refresh
	if !regexp.MustCompile(`--refresh\s+refresh each account's usage cache first \(claude -p /usage, one account at a time, no tokens\)`).MatchString(h) {
		t.Errorf("help lacks --refresh:\n%s", h)
	}
	if s := "Exit 2 when --refresh failed on at least one account (the whole report is still printed)."; !strings.Contains(flat, s) {
		t.Errorf("help lacks %q:\n%s", s, h)
	}
	if strings.Contains(h, "--json json") || strings.Contains(h, "`") {
		t.Fatalf("cobra read a value name into the help (backticks?):\n%s", h)
	}
}

// (verify round 1): every cell is of another scope and unreadable — from a
// work view nothing is shown, so the error must not count them either:
// exactly "no cell could be read", never "(0 unreadable)" nor a hidden count.
func TestUsageAllHiddenUnreadableNoCount(t *testing.T) {
	d, out, errw, _ := usageHome(t, true)
	home := filepath.Dir(filepath.Dir(d.cellsDir))
	trunc := filepath.Join(home, ".claude-kc", ".claude.json")
	body := maxCellFile(t, "33333333-3333-4333-8333-333333333333", usageNow, 50, "2026-10-06T02:00:00+00:00", 1, "2026-10-05T12:00:00+00:00")
	if err := os.MkdirAll(filepath.Dir(trunc), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trunc, body[:len(body)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	table := "cells:\n" +
		"  ka: { config_dir: " + filepath.Join(home, ".claude-ka-missing") + ", scope: perso, account: i }\n" +
		"  kc: { config_dir: " + filepath.Join(home, ".claude-kc") + ", scope: perso, account: j }\n"
	if err := os.WriteFile(filepath.Join(d.cellsDir, "claude-cells.yaml"), []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	d.getenv = func(string) string { return "" } // outside any cell: work view
	for _, asJSON := range []bool{false, true} {
		out.Reset()
		errw.Reset()
		err := runUsage(d, asJSON)
		if err == nil || err.Error() != "no cell could be read" || exitCode(err) != 1 {
			t.Fatalf("json=%v: err = %v (exit %d), want exactly \"no cell could be read\", exit 1", asJSON, err, exitCode(err))
		}
		leak := regexp.MustCompile(`perso|\b(ka|kc)\b|missing`)
		if m := leak.FindString(out.String() + errw.String() + err.Error()); m != "" {
			t.Fatalf("json=%v: hidden cell leaked %q:\nstdout %s\nstderr %s", asJSON, m, out, errw)
		}
	}
}

// --json lists the accounts in the same order as the text, from any view.
func TestUsageJSONAccountsInDeclarationOrder(t *testing.T) {
	d, out, _, home := researchHome(t)
	for name, cfgDir := range map[string]string{"perso cell ke": filepath.Join(home, ".claude-ke"), "work cell kf": filepath.Join(home, ".claude-kf"), "no cell": ""} {
		out.Reset()
		d.getenv = func(k string) string {
			if k == "CLAUDE_CONFIG_DIR" {
				return cfgDir
			}
			return ""
		}
		if err := runUsage(d, true); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var got struct {
			Accounts []struct {
				Label string `json:"label"`
			} `json:"accounts"`
		}
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		var ls []string
		for _, a := range got.Accounts {
			ls = append(ls, a.Label)
		}
		if strings.Join(ls, " ") != "m i j t" {
			t.Errorf("%s: accounts = %q, want m i j t", name, ls)
		}
		if strings.Contains(out.String(), `"rank"`) {
			t.Errorf("%s: rank shown in the JSON", name)
		}
	}
}

// bumpLauncher fakes claude -p /usage: it rewrites the cell's fetchedAtMs to
// `at`, unless noop names the cell dir's basename (then the cache stays as it
// was) or fail maps it to an error. It records the dirs, in order.
func bumpLauncher(t *testing.T, at time.Time, noop map[string]bool, fail map[string]error, calls *[]string) claudeusage.LaunchFunc {
	return func(_ context.Context, dir string) error {
		*calls = append(*calls, filepath.Base(dir))
		if err := fail[filepath.Base(dir)]; err != nil {
			return err
		}
		if noop[filepath.Base(dir)] {
			return nil
		}
		p := filepath.Join(dir, ".claude.json")
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		m["cachedUsageUtilization"].(map[string]any)["fetchedAtMs"] = at.UnixMilli()
		nb, _ := json.Marshal(m)
		if err := os.WriteFile(p, nb, 0o600); err != nil {
			t.Fatal(err)
		}
		return nil
	}
}

// AC1: after --refresh, no refreshed account is stale; REFRESH reads ok.
func TestUsageRefreshAllOK(t *testing.T) {
	d, out, errw, _ := usageHome(t, true)
	var calls []string
	d.refresh = bumpLauncher(t, usageNow, nil, nil, &calls)
	d.refreshTimeout = time.Minute
	if err := runUsage(d, false); err != nil {
		t.Fatalf("runUsage: %v (stderr %s)", err, errw)
	}
	lines := strings.Split(out.String(), "\n")
	if lines[2] != "ACCOUNT  7D   7D RESET     5H  5H RESET     BUDGET                       AGE  REFRESH  SESSIONS  CAP  CELLS  FLAGS" {
		t.Fatalf("header = %q", lines[2])
	}
	for _, r := range rows(strings.Join(lines[3:6], "\n")) {
		if r[6] != "0m" || r[7] != "ok" {
			t.Errorf("row %q: want AGE 0m, REFRESH ok", r)
		}
	}
	if strings.Contains(out.String(), "!") || strings.Contains(out.String(), "refresh failed") {
		t.Fatalf("stale mark or failure after a full refresh:\n%s", out)
	}
	// from the perso cell, t (work-only account) is launched too
	if got := strings.Join(calls, ","); got != ".claude-kd,.claude-ke,.claude-w4" {
		t.Fatalf("launched %s", got)
	}
	if want := "refreshing cell kd\nrefreshing cell ke\nrefreshing cell t\n"; errw.String() != want {
		t.Fatalf("stderr = %q, want %q", errw, want)
	}
}

// AC2/AC3: t at its cap does not move; the rest of the table is intact, the
// failure is on t's row and in a line, exit 2.
func TestUsageRefreshPartialFailure(t *testing.T) {
	d, out, errw, _ := usageHome(t, true)
	var calls []string
	d.refresh = bumpLauncher(t, usageNow, map[string]bool{".claude-w4": true}, nil, &calls)
	d.refreshTimeout = time.Minute
	err := runUsage(d, false)
	if err == nil || err.Error() != "refresh failed for 1 of 3 accounts" || exitCode(err) != 2 {
		t.Fatalf("err = %v (exit %d)", err, exitCode(err))
	}
	got := rows(strings.Join(strings.Split(out.String(), "\n")[3:6], "\n"))
	want := map[string][2]string{"j": {"0m", "ok"}, "m": {"0m", "ok"}, "t": {"2h06!", "failed"}}
	for _, r := range got {
		if w := want[r[0]]; r[6] != w[0] || r[7] != w[1] {
			t.Errorf("row %q: want AGE %s REFRESH %s", r, w[0], w[1])
		}
	}
	if !strings.Contains(out.String(), "\nrefresh failed: t (cell t) — usage cache unchanged after claude -p /usage\n") {
		t.Fatalf("no failure line:\n%s", out)
	}
	if !strings.Contains(out.String(), "\n! reading older than 1h: Claude Code itself ignores it — pigtail usage --refresh refreshes it\n") {
		t.Fatalf("no stale note for t:\n%s", out)
	}
	if !strings.Contains(errw.String(), "refreshing cell t\n") {
		t.Fatalf("stderr = %q", errw)
	}
}

// AC2: every launch failing (claude missing) is visible on every account, exit 2.
func TestUsageRefreshClaudeMissing(t *testing.T) {
	d, out, _, _ := usageHome(t, true)
	var calls []string
	nf := errors.New("claude not found on PATH (cmux shim skipped)")
	d.refresh = bumpLauncher(t, usageNow, nil, map[string]error{".claude-kd": nf, ".claude-ke": nf, ".claude-w4": nf}, &calls)
	d.refreshTimeout = time.Minute
	err := runUsage(d, false)
	if err == nil || err.Error() != "refresh failed for 3 of 3 accounts" || exitCode(err) != 2 {
		t.Fatalf("err = %v", err)
	}
	if strings.Count(out.String(), "— claude not found on PATH (cmux shim skipped)\n") != 3 {
		t.Fatalf("want 3 failure lines:\n%s", out)
	}
	if !strings.HasPrefix(out.String(), "7d used (weighted): 49%\n") {
		t.Fatalf("the report must still be whole:\n%s", out)
	}
}

// AC2: the JSON carries the verdict per account.
func TestUsageRefreshJSON(t *testing.T) {
	d, out, _, _ := usageHome(t, true)
	var calls []string
	d.refresh = bumpLauncher(t, usageNow, map[string]bool{".claude-w4": true}, nil, &calls)
	d.refreshTimeout = time.Minute
	if err := runUsage(d, true); exitCode(err) != 2 {
		t.Fatalf("exit %d", exitCode(err))
	}
	var rep struct {
		Accounts []struct {
			Label   string         `json:"label"`
			Refresh map[string]any `json:"refresh"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	for _, a := range rep.Accounts {
		switch a.Label {
		case "t":
			if a.Refresh["status"] != "failed" || a.Refresh["error"] != "usage cache unchanged after claude -p /usage" || a.Refresh["cell"] != "t" {
				t.Errorf("t refresh = %v", a.Refresh)
			}
		default:
			if a.Refresh["status"] != "refreshed" || a.Refresh["fetched_after"] != "2026-10-05T11:32:00Z" {
				t.Errorf("%s refresh = %v", a.Label, a.Refresh)
			}
		}
	}
}

// AC2 / gap 1 of review 1: t has a single cell; the launch leaves its file
// truncated (a live session rewriting it), so the final view drops t. The
// failure still shows — a line in text, refresh_failures in JSON — and exits 2.
func TestUsageRefreshAccountLeavesReport(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		d, out, _, _ := usageHome(t, true)
		var calls []string
		bump := bumpLauncher(t, usageNow, nil, nil, &calls)
		d.refresh = func(ctx context.Context, dir string) error {
			if filepath.Base(dir) == ".claude-w4" {
				calls = append(calls, filepath.Base(dir))
				return os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"oauthAccount":`), 0o600)
			}
			return bump(ctx, dir)
		}
		d.refreshTimeout = time.Minute
		err := runUsage(d, asJSON)
		if err == nil || err.Error() != "refresh failed for 1 of 3 accounts" || exitCode(err) != 2 {
			t.Fatalf("json=%v: err = %v (exit %d)", asJSON, err, exitCode(err))
		}
		if !asJSON {
			if !regexp.MustCompile(`\nrefresh failed: t \(cell t\) — re-read failed: parse \S+/\.claude-w4/\.claude\.json: `).MatchString(out.String()) {
				t.Fatalf("no failure line for t, which left the table:\n%s", out)
			}
			continue
		}
		var rep struct {
			Accounts []struct {
				Label string `json:"label"`
			} `json:"accounts"`
			RefreshFailures []map[string]any `json:"refresh_failures"`
		}
		if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
			t.Fatal(err)
		}
		for _, a := range rep.Accounts {
			if a.Label == "t" {
				t.Fatal("t is still listed: the case under test did not happen")
			}
		}
		if len(rep.RefreshFailures) != 1 {
			t.Fatalf("refresh_failures = %v", rep.RefreshFailures)
		}
		f := rep.RefreshFailures[0]
		if e, _ := f["error"].(string); f["label"] != "t" || f["cell"] != "t" || f["status"] != "failed" || !strings.HasPrefix(e, "re-read failed: ") {
			t.Fatalf("refresh_failures[0] = %v", f)
		}
	}
}

// AC9: no --refresh, no refresh key, no REFRESH column.
func TestUsageJSONNoRefreshKey(t *testing.T) {
	d, out, _, _ := usageHome(t, true)
	if err := runUsage(d, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `"refresh"`) || strings.Contains(out.String(), `"refresh_failures"`) {
		t.Fatalf("refresh key without --refresh:\n%s", out)
	}
}

// AC4 / from a work cell and from outside any cell (work view),
// never a perso cell launched, nothing perso shown — in text and in JSON.
// Each pass runs 2 min after the previous one's launches: nothing is fresh,
// so every pass really launches (a pass that launches nothing proves nothing).
func TestUsageRefreshWorkNeverLaunchesPerso(t *testing.T) {
	d, out, errw, home := researchHome(t)
	clock := usageNow
	d.now = func() time.Time { return clock }
	d.refreshTimeout = time.Minute
	leak := regexp.MustCompile(`perso|\b(ka|kc|ke|p)\b`)
	for _, cfg := range []string{filepath.Join(home, ".claude-kf"), ""} { // cell kf, then no CLAUDE_CONFIG_DIR
		d.getenv = func(k string) string {
			if k == "CLAUDE_CONFIG_DIR" {
				return cfg
			}
			return ""
		}
		for _, asJSON := range []bool{false, true} {
			clock = clock.Add(2 * time.Minute)
			var calls []string
			d.refresh = bumpLauncher(t, clock, nil, nil, &calls)
			out.Reset()
			errw.Reset()
			if err := runUsage(d, asJSON); err != nil {
				t.Fatalf("cfg=%q json=%v: %v", cfg, asJSON, err)
			}
			// work cells only, in rank order: kf 0, kb 2, kd 4, t 6
			if got := strings.Join(calls, ","); got != ".claude-kf,.claude-kb,.claude-kd,.claude-w4" {
				t.Errorf("cfg=%q json=%v: launched %s, want kf,kb,kd,t", cfg, asJSON, got)
			}
			if m := leak.FindString(out.String() + errw.String()); m != "" {
				t.Errorf("cfg=%q json=%v: output leaks %q", cfg, asJSON, m)
			}
		}
	}
}

// AC4 / gap 3 of review 2 (E1's rule, resolveScope): outside any cell the
// override is the identity. --scope perso or PIGTAIL_SCOPE=perso makes the
// caller the master: the perso cells are launched. No override: the table's
// default scope, work view, no perso cell launched.
func TestUsageRefreshOutsideCellFollowsOverride(t *testing.T) {
	d, _, _, _ := researchHome(t)
	t.Cleanup(func() { flagScope = "" })
	clock := usageNow
	d.now = func() time.Time { return clock }
	d.refreshTimeout = time.Minute
	for _, c := range []struct{ flag, env, want string }{
		{"", "perso", ".claude-ke,.claude-ka,.claude-kc,.claude-w4"}, // master: ke 1, ka 3, kc 5, t 6
		{"perso", "", ".claude-ke,.claude-ka,.claude-kc,.claude-w4"}, // --scope perso, same
		{"", "", ".claude-kf,.claude-kb,.claude-kd,.claude-w4"},      // no override: kf 0, kb 2, kd 4, t 6
	} {
		flagScope = c.flag
		d.getenv = func(k string) string {
			if k == envScope {
				return c.env
			}
			return "" // no CLAUDE_CONFIG_DIR: outside any cell
		}
		clock = clock.Add(2 * time.Minute) // nothing fresh: every target launches
		var calls []string
		d.refresh = bumpLauncher(t, clock, nil, nil, &calls)
		if err := runUsage(d, false); err != nil {
			t.Fatalf("flag=%q env=%q: %v", c.flag, c.env, err)
		}
		if got := strings.Join(calls, ","); got != c.want {
			t.Errorf("flag=%q env=%q: launched %s, want %s", c.flag, c.env, got, c.want)
		}
	}
}

// AC11 / gap 2 of review 2, from the work cell kf: kb unreadable, ka (perso,
// hidden) readable. Build lists i through ka; i must be failed on kb — never
// skipped —, exit 2, kb never launched, and nothing perso shown (no perso cell
// id or name in stdout, stderr or JSON).
func TestUsageRefreshWorkUnusableOwnCellFails(t *testing.T) {
	d, out, errw, home := researchHome(t)
	if err := os.WriteFile(filepath.Join(home, ".claude-kb", ".claude.json"), []byte(`{"oauthAccount":`), 0o600); err != nil {
		t.Fatal(err)
	}
	clock := usageNow
	d.now = func() time.Time { return clock }
	d.refreshTimeout = time.Minute
	d.getenv = func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return filepath.Join(home, ".claude-kf")
		}
		return ""
	}
	leak := regexp.MustCompile(`perso|\b(ka|kc|ke|p)\b`)
	for _, asJSON := range []bool{false, true} {
		clock = clock.Add(2 * time.Minute)
		var calls []string
		d.refresh = bumpLauncher(t, clock, nil, nil, &calls)
		out.Reset()
		errw.Reset()
		err := runUsage(d, asJSON)
		if err == nil || err.Error() != "refresh failed for 1 of 4 accounts" || exitCode(err) != 2 {
			t.Fatalf("json=%v: err = %v (exit %d)", asJSON, err, exitCode(err))
		}
		if got := strings.Join(calls, ","); got != ".claude-kf,.claude-kd,.claude-w4" {
			t.Errorf("json=%v: launched %s, want kf,kd,t (kb unusable, ka never)", asJSON, got)
		}
		if m := leak.FindString(out.String() + errw.String()); m != "" {
			t.Errorf("json=%v: output leaks %q", asJSON, m)
		}
		if !asJSON {
			if !strings.Contains(out.String(), "\nrefresh failed: i (cell kb) — no usable cell in the caller's scope\n") {
				t.Fatalf("no failure line for i:\n%s", out)
			}
			continue
		}
		var rep struct {
			Accounts []struct {
				Label   string         `json:"label"`
				Refresh map[string]any `json:"refresh"`
			} `json:"accounts"`
		}
		if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, a := range rep.Accounts {
			if a.Label == "i" {
				found = true
				if a.Refresh["status"] != "failed" || a.Refresh["cell"] != "kb" || a.Refresh["error"] != "no usable cell in the caller's scope" {
					t.Errorf("i refresh = %v", a.Refresh)
				}
			}
		}
		if !found {
			t.Fatal("i not listed: the case under test did not happen")
		}
	}
}

// AC2/AC3 / gap 4 of review 2: every launch truncates its cell's file (a live
// session rewriting it), so the final read finds no readable cell. The refresh
// failures win: exit 2 and its Error: line, one "refresh failed:" line per
// account, refresh_failures in JSON — never E1's exit 1 "no cell could be
// read", which only applies before any launch.
func TestUsageRefreshAllCellsLostExitTwo(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		d, out, _, _ := usageHome(t, true)
		var calls []string
		d.refresh = func(_ context.Context, dir string) error {
			calls = append(calls, filepath.Base(dir))
			return os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"oauthAccount":`), 0o600)
		}
		d.refreshTimeout = time.Minute
		err := runUsage(d, asJSON)
		if err == nil || err.Error() != "refresh failed for 3 of 3 accounts" || exitCode(err) != 2 {
			t.Fatalf("json=%v: err = %v (exit %d), want exit 2", asJSON, err, exitCode(err))
		}
		if len(calls) != 3 {
			t.Fatalf("json=%v: launched %v, want kd, ke, t", asJSON, calls)
		}
		if !asJSON {
			lines := regexp.MustCompile(`(?m)^refresh failed: [jmt] \(cell (kd|ke|t)\) — re-read failed: parse `).FindAllString(out.String(), -1)
			if len(lines) != 3 {
				t.Fatalf("want 3 failure lines:\n%s", out)
			}
			continue
		}
		var rep struct {
			Accounts        []map[string]any `json:"accounts"`
			RefreshFailures []map[string]any `json:"refresh_failures"`
		}
		if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
			t.Fatal(err)
		}
		if len(rep.Accounts) != 0 || len(rep.RefreshFailures) != 3 {
			t.Fatalf("accounts = %v, refresh_failures = %v", rep.Accounts, rep.RefreshFailures)
		}
		for _, f := range rep.RefreshFailures {
			if e, _ := f["error"].(string); f["status"] != "failed" || !strings.HasPrefix(e, "re-read failed: ") {
				t.Errorf("refresh_failures entry = %v", f)
			}
		}
	}
}

// AC3 / gap 5 of review 1, end to end through cobra: the --refresh exit 2 and
// a runtime exit 1 print their Error: line and no Usage: block (both leave
// RunE, silenced tree-wide by silenceUsageOnRunE since PR #65 — unchanged for
// exit 1); a misuse still prints Usage:. No real claude: PATH holds none, so
// the real launcher fails before starting any process.
func TestUsageRefreshExitTwoNoUsageBlock(t *testing.T) {
	prev := flagSocket // --socket below writes the global: restore it, as admit_test.go does
	t.Cleanup(func() { flagSocket = prev })
	flagScope = ""
	xdg, cell := t.TempDir(), filepath.Join(t.TempDir(), ".claude-ke")
	table := filepath.Join(xdg, "pigtail", "claude-cells.yaml")
	for _, dir := range []string{cell, filepath.Dir(table)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	body := maxCellFile(t, "11111111-1111-4111-8111-111111111111", time.Now().Add(-2*time.Hour), 1, "2026-10-11T17:00:00.512+00:00", 4, "2026-10-05T03:00:00.5+00:00")
	if err := os.WriteFile(filepath.Join(cell, ".claude.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	writeTable := func(b string) {
		if err := os.WriteFile(table, []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeTable("cells:\n  ke: { config_dir: " + cell + ", scope: perso, account: m }\n")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("CLAUDE_CONFIG_DIR", cell)
	t.Setenv(envScope, "")
	t.Setenv("PATH", t.TempDir())                          // no claude anywhere: ErrClaudeNotFound, nothing started
	dead := filepath.Join(t.TempDir(), "nonexistent.sock") // SESSIONS: daemon unreachable, a warning only
	run := func(args ...string) (string, error) {
		root := newRootCmd()
		var out bytes.Buffer
		root.SetOut(&out) // cobra's Usage: block
		root.SetErr(&out) // cobra's Error: line (the report itself goes to os.Stdout)
		root.SetArgs(append([]string{"--socket", dead}, args...))
		err := root.Execute()
		return out.String(), err
	}
	got, err := run("usage", "--refresh")
	if exitCode(err) != 2 || !strings.Contains(got, "Error: refresh failed for 1 of 1 accounts") || strings.Contains(got, "Usage:") {
		t.Fatalf("exit 2: exit %d, output:\n%s", exitCode(err), got)
	}
	writeTable("cells: {}\n") // runtime exit 1 of E1: unchanged, no Usage: block
	got, err = run("usage", "--refresh")
	if exitCode(err) != 1 || !strings.Contains(got, "Error: ") || !strings.Contains(got, "lists no cell") || strings.Contains(got, "Usage:") {
		t.Fatalf("runtime exit 1: exit %d, output:\n%s", exitCode(err), got)
	}
	got, err = run("usage", "--nope") // misuse: unchanged, Usage: block
	if exitCode(err) != 1 || !strings.Contains(got, "Usage:") {
		t.Fatalf("misuse: exit %d, output:\n%s", exitCode(err), got)
	}
}

// pickHome: cells ka, ke (perso, homes <home>/perso) and kd (work, home
// <home>/work), ranked ka ke kd, every reading fresh at usageNow; m's 7-day
// window renews first in perso. The caller is cell ke unless env says otherwise.
func pickHome(t *testing.T) (usageDeps, map[string]string, string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	testutil.SerialOnly(t) // swaps flagScope
	flagScope = ""
	t.Cleanup(func() { flagScope = "" })
	home := t.TempDir()
	cfg := filepath.Join(home, ".config", "pigtail")
	write := func(rel string, b []byte) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{"perso/proj", "work/proj", "elsewhere"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fresh := usageNow.Add(-time.Minute)
	write(".claude-ka/.claude.json", maxCellFile(t, "22222222-2222-4222-8222-222222222222", fresh, 10, "2026-10-09T08:00:00+00:00", 5, "2026-10-05T14:00:00+00:00"))
	write(".claude-ke/.claude.json", maxCellFile(t, "11111111-1111-4111-8111-111111111111", fresh, 40, "2026-10-07T08:00:00+00:00", 5, "2026-10-05T14:00:00+00:00"))
	write(".claude-kd/.claude.json", maxCellFile(t, "33333333-3333-4333-8333-333333333333", fresh, 20, "2026-10-08T08:00:00+00:00", 5, "2026-10-05T14:00:00+00:00"))
	cell := func(id, sc, letter, home string, rank int) string {
		return fmt.Sprintf("  %s: { config_dir: %s, scope: %s, account: %s, rank: %d, cwd_home: %s }\n",
			id, filepath.Join(filepath.Dir(cfg), "..", ".claude-"+id), sc, letter, rank, home)
	}
	write(".config/pigtail/claude-cells.yaml", []byte("cells:\n"+
		cell("ka", "perso", "i", filepath.Join(home, "perso"), 0)+
		cell("ke", "perso", "m", filepath.Join(home, "perso"), 1)+
		cell("kd", "globex", "j", filepath.Join(home, "work"), 2)))
	var out, errw bytes.Buffer
	env := map[string]string{"CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude-ke")}
	d := usageDeps{
		cellsDir: cfg,
		getenv:   func(k string) string { return env[k] },
		readFile: os.ReadFile,
		now:      func() time.Time { return usageNow },
		loc:      cest,
		out:      &out, errw: &errw,
	}
	return d, env, home, &out, &errw
}

func TestUsagePickCLIScope(t *testing.T) {
	d, _, _, out, errw := pickHome(t)
	flagScope = "perso"
	if err := runUsagePick(d, "", false); err != nil {
		t.Fatalf("pick: %v (stderr %s)", err, errw)
	}
	if out.String() != "ke\n" || errw.Len() != 0 {
		t.Fatalf("stdout %q stderr %q, want ke alone", out, errw)
	}
	// The master cell may pick for another scope.
	out.Reset()
	flagScope = "globex"
	if err := runUsagePick(d, "", true); err != nil {
		t.Fatal(err)
	}
	var ch claudeusage.Choice
	if err := json.Unmarshal(out.Bytes(), &ch); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if ch.Cell.ID != "kd" || ch.Label != "j" || len(ch.Ranking) != 1 || ch.Group != claudeusage.GroupFresh {
		t.Fatalf("choice = %+v", ch)
	}
}

func TestUsagePickCLICwd(t *testing.T) {
	d, env, home, out, errw := pickHome(t)
	delete(env, "CLAUDE_CONFIG_DIR") // the launcher c, outside any cell
	if err := runUsagePick(d, filepath.Join(home, "work", "proj"), false); err != nil {
		t.Fatalf("pick: %v (stderr %s)", err, errw)
	}
	if out.String() != "kd\n" {
		t.Fatalf("stdout %q, want kd", out)
	}
	out.Reset()
	if err := runUsagePick(d, filepath.Join(home, "perso", "proj"), false); err != nil || out.String() != "ke\n" {
		t.Fatalf("perso cwd: %v stdout %q", err, out)
	}
	err := runUsagePick(d, filepath.Join(home, "elsewhere"), false)
	if err == nil || !strings.Contains(err.Error(), "under no cell's home") {
		t.Fatalf("outside every home: err = %v", err)
	}
	flagScope = "perso"
	if err := runUsagePick(d, filepath.Join(home, "perso"), false); err == nil || !strings.Contains(err.Error(), "exactly one of --scope and --cwd") {
		t.Fatalf("both: err = %v", err)
	}
	flagScope = ""
	if err := runUsagePick(d, "", false); err == nil || !strings.Contains(err.Error(), "exactly one of --scope and --cwd") {
		t.Fatalf("neither: err = %v", err)
	}
}

func TestUsagePickCLINoCandidate(t *testing.T) {
	d, _, home, out, errw := pickHome(t)
	if err := os.Remove(filepath.Join(home, ".claude-ka", ".claude.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude-ke", ".claude.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	flagScope = "perso"
	err := runUsagePick(d, "", false)
	if exitCode(err) != 1 {
		t.Fatalf("exit = %d (%v), want 1", exitCode(err), err)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout %q, want nothing", out)
	}
	s := errw.String()
	for _, want := range []string{"warning: rotation: no candidate", "scope perso", "cell ke: not logged in", "cell ka: read "} {
		if !strings.Contains(s, want) {
			t.Errorf("stderr %q lacks %q", s, want)
		}
	}
	if strings.Contains(s, "kd") {
		t.Errorf("stderr %q names a cell of another scope", s)
	}
}

func TestUsagePickCLIRefusesOtherScopeFromCell(t *testing.T) {
	d, env, home, out, _ := pickHome(t)
	env["CLAUDE_CONFIG_DIR"] = filepath.Join(home, ".claude-kd")
	flagScope = "perso"
	err := runUsagePick(d, "", false)
	if err == nil || !strings.Contains(err.Error(), "refused") || out.Len() != 0 {
		t.Fatalf("work cell picking for perso: err %v stdout %q, want a refusal", err, out)
	}
	flagScope = ""
	err = runUsagePick(d, filepath.Join(home, "perso", "proj"), false)
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("work cell, perso cwd: err %v, want a refusal", err)
	}
	flagScope = "globex"
	if err := runUsagePick(d, "", false); err != nil || out.String() != "kd\n" {
		t.Fatalf("own scope: err %v stdout %q", err, out)
	}
}

func TestRotationOffPick(t *testing.T) {
	d, env, _, out, errw := pickHome(t)
	env["PIGTAIL_ROTATION"] = "off"
	flagScope = "perso"
	err := runUsagePick(d, "", false)
	if exitCode(err) != 1 || out.Len() != 0 {
		t.Fatalf("exit %d stdout %q, want 1 and nothing", exitCode(err), out)
	}
	if got := strings.Count(errw.String(), "rotation: off"); got != 1 {
		t.Fatalf("stderr %q, want one rotation: off line", errw)
	}
}

func TestUsagePickCLIHelp(t *testing.T) {
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"usage", "pick", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if h := buf.String(); !strings.Contains(h, "--cwd") || !strings.Contains(h, "--json") || !strings.Contains(h, "config_dir") || !strings.Contains(h, "outside any cell") {
		t.Fatalf("help:\n%s", h)
	}
}

// A12: pigtail usage --refresh never runs claude -p /usage while another
// refresh (the daemon's sweep) holds the lock: nothing launched, said on
// stderr, the report printed from the cache, exit 0.
func TestUsageRefreshSkipsWhenLocked(t *testing.T) {
	d, out, errw, _ := usageHome(t, true)
	var calls []string
	d.refresh = bumpLauncher(t, usageNow, nil, nil, &calls)
	d.refreshTimeout = time.Minute
	unlock, held, err := claudeusage.TryRefreshLock(d.stateDir)
	if err != nil || held {
		t.Fatalf("lock: held=%v err=%v", held, err)
	}
	defer unlock()
	if err := runUsage(d, false); err != nil {
		t.Fatalf("runUsage: %v, want exit 0", err)
	}
	if len(calls) != 0 {
		t.Fatalf("launched %v while locked", calls)
	}
	want := "warning: usage refresh skipped — another refresh is running (" + filepath.Join(d.stateDir, "usage-refresh.lock") + ")\n"
	if !strings.Contains(errw.String(), want) {
		t.Fatalf("stderr = %q, want %q", errw, want)
	}
	if strings.Contains(out.String(), "REFRESH") || !strings.HasPrefix(out.String(), "7d used (weighted):") {
		t.Fatalf("want the cached report, no REFRESH column:\n%s", out)
	}
}

// A lock that cannot be taken refuses the refresh (said, exit 2), never runs
// one unlocked.
func TestUsageRefreshLockErrorRefuses(t *testing.T) {
	d, _, errw, _ := usageHome(t, true)
	var calls []string
	d.refresh = bumpLauncher(t, usageNow, nil, nil, &calls)
	d.refreshTimeout = time.Minute
	d.refreshLock = func(string) (func(), bool, error) { return nil, false, errors.New("refresh lock: permission denied") }
	err := runUsage(d, false)
	if err == nil || exitCode(err) != 2 {
		t.Fatalf("err = %v (exit %d), want exit 2", err, exitCode(err))
	}
	if len(calls) != 0 {
		t.Fatalf("launched %v without the lock", calls)
	}
	if !strings.Contains(errw.String(), "warning: usage refresh not run — refresh lock: permission denied\n") {
		t.Fatalf("stderr = %q", errw)
	}
}

// writeBackground writes the daemon's usage-refresh.json into the deps' state dir.
func writeBackground(t *testing.T, d usageDeps, st claudeusage.BackgroundState) {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(d.stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.stateDir, claudeusage.BackgroundStateName), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func bgState(at time.Time, res ...claudeusage.BackgroundResult) claudeusage.BackgroundState {
	return claudeusage.BackgroundState{At: at, ResultsAt: &at, Results: res}
}

// A12: a failure of the daemon's last background refresh shows in pigtail
// usage (a line, and background_refresh in --json); a success says nothing.
func TestUsageShowsBackgroundRefreshFailure(t *testing.T) {
	d, out, errw, _ := usageHome(t, true)
	fetched := usageNow.Add(-time.Hour)
	writeBackground(t, d, bgState(usageNow.Add(-12*time.Minute),
		claudeusage.BackgroundResult{Label: "m", AccountUUID: "11111111-1111-4111-8111-111111111111", Cell: "ke", CellScope: "perso",
			Status: claudeusage.RefreshFailed, Error: "timed out after 60s", FetchedAfter: &fetched},
		claudeusage.BackgroundResult{Label: "j", AccountUUID: "33333333-3333-4333-8333-333333333333", Cell: "kd", CellScope: "globex",
			Status: claudeusage.RefreshOK, FetchedAfter: &fetched}))
	if err := runUsage(d, false); err != nil {
		t.Fatalf("runUsage: %v (stderr %s)", err, errw)
	}
	if !strings.Contains(out.String(), "\nbackground refresh failed: m (cell ke) — timed out after 60s, 12m ago\n") {
		t.Fatalf("no background failure line:\n%s", out)
	}
	if strings.Count(out.String(), "background refresh failed") != 1 {
		t.Fatalf("a success was shown as a failure:\n%s", out)
	}
	out.Reset()
	if err := runUsage(d, true); err != nil {
		t.Fatal(err)
	}
	var got struct {
		BackgroundRefresh *struct {
			Failures []struct {
				Label, Cell, Error string
				AgeSeconds         int64 `json:"age_seconds"`
			} `json:"failures"`
		} `json:"background_refresh"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.BackgroundRefresh == nil || len(got.BackgroundRefresh.Failures) != 1 {
		t.Fatalf("background_refresh = %+v\n%s", got.BackgroundRefresh, out)
	}
	if f := got.BackgroundRefresh.Failures[0]; f.Label != "m" || f.Cell != "ke" || f.Error != "timed out after 60s" || f.AgeSeconds != 720 {
		t.Fatalf("failure = %+v", f)
	}
	// No state file: nothing, no key.
	if err := os.Remove(filepath.Join(d.stateDir, claudeusage.BackgroundStateName)); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runUsage(d, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "background_refresh") || strings.Contains(errw.String(), "background") {
		t.Fatalf("absent state shown:\n%s\nstderr %s", out, errw)
	}
}

// the sweep's results are seen from the master. From a work cell, a
// failure on a perso cell names the account only (as the work report lists
// it); an account the caller's report does not list is not named at all —
// neither in text nor in --json.
func TestUsageBackgroundRefreshHiddenCell(t *testing.T) {
	d, out, errw, _ := usageHome(t, true)
	home := filepath.Dir(filepath.Dir(d.cellsDir))
	d.getenv = func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return filepath.Join(home, ".claude-kd") // kd, work
		}
		return ""
	}
	writeBackground(t, d, bgState(usageNow.Add(-5*time.Minute),
		claudeusage.BackgroundResult{Label: "j", AccountUUID: "33333333-3333-4333-8333-333333333333", Cell: "kc", CellScope: "perso",
			Status: claudeusage.RefreshFailed, Error: "claude exited 1"},
		claudeusage.BackgroundResult{Label: "m", AccountUUID: "11111111-1111-4111-8111-111111111111", Cell: "ke", CellScope: "perso",
			Status: claudeusage.RefreshFailed, Error: "timed out after 60s"},
		claudeusage.BackgroundResult{Label: "z", AccountUUID: "77777777-7777-4777-8777-777777777777", Cell: "zp", CellScope: "perso",
			Status: claudeusage.RefreshFailed, Error: "zz unreachable"}))
	if err := runUsage(d, false); err != nil {
		t.Fatalf("runUsage: %v (stderr %s)", err, errw)
	}
	for _, want := range []string{"\nbackground refresh failed: j — claude exited 1, 5m ago\n", "\nbackground refresh failed: m — timed out after 60s, 5m ago\n"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("want %q (account only):\n%s", want, out)
		}
	}
	for _, leak := range []string{"kc", "ke", "zp", "failed: z", "zz unreachable"} {
		if strings.Contains(out.String(), leak) {
			t.Errorf("text shows %s:\n%s", leak, out)
		}
	}
	out.Reset()
	if err := runUsage(d, true); err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{`"kc"`, `"ke"`, `"zp"`, "77777777-7777", "zz unreachable"} {
		if strings.Contains(out.String(), leak) {
			t.Errorf("JSON shows %s:\n%s", leak, out)
		}
	}
}

// An unreadable state is said (warning), never read as "no failure"; the
// report is unchanged, exit 0.
func TestUsageBackgroundRefreshUnreadable(t *testing.T) {
	d, out, errw, _ := usageHome(t, true)
	if err := os.MkdirAll(d.stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.stateDir, claudeusage.BackgroundStateName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runUsage(d, false); err != nil {
		t.Fatalf("runUsage: %v", err)
	}
	if !strings.Contains(errw.String(), "warning: background refresh state unreadable — ") {
		t.Fatalf("stderr = %q", errw)
	}
	if !strings.HasPrefix(out.String(), "7d used (weighted):") {
		t.Fatalf("report:\n%s", out)
	}
}

// Launch margin: usage pick --cell / --over-margin.

const (
	pickUUIDI = "22222222-2222-4222-8222-222222222222"
	pickUUIDM = "11111111-1111-4111-8111-111111111111"
)

// saturate rewrites a pickHome cell's reading: 7-day window at p7 % renewing
// at r7 (RFC 3339).
func saturate(t *testing.T, home, id, uuid string, p7 int, r7 string) {
	t.Helper()
	b := maxCellFile(t, uuid, usageNow.Add(-time.Minute), p7, r7, 5, "2026-10-05T14:00:00+00:00")
	if err := os.WriteFile(filepath.Join(home, ".claude-"+id, ".claude.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// marginTrace points the margin sinks at a scratch spawns.jsonl and a
// recording journal.
func marginTrace(t *testing.T, d *usageDeps) (*[]*pigtail.JournalEntry, func() []spawnreg.Entry) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rows := &[]*pigtail.JournalEntry{}
	d.marginSinks = func() launchmargin.Sinks {
		return cliMarginSinks(func(e *pigtail.JournalEntry) error { *rows = append(*rows, e); return nil }, d.errw)
	}
	return rows, func() []spawnreg.Entry {
		es, err := spawnreg.Open(spawnreg.DefaultPath()).Entries()
		if err != nil {
			t.Fatal(err)
		}
		return es
	}
}

func TestUsagePickCellSwitches(t *testing.T) {
	d, _, home, out, errw := pickHome(t)
	saturate(t, home, "ke", pickUUIDM, 97, "2026-10-07T08:00:00+00:00")
	if err := runUsagePick(d, "", false, pickOpts{cell: "ke"}); err != nil {
		t.Fatalf("pick: %v (stderr %s)", err, errw)
	}
	if out.String() != "ka\n" || !strings.Contains(errw.String(), "warning: launch margin: cell ke (account m) is over the margin — launching on ka (account i) instead") {
		t.Fatalf("stdout %q stderr %q, want ka and the switch warning", out, errw)
	}
}

func TestUsagePickCellRefusedExit3(t *testing.T) {
	d, _, home, out, errw := pickHome(t)
	saturate(t, home, "ke", pickUUIDM, 97, "2026-10-07T08:00:00+00:00")
	saturate(t, home, "ka", pickUUIDI, 96, "2026-10-06T08:00:00+00:00")
	err := runUsagePick(d, "", false, pickOpts{cell: "ke"})
	if exitCode(err) != 3 || out.Len() != 0 {
		t.Fatalf("exit %d (%v) stdout %q, want 3 and nothing", exitCode(err), err, out)
	}
	if want := "first release: i at Tue 10:00"; !strings.Contains(err.Error(), want) || !strings.HasPrefix(err.Error(), "usage pick: every account of scope perso is over the margin") {
		t.Fatalf("err %q lacks %q (stderr %s)", err, want, errw)
	}
}

func TestUsagePickOverMarginFlag(t *testing.T) {
	d, _, home, out, _ := pickHome(t)
	saturate(t, home, "ke", pickUUIDM, 97, "2026-10-07T08:00:00+00:00")
	saturate(t, home, "ka", pickUUIDI, 96, "2026-10-06T08:00:00+00:00")
	if err := runUsagePick(d, "", true, pickOpts{cell: "ke", force: true}); err != nil {
		t.Fatalf("pick: %v", err)
	}
	if !strings.Contains(out.String(), `"decision":"forced"`) || !strings.Contains(out.String(), `"requested":"ke"`) {
		t.Fatalf("json = %s", out)
	}
	out.Reset()
	if err := runUsagePick(d, "", false, pickOpts{cell: "ke", force: true}); err != nil || out.String() != "ke\n" {
		t.Fatalf("plain: %v %q", err, out)
	}
}

func TestUsagePickCellOtherScopeAllowed(t *testing.T) {
	d, env, home, out, errw := pickHome(t)
	env["CLAUDE_CONFIG_DIR"] = filepath.Join(home, ".claude-kd") // a work panel
	if err := runUsagePick(d, "", false, pickOpts{cell: "ke"}); err != nil || out.String() != "ke\n" {
		t.Fatalf("work panel, --cell ke: %v stdout %q stderr %q", err, out, errw)
	}
	out.Reset()
	saturate(t, home, "ke", pickUUIDM, 97, "2026-10-07T08:00:00+00:00")
	if err := runUsagePick(d, "", false, pickOpts{cell: "ke"}); err != nil || out.String() != "ka\n" {
		t.Fatalf("guard on perso: %v stdout %q", err, out)
	}
}

func TestUsagePickScopeFormRefusesWhenAllOver(t *testing.T) {
	d, _, home, out, _ := pickHome(t)
	saturate(t, home, "ke", pickUUIDM, 97, "2026-10-07T08:00:00+00:00")
	saturate(t, home, "ka", pickUUIDI, 96, "2026-10-06T08:00:00+00:00")
	flagScope = "perso"
	err := runUsagePick(d, "", false)
	if exitCode(err) != 3 || out.Len() != 0 || !strings.Contains(err.Error(), "--over-margin") {
		t.Fatalf("exit %d (%v) stdout %q, want 3", exitCode(err), err, out)
	}
	if err := runUsagePick(d, "", false, pickOpts{force: true}); err != nil || out.String() != "ka\n" {
		t.Fatalf("--over-margin: %v stdout %q, want the Pick fallback ka", err, out)
	}
}

func TestUsagePickCellExclusiveWithScope(t *testing.T) {
	d, _, home, _, _ := pickHome(t)
	flagScope = "perso"
	if err := runUsagePick(d, "", false, pickOpts{cell: "ke"}); err == nil || !strings.Contains(err.Error(), "--cell") {
		t.Fatalf("--cell with --scope: %v", err)
	}
	flagScope = ""
	if err := runUsagePick(d, filepath.Join(home, "perso"), false, pickOpts{cell: "ke"}); err == nil || !strings.Contains(err.Error(), "--cell") {
		t.Fatalf("--cell with --cwd: %v", err)
	}
}

func TestUsagePickCellUnknown(t *testing.T) {
	d, _, _, out, _ := pickHome(t)
	err := runUsagePick(d, "", false, pickOpts{cell: "zz"})
	if err == nil || !strings.Contains(err.Error(), `unknown cell "zz"`) || !strings.Contains(err.Error(), "ka,kd,ke") || out.Len() != 0 {
		t.Fatalf("unknown cell: %v stdout %q", err, out)
	}
}

func TestUsagePickCellForeignScopeBounded(t *testing.T) {
	d, env, home, out, errw := pickHome(t)
	env["CLAUDE_CONFIG_DIR"] = filepath.Join(home, ".claude-kd") // non-master caller, work
	saturate(t, home, "ke", pickUUIDM, 97, "2026-10-07T08:00:00+00:00")
	if err := runUsagePick(d, "", true, pickOpts{cell: "ke"}); err != nil {
		t.Fatalf("pick: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if len(v) != 5 || v["cell"] != "ka" || v["decision"] != "switched" || v["requested"] != "ke" || v["config_dir"] != filepath.Join(home, ".claude-ka") {
		t.Fatalf("bounded json = %v, want exactly {cell, config_dir, decision, requested, frees_at}, config_dir of the switched-to cell", v)
	}
	if _, ok := v["frees_at"]; !ok {
		t.Fatalf("bounded json lacks frees_at: %v", v)
	}
	if s := errw.String(); !strings.Contains(s, "launching on ka instead") || strings.Contains(s, "account") || strings.Contains(s, "perso") {
		t.Fatalf("bounded warning = %q", s)
	}
	// refusal: the release time, no scope nor account
	saturate(t, home, "ka", pickUUIDI, 96, "2026-10-06T08:00:00+00:00")
	out.Reset()
	err := runUsagePick(d, "", false, pickOpts{cell: "ke"})
	if exitCode(err) != 3 || strings.Contains(err.Error(), "perso") || strings.Contains(err.Error(), "first release: ") || !strings.Contains(err.Error(), "Tue 10:00") {
		t.Fatalf("bounded refusal: exit %d %v", exitCode(err), err)
	}
	// the master gets the full output
	env["CLAUDE_CONFIG_DIR"] = filepath.Join(home, ".claude-ke")
	saturate(t, home, "ka", pickUUIDI, 10, "2026-10-09T08:00:00+00:00")
	out.Reset()
	if err := runUsagePick(d, "", true, pickOpts{cell: "ke"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"ranking"`) || !strings.Contains(out.String(), `"account_uuid"`) || !strings.Contains(out.String(), `"decision":"switched"`) {
		t.Fatalf("master json = %s", out)
	}
	var mv map[string]any
	if err := json.Unmarshal(out.Bytes(), &mv); err != nil {
		t.Fatal(err)
	}
	if mv["config_dir"] != filepath.Join(home, ".claude-ka") {
		t.Fatalf("master top-level config_dir = %v, want %s", mv["config_dir"], filepath.Join(home, ".claude-ka"))
	}
}

func TestUsagePickJSONConfigDirScopeForm(t *testing.T) {
	d, env, home, out, errw := pickHome(t)
	decode := func() map[string]any {
		t.Helper()
		var v map[string]any
		if err := json.Unmarshal(out.Bytes(), &v); err != nil {
			t.Fatalf("json: %v\n%s", err, out)
		}
		return v
	}
	// The master cell picking for another scope: the full form.
	flagScope = "globex"
	if err := runUsagePick(d, "", true); err != nil {
		t.Fatalf("pick: %v (stderr %s)", err, errw)
	}
	if v := decode(); v["config_dir"] != filepath.Join(home, ".claude-kd") {
		t.Fatalf("master config_dir = %v, want .claude-kd", v["config_dir"])
	}
	// A caller in no cell (env of a GUI app) is the bounded viewer.
	delete(env, "CLAUDE_CONFIG_DIR")
	wantKeys := []string{"cell", "config_dir", "decision", "frees_at", "requested"}
	check := func(label string) {
		t.Helper()
		v := decode()
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if !reflect.DeepEqual(keys, wantKeys) || v["config_dir"] != filepath.Join(home, ".claude-ke") {
			t.Fatalf("%s: json = %v, want keys %v and config_dir .claude-ke", label, v, wantKeys)
		}
	}
	out.Reset()
	flagScope = "perso"
	if err := runUsagePick(d, "", true); err != nil {
		t.Fatalf("scope: %v (stderr %s)", err, errw)
	}
	check("--scope perso")
	out.Reset()
	flagScope = ""
	if err := runUsagePick(d, filepath.Join(home, "perso", "proj"), true); err != nil {
		t.Fatalf("cwd: %v (stderr %s)", err, errw)
	}
	check("--cwd")
}

func TestUsagePickTracesSwitchRefusedForced(t *testing.T) {
	d, env, home, out, errw := pickHome(t)
	rows, entries := marginTrace(t, &d)
	// ok: nothing traced
	if err := runUsagePick(d, "", false, pickOpts{cell: "ke"}); err != nil {
		t.Fatal(err)
	}
	if es := entries(); len(es) != 0 || len(*rows) != 0 {
		t.Fatalf("ok traced: %+v %+v", es, *rows)
	}
	saturate(t, home, "ke", pickUUIDM, 97, "2026-10-07T08:00:00+00:00")
	_ = runUsagePick(d, "", false, pickOpts{cell: "ke"}) // switched
	saturate(t, home, "ka", pickUUIDI, 96, "2026-10-06T08:00:00+00:00")
	_ = runUsagePick(d, "", false, pickOpts{cell: "ke"})              // refused
	_ = runUsagePick(d, "", false, pickOpts{cell: "ke", force: true}) // forced
	env["PIGTAIL_ROTATION"] = "off"
	errw.Reset()
	if err := runUsagePick(d, "", false, pickOpts{cell: "ke"}); err != nil { // unmeasured
		t.Fatal(err)
	}
	if !strings.Contains(errw.String(), "warning: launch margin: not measured") {
		t.Fatalf("unmeasured stderr = %q", errw)
	}
	es := entries()
	var got []string
	for _, e := range es {
		if e.Kind != spawnreg.KindMargin {
			t.Fatalf("entry kind %q", e.Kind)
		}
		got = append(got, e.Result+"|"+e.Margin)
	}
	want := []string{"switched|switched ke->ka", "refused|refused perso frees 2026-10-06T08:00:00Z", "forced|forced ke", "unmeasured|unmeasured rotation-off"}
	if strings.Join(got, ";") != strings.Join(want, ";") {
		t.Fatalf("entries = %q, want %q", got, want)
	}
	if len(*rows) != 4 {
		t.Fatalf("journal rows = %d, want 4", len(*rows))
	}
	for _, r := range *rows {
		if r.Scope != "" || r.Kind != "launch-margin" {
			t.Fatalf("journal row %+v, want kind launch-margin and an empty scope", r)
		}
	}
	// a non-master caller's traces are bounded
	delete(env, "PIGTAIL_ROTATION")
	env["CLAUDE_CONFIG_DIR"] = filepath.Join(home, ".claude-kd")
	*rows = nil
	_ = runUsagePick(d, "", false, pickOpts{cell: "ke"}) // refused, bounded
	es = entries()
	last := es[len(es)-1]
	if len(*rows) != 1 || strings.Contains((*rows)[0].Summary, "perso") || strings.Contains((*rows)[0].Summary, "first release: ") ||
		last.Margin != "refused ke frees 2026-10-06T08:00:00Z" {
		t.Fatalf("non-master trace: %+v %+v", *rows, last)
	}
	_ = out
}

// , the launcher's path: usage pick --cell reads launchmargin.Guard, whose
// base behaviour under PIGTAIL_ROTATION=off is to measure nothing: the requested
// cell on stdout, exit 0, a "not measured" warning on stderr. A reading past its
// weekly budget changes none of it.
func TestUsagePickCellRotationOffIgnoresBudget(t *testing.T) {
	d, env, home, out, errw := pickHome(t)
	// 60 % a week from the reset: past the curve, under the 90 % ceiling. Both
	// accounts of the scope, so that Staffing has none left.
	reset := usageNow.Add(6 * 24 * time.Hour).Format(time.RFC3339)
	saturate(t, home, "ke", pickUUIDM, 60, reset)
	saturate(t, home, "ka", pickUUIDI, 60, reset)

	// Positive control: on this reading Staffing refuses for the budget.
	tbl, err := claudeprofile.Load(d.cellsDir)
	if err != nil {
		t.Fatal(err)
	}
	_, _, gerr := launchmargin.GuardScopeStaffing(launchmargin.Deps{Getenv: d.getenv, ReadFile: d.readFile, Cells: tbl,
		Now: d.now, Viewer: launchmargin.Viewer{Master: true}}, "perso")
	var ob *launchmargin.OverBudgetError
	if !errors.As(gerr, &ob) {
		t.Fatalf("Staffing on this reading: %v, want *OverBudgetError", gerr)
	}

	out.Reset()
	errw.Reset()
	env["PIGTAIL_ROTATION"] = "off"
	if err := runUsagePick(d, "", false, pickOpts{cell: "ke"}); err != nil {
		t.Fatalf("pick: %v (stderr %s)", err, errw)
	}
	if out.String() != "ke\n" {
		t.Fatalf("stdout %q, want the requested cell", out)
	}
	if !strings.Contains(errw.String(), "warning: launch margin: not measured") ||
		!strings.Contains(errw.String(), "launching on cell ke") || strings.Contains(errw.String(), "budget") {
		t.Fatalf("stderr %q", errw)
	}
}

// Regressed-zone pin: --scope under PIGTAIL_ROTATION=off exits 1 as before.
func TestUsagePickScopeRotationOffKeepsExit1(t *testing.T) {
	d, env, _, out, _ := pickHome(t)
	env["PIGTAIL_ROTATION"] = "off"
	flagScope = "perso"
	if err := runUsagePick(d, "", false); exitCode(err) != 1 || out.Len() != 0 {
		t.Fatalf("exit %d (%v) stdout %q, want 1 and nothing", exitCode(err), err, out)
	}
}

func TestRenderUsageTextBudgetColumn(t *testing.T) {
	t.Parallel()
	frees := time.Date(2026, 10, 5, 13, 4, 0, 0, time.UTC) // a Monday, 15:04 CEST
	r := claudeusage.Report{CallerScope: "perso", Accounts: []claudeusage.Account{
		{Label: "m", Budget: claudeusage.BudgetView{State: claudeusage.BudgetWithin, BudgetPct: 70, Used: 41, Left: 29, Allowed: 50}},
		{Label: "j", Budget: claudeusage.BudgetView{State: claudeusage.BudgetOver, BudgetPct: 70, Used: 55, Left: 15, Allowed: 40, FreesAt: &frees}},
		{Label: "i", Budget: claudeusage.BudgetView{State: claudeusage.BudgetUnmeasured, BudgetPct: 70, Why: "reading older than 30m"}},
	}}
	var buf bytes.Buffer
	renderUsageText(&buf, r, cest)
	out := buf.String()
	lines := strings.Split(out, "\n")
	head := lines[2]
	if b, a := strings.Index(head, "5H RESET"), strings.Index(head, "BUDGET"); b < 0 || a < b || strings.Index(head, "AGE") < a {
		t.Fatalf("BUDGET is not between 5H RESET and AGE: %q", head)
	}
	for _, want := range []string{"41/70% (left 29)", "55/70% over until Mon 15:04",
		"budget: i: unmeasured: reading older than 30m — Staffing will not launch on this account"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "budget: m:") || strings.Contains(out, "budget: j:") {
		t.Errorf("a warning on a measured account:\n%s", out)
	}
	if got := rows(lines[5])[0]; got[0] != "i" || got[5] != "?" {
		t.Errorf("unmeasured row: %q", got)
	}
}

func TestUsageJSONCarriesBudget(t *testing.T) {
	frees := time.Date(2026, 10, 5, 13, 4, 0, 0, time.UTC)
	b, err := json.Marshal(claudeusage.Account{Label: "j",
		Budget: claudeusage.BudgetView{State: claudeusage.BudgetOver, BudgetPct: 70, Used: 55, Left: 15, Allowed: 40, FreesAt: &frees}})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Budget map[string]any `json:"budget"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"budget_pct", "used", "left", "frees_at"} {
		if _, ok := got.Budget[k]; !ok {
			t.Errorf("budget lacks %q: %s", k, b)
		}
	}
	if got.Budget["state"] != "over" {
		t.Errorf("state = %v", got.Budget["state"])
	}

	// Through the command: every account of the real report carries it.
	d, out, _, _ := usageHome(t, true)
	if err := runUsage(d, true); err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Accounts []struct {
			Label  string `json:"label"`
			Budget *struct {
				State string `json:"state"`
			} `json:"budget"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil || len(rep.Accounts) == 0 {
		t.Fatalf("usage --json: %v\n%s", err, out)
	}
	for _, a := range rep.Accounts {
		if a.Budget == nil || a.Budget.State == "" {
			t.Errorf("account %s has no budget: %s", a.Label, out)
		}
	}
}
