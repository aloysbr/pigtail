package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudeusage"
)

// realAheadOwn is the production reading of the session's own account; init
// below swaps the package-wide seams for refusing fakes, so that no test of
// this package ever reads a real .claude.json or forks a real worker from a
// Stop it did not set up for that.
var realAheadOwn = aheadOwn

// realAheadPick is the production pick, kept for the wiring test.
var realAheadPick = aheadPick

var errAheadSeamUnset = errors.New("test: anticipatory seam not stubbed")

func init() {
	aheadOwn = func(func(string) string) (*claudeprofile.Table, claudeprofile.Cell, claudeusage.Snapshot, claudeusage.Thresholds, error) {
		return nil, claudeprofile.Cell{}, claudeusage.Snapshot{}, claudeusage.Thresholds{}, errAheadSeamUnset
	}
	aheadPick = func(*claudeprofile.Table, claudeprofile.Cell, time.Time) (claudeusage.Choice, string, error) {
		return claudeusage.Choice{}, "", errAheadSeamUnset
	}
	startRotationAhead = func(string, string, int, string, int64) error { return errAheadSeamUnset }
}

const aheadHookSID = "s-ahead"

type aheadStart struct {
	sid, surface string
	afterPID     int
	turn         string
	size         int64
}

type aheadHookFixture struct {
	t                                   *testing.T
	logDir, waitDir, inboxDir, standDir string
	bgDir                               string
	tr                                  string
	snap                                claudeusage.Snapshot
	th                                  claudeusage.Thresholds
	ownErr, pickErr                     error
	group                               claudeusage.PickGroup
	ownCalls, pickCalls                 int
	starts                              []aheadStart
}

func newAheadHook(t *testing.T) *aheadHookFixture {
	t.Helper()
	startRelayDaemon(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	h := &aheadHookFixture{t: t, logDir: t.TempDir(), waitDir: t.TempDir(), inboxDir: t.TempDir(),
		standDir: t.TempDir(), bgDir: t.TempDir(), group: claudeusage.GroupFresh,
		th: claudeusage.Thresholds{SelectPct: 90, SwitchPct: 98}}
	h.tr = aheadHookTranscript(t, "cli", nil)
	h.snap = aheadSnap(10, 98, time.Now().Add(72*time.Hour))
	savedOwn, savedPick, savedStart := aheadOwn, aheadPick, startRotationAhead
	t.Cleanup(func() { aheadOwn, aheadPick, startRotationAhead = savedOwn, savedPick, savedStart })
	aheadOwn = func(func(string) string) (*claudeprofile.Table, claudeprofile.Cell, claudeusage.Snapshot, claudeusage.Thresholds, error) {
		h.ownCalls++
		return &claudeprofile.Table{}, claudeprofile.Cell{ID: "m1", Scope: "globex"}, h.snap, h.th, h.ownErr
	}
	aheadPick = func(*claudeprofile.Table, claudeprofile.Cell, time.Time) (claudeusage.Choice, string, error) {
		h.pickCalls++
		return h.choice(), "acct-m", h.pickErr
	}
	startRotationAhead = func(sid, surface string, afterPID int, turn string, size int64) error {
		h.starts = append(h.starts, aheadStart{sid, surface, afterPID, turn, size})
		return nil
	}
	return h
}

// aheadSnap is a fresh reading of account m: 5h and 7d percents, the 7d reset.
func aheadSnap(five, seven float64, sevenReset time.Time) claudeusage.Snapshot {
	now := time.Now()
	return claudeusage.Snapshot{AccountUUID: "acct-m", HasReading: true, FetchedAt: now,
		FiveHour: &claudeusage.Window{Percent: five, ResetsAt: now.Add(2 * time.Hour)},
		SevenDay: &claudeusage.Window{Percent: seven, ResetsAt: sevenReset}}
}

func (h *aheadHookFixture) choice() claudeusage.Choice {
	r := []claudeusage.Candidate{
		{Label: "j", AccountUUID: "acct-j", Group: h.group, SevenDay: &claudeusage.WindowView{RawPercent: 20}},
		{Label: "m", AccountUUID: "acct-m", Group: claudeusage.GroupOverMargin,
			SevenDay: &claudeusage.WindowView{RawPercent: 98}, Thresholds: h.th},
	}
	return claudeusage.Choice{Candidate: r[0], Ranking: r, Warnings: []string{}}
}

// aheadHookTranscript is a turn p1 of entrypoint ep, answered; extra lines
// are written before it (an earlier turn).
func aheadHookTranscript(t *testing.T, ep string, before []string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	now := time.Now()
	lines := append([]string{}, before...)
	lines = append(lines,
		fmt.Sprintf(`{"type":"user","promptId":"p1","entrypoint":%q,"timestamp":%q,"message":{"role":"user","content":"go"}}`, ep, now.Add(-time.Minute).UTC().Format(time.RFC3339Nano)),
		fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"role":"assistant","content":"done"}}`, now.UTC().Format(time.RFC3339Nano)))
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func (h *aheadHookFixture) env(extra ...string) func(string) string {
	m := map[string]string{"CLAUDE_HOOK_LOG_DIR": h.logDir, "CMUX_SURFACE_ID": strings.ToLower(rotSurface),
		"PIGTAIL_WAIT_DIR": h.waitDir, "PIGTAIL_INBOX_DIR": h.inboxDir, "PIGTAIL_STANDBY_DIR": h.standDir,
		"PIGTAIL_BGWORK_DIR": h.bgDir}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i]] = extra[i+1]
	}
	return envOf(m)
}

func (h *aheadHookFixture) stop(msg string, getenv func(string) string) string {
	h.t.Helper()
	b, _ := json.Marshal(map[string]string{"session_id": aheadHookSID, "hook_event_name": "Stop",
		"transcript_path": h.tr, "cwd": "/w", "last_assistant_message": msg})
	var out bytes.Buffer
	runWaitHook(bytes.NewReader(b), &out, getenv)
	return out.String()
}

// lines are the hook=rotate lines of hooks.jsonl with that outcome.
func (h *aheadHookFixture) lines(outcome string) []map[string]string {
	b, _ := os.ReadFile(filepath.Join(h.logDir, "hooks.jsonl"))
	var out []map[string]string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]string
		if json.Unmarshal([]byte(l), &m) == nil && m["hook"] == "rotate" && (outcome == "" || m["outcome"] == outcome) {
			out = append(out, m)
		}
	}
	return out
}

func writeStampAgo(t *testing.T, path string, ago time.Duration) {
	t.Helper()
	if err := writeStamp(path, time.Now().Add(-ago)); err != nil {
		t.Fatal(err)
	}
}

func TestWaitHookStartsAheadOverSwitch(t *testing.T) {
	h := newAheadHook(t)
	if out := h.stop("Fini.", h.env()); out != "" {
		t.Fatalf("stdout = %q", out)
	}
	st, err := os.Stat(h.tr)
	if err != nil {
		t.Fatal(err)
	}
	want := aheadStart{aheadHookSID, strings.ToLower(rotSurface), os.Getpid(), "p1", st.Size()}
	if len(h.starts) != 1 || h.starts[0] != want {
		t.Fatalf("starts = %+v, want %+v", h.starts, want)
	}
	ls := h.lines("ahead-started")
	if len(ls) != 1 || !strings.Contains(ls[0]["detail"], "window=7d") || !strings.Contains(ls[0]["detail"], "switch=98") {
		t.Fatalf("lines = %+v", h.lines(""))
	}
}

func TestWaitHookAheadThresholds(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	for _, cs := range []struct {
		name   string
		snap   claudeusage.Snapshot
		th     claudeusage.Thresholds
		window string // "" = no start
	}{
		{"default under", aheadSnap(10, 97.9, time.Now().Add(time.Hour)), claudeusage.Thresholds{SelectPct: 90, SwitchPct: 98}, ""},
		{"m at its switch", aheadSnap(10, 78, time.Now().Add(time.Hour)), claudeusage.Thresholds{SelectPct: 70, SwitchPct: 78}, "7d"},
		{"m under its switch", aheadSnap(10, 77.9, time.Now().Add(time.Hour)), claudeusage.Thresholds{SelectPct: 70, SwitchPct: 78}, ""},
		{"5h over", aheadSnap(98, 10, time.Now().Add(time.Hour)), claudeusage.Thresholds{SelectPct: 90, SwitchPct: 98}, "5h"},
		{"reset passed", aheadSnap(10, 99, past), claudeusage.Thresholds{SelectPct: 90, SwitchPct: 98}, ""},
	} {
		t.Run(cs.name, func(t *testing.T) {
			h := newAheadHook(t)
			h.snap, h.th = cs.snap, cs.th
			h.stop("Fini.", h.env())
			if cs.window == "" {
				if len(h.starts) != 0 || h.pickCalls != 0 || len(h.lines("")) != 0 {
					t.Fatalf("starts=%v picks=%d lines=%+v, want nothing", h.starts, h.pickCalls, h.lines(""))
				}
				return
			}
			ls := h.lines("ahead-started")
			if len(h.starts) != 1 || len(ls) != 1 || !strings.Contains(ls[0]["detail"], "window="+cs.window) {
				t.Fatalf("starts=%v lines=%+v, want one at window=%s", h.starts, h.lines(""), cs.window)
			}
		})
	}
}

func TestWaitHookAheadNoReadingNoStart(t *testing.T) {
	h := newAheadHook(t)
	h.snap = claudeusage.Snapshot{AccountUUID: "acct-m"}
	h.stop("Fini.", h.env())
	if len(h.starts) != 0 || h.pickCalls != 0 || len(h.lines("")) != 0 {
		t.Fatalf("starts=%v picks=%d lines=%+v", h.starts, h.pickCalls, h.lines(""))
	}
}

func TestWaitHookAheadNoStart(t *testing.T) {
	type setup func(h *aheadHookFixture) (func(string) string, string)
	marker := func(dir, body string) setup {
		return func(h *aheadHookFixture) (func(string) string, string) {
			d := map[string]string{"wait": h.waitDir, "inbox": h.inboxDir, "standby": h.standDir}[dir]
			if err := os.WriteFile(filepath.Join(d, aheadHookSID), []byte(body), 0o600); err != nil {
				h.t.Fatal(err)
			}
			return h.env(), "Fini."
		}
	}
	cases := []struct {
		name string
		set  setup
	}{
		{"subagent", nil},
		{"sdk-cli", func(h *aheadHookFixture) (func(string) string, string) {
			h.tr = aheadHookTranscript(h.t, "sdk-cli", nil)
			return h.env(), "Fini."
		}},
		{"no surface", func(h *aheadHookFixture) (func(string) string, string) {
			return h.env("CMUX_SURFACE_ID", ""), "Fini."
		}},
		{"rotation off", func(h *aheadHookFixture) (func(string) string, string) {
			return h.env(envRotation, "off"), "Fini."
		}},
		{"standby", marker("standby", "")},
		{"wait done", marker("wait", "done\n")},
		{"wait working", marker("wait", "working\n")},
		{"wait limit", marker("wait", "limit\n")},
		{"wait login", marker("wait", "login\n")},
		{"wait api_error", marker("wait", "api_error\n")},
		{"inbox", marker("inbox", "")},
		{"working line", func(h *aheadHookFixture) (func(string) string, string) {
			return h.env(), "Lancé.\n🔄 En cours — la CI tourne, je reviens seul."
		}},
		{"switched ahead recently", func(h *aheadHookFixture) (func(string) string, string) {
			writeStampAgo(h.t, rotateAheadPath(aheadHookSID), 10*time.Minute)
			return h.env(), "Fini."
		}},
		{"rotated recently", func(h *aheadHookFixture) (func(string) string, string) {
			writeStampAgo(h.t, rotateLastPath(aheadHookSID), 10*time.Minute)
			return h.env(), "Fini."
		}},
	}
	for _, cs := range cases {
		t.Run(cs.name, func(t *testing.T) {
			h := newAheadHook(t)
			if cs.set == nil { // a subagent's Stop
				b, _ := json.Marshal(map[string]string{"session_id": aheadHookSID, "hook_event_name": "Stop",
					"transcript_path": h.tr, "agent_id": "a1", "last_assistant_message": "Fini."})
				runWaitHook(bytes.NewReader(b), &bytes.Buffer{}, h.env())
			} else {
				getenv, msg := cs.set(h)
				h.stop(msg, getenv)
			}
			if len(h.starts) != 0 || h.ownCalls != 0 {
				t.Fatalf("starts=%v ownCalls=%d, want neither", h.starts, h.ownCalls)
			}
		})
	}
}

func TestWaitHookAheadSkipsAfterStopFailure(t *testing.T) {
	now := time.Now()
	rl := func(at, reset time.Time) string {
		return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429,"quotaLimits":{"resetsAt":%d}}`,
			at.UTC().Format(time.RFC3339Nano), reset.Unix())
	}
	for _, cs := range []struct {
		name  string
		tr    func(t *testing.T) string
		wait  string
		start bool
	}{
		{"limit of this turn", func(t *testing.T) string { return limitTranscript(t, "cli", now.Add(3*time.Hour)) }, "", false},
		{"limit already reset", func(t *testing.T) string { return limitTranscript(t, "cli", now.Add(-time.Minute)) }, "", true},
		{"limit of an earlier turn", func(t *testing.T) string {
			old := fmt.Sprintf(`{"type":"user","promptId":"p0","entrypoint":"cli","timestamp":%q}`, now.Add(-10*time.Minute).UTC().Format(time.RFC3339Nano))
			return aheadHookTranscript(t, "cli", []string{old, rl(now.Add(-9*time.Minute), now.Add(3*time.Hour))})
		}, "", true},
		{"limit marker", func(t *testing.T) string { return aheadHookTranscript(t, "cli", nil) }, "limit\n", false},
	} {
		t.Run(cs.name, func(t *testing.T) {
			h := newAheadHook(t)
			h.tr = cs.tr(t)
			if cs.wait != "" {
				_ = os.WriteFile(filepath.Join(h.waitDir, aheadHookSID), []byte(cs.wait), 0o600)
			}
			h.stop("Fini.", h.env())
			if got := len(h.starts) == 1; got != cs.start {
				t.Fatalf("starts=%v, want start=%t", h.starts, cs.start)
			}
			if !cs.start && h.ownCalls != 0 {
				t.Fatalf("ownCalls=%d, want none", h.ownCalls)
			}
		})
	}
}

func TestWaitHookAheadHeldWhenAllOver(t *testing.T) {
	for _, g := range []claudeusage.PickGroup{claudeusage.GroupOverMargin, claudeusage.GroupStale} {
		t.Run(string(g), func(t *testing.T) {
			h := newAheadHook(t)
			h.group = g
			h.stop("Fini.", h.env())
			if len(h.starts) != 0 || len(h.lines("ahead-held")) != 1 {
				t.Fatalf("starts=%v lines=%+v", h.starts, h.lines(""))
			}
			if _, ok := lastAheadRefused(aheadHookSID); !ok {
				t.Fatal("ahead-held wrote no backoff stamp")
			}
		})
	}
}

func TestWaitHookAheadRefusedBackoff(t *testing.T) {
	t.Run("stamp 5 min ago", func(t *testing.T) {
		h := newAheadHook(t)
		writeStampAgo(t, rotateAheadRefusedPath(aheadHookSID), 5*time.Minute)
		h.stop("Fini.", h.env())
		if len(h.starts) != 0 || h.ownCalls != 0 || len(h.lines("")) != 0 {
			t.Fatalf("starts=%v own=%d lines=%+v", h.starts, h.ownCalls, h.lines(""))
		}
	})
	t.Run("stamp 11 min ago", func(t *testing.T) {
		h := newAheadHook(t)
		writeStampAgo(t, rotateAheadRefusedPath(aheadHookSID), 11*time.Minute)
		h.stop("Fini.", h.env())
		if len(h.starts) != 1 {
			t.Fatalf("starts=%v, want the check to go on", h.starts)
		}
	})
	t.Run("two Stops, every target over", func(t *testing.T) {
		h := newAheadHook(t)
		h.group = claudeusage.GroupOverMargin
		h.stop("Fini.", h.env())
		h.stop("Fini.", h.env())
		if n := len(h.lines("ahead-held")); n != 1 || h.ownCalls != 1 {
			t.Fatalf("ahead-held lines=%d own=%d, want 1 and 1", n, h.ownCalls)
		}
	})
	t.Run("the limit mode ignores the stamp", func(t *testing.T) {
		h := newAheadHook(t)
		got := stubStartRotation(t, nil)
		writeStampAgo(t, rotateAheadRefusedPath("s-rot"), time.Minute)
		runWaitHook(strings.NewReader(limitPayload(limitTranscript(t, "cli", time.Now().Add(time.Hour)), "")), &bytes.Buffer{}, h.env())
		if len(*got) != 1 || len(h.starts) != 0 {
			t.Fatalf("limit starts=%v ahead starts=%v", *got, h.starts)
		}
	})
}

// twinTable is a cell table of account m in two cells (m1, m2) and of
// account j in one (j1), all of one scope; the session runs in m1.
func twinTable(t *testing.T) (*claudeprofile.Table, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	dirs := map[string]string{}
	body := "cells:\n"
	for _, c := range []struct{ id, acct string }{{"m1", "m"}, {"m2", "m"}, {"j1", "j"}} {
		dirs[c.id] = filepath.Join(dir, "cell-"+c.id)
		body += "  " + c.id + ": { config_dir: " + dirs[c.id] + ", scope: globex, account: " + c.acct + ", switch_pct: 98 }\n"
	}
	if err := os.WriteFile(filepath.Join(dir, claudeprofile.Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tbl, err := claudeprofile.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return tbl, dirs
}

func TestWaitHookAheadOwnFreshestOfTwinCells(t *testing.T) {
	now := time.Now()
	old, fresh := now.Add(-2*time.Hour), now.Add(-time.Minute)
	for _, cs := range []struct {
		name         string
		m1, m2       claudeusage.Snapshot
		start, picks bool
	}{
		{"old 50 then fresh 98", aheadSnapAt(50, old), aheadSnapAt(98, fresh), true, true},
		{"old 98 then fresh 50", aheadSnapAt(98, old), aheadSnapAt(50, fresh), false, false},
		{"twin on another account", aheadSnapAt(50, old), func() claudeusage.Snapshot {
			s := aheadSnapAt(98, fresh)
			s.AccountUUID = "acct-x"
			return s
		}(), false, false},
	} {
		t.Run(cs.name, func(t *testing.T) {
			h := newAheadHook(t)
			tbl, dirs := twinTable(t)
			aheadOwn = func(getenv func(string) string) (*claudeprofile.Table, claudeprofile.Cell, claudeusage.Snapshot, claudeusage.Thresholds, error) {
				h.ownCalls++
				return realAheadOwn(getenv)
			}
			savedLoad, savedRead := rotLoadTable, aheadReadCell
			t.Cleanup(func() { rotLoadTable, aheadReadCell = savedLoad, savedRead })
			rotLoadTable = func() (*claudeprofile.Table, error) { return tbl, nil }
			reads := map[string]int{}
			aheadReadCell = func(c claudeusage.CellInput) claudeusage.Snapshot {
				reads[c.ConfigDir]++
				switch c.ConfigDir {
				case dirs["m1"]:
					return cs.m1
				case dirs["m2"]:
					return cs.m2
				}
				return claudeusage.Snapshot{AccountUUID: "acct-j", HasReading: true, FetchedAt: now}
			}
			h.stop("Fini.", h.env("CLAUDE_CONFIG_DIR", dirs["m1"]))
			if got := len(h.starts) == 1; got != cs.start {
				t.Fatalf("starts=%v, want start=%t", h.starts, cs.start)
			}
			if got := h.pickCalls > 0; got != cs.picks {
				t.Fatalf("picks=%d, want picked=%t", h.pickCalls, cs.picks)
			}
			if reads[dirs["j1"]] != 1 || reads[dirs["m1"]] != 1 || reads[dirs["m2"]] != 1 {
				t.Fatalf("reads = %v, want each cell once (twins are found by accountUuid)", reads)
			}
		})
	}
}

func aheadSnapAt(seven float64, fetched time.Time) claudeusage.Snapshot {
	s := aheadSnap(10, seven, time.Now().Add(72*time.Hour))
	s.FetchedAt = fetched
	return s
}

func TestWaitHookAheadOwnUnderSwitchReadsTheTable(t *testing.T) {
	h := newAheadHook(t)
	dir := t.TempDir()
	m1, j1 := filepath.Join(dir, "cell-m1"), filepath.Join(dir, "cell-j1")
	body := "cells:\n  m1: { config_dir: " + m1 + ", scope: globex, account: m }\n  j1: { config_dir: " + j1 + ", scope: globex, account: j }\n"
	if err := os.WriteFile(filepath.Join(dir, claudeprofile.Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tbl, err := claudeprofile.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	aheadOwn = realAheadOwn
	savedLoad, savedRead := rotLoadTable, aheadReadCell
	t.Cleanup(func() { rotLoadTable, aheadReadCell = savedLoad, savedRead })
	rotLoadTable = func() (*claudeprofile.Table, error) { return tbl, nil }
	reads := 0
	aheadReadCell = func(c claudeusage.CellInput) claudeusage.Snapshot {
		reads++
		if c.ConfigDir != m1 {
			return claudeusage.Snapshot{AccountUUID: "acct-j", HasReading: true, FetchedAt: time.Now()}
		}
		return aheadSnap(10, 50, time.Now().Add(time.Hour))
	}
	h.stop("Fini.", h.env("CLAUDE_CONFIG_DIR", m1))
	if reads != 2 || h.pickCalls != 0 || len(h.lines("")) != 0 {
		t.Fatalf("reads=%d picks=%d lines=%+v", reads, h.pickCalls, h.lines(""))
	}
}

func TestWaitHookAheadPickErrorFailsOpen(t *testing.T) {
	h := newAheadHook(t)
	h.ownErr = errors.New("table unreadable")
	if out := h.stop("Fini.", h.env()); out != "" || len(h.starts) != 0 || h.pickCalls != 0 {
		t.Fatalf("own error: out=%q starts=%v picks=%d", out, h.starts, h.pickCalls)
	}
	h.ownErr, h.pickErr = nil, errors.New("pick failed")
	if out := h.stop("Fini.", h.env()); out != "" || len(h.starts) != 0 || len(h.lines("")) != 0 {
		t.Fatalf("pick error: out=%q starts=%v lines=%+v", out, h.starts, h.lines(""))
	}
}

func TestWaitHookStopFailureStillRotatesAtLimit(t *testing.T) {
	h := newAheadHook(t)
	got := stubStartRotation(t, nil)
	runWaitHook(strings.NewReader(limitPayload(limitTranscript(t, "cli", time.Now().Add(time.Hour)), "")), &bytes.Buffer{}, h.env())
	if len(*got) != 1 || len(h.starts) != 0 || h.ownCalls != 0 {
		t.Fatalf("limit=%v ahead=%v own=%d", *got, h.starts, h.ownCalls)
	}
}

// A twin is a cell logged into the same accountUuid, whatever its account
// letter says: m2 is lettered j but holds account m's fresher reading.
func TestWaitHookAheadTwinsGroupedByAccountUuid(t *testing.T) {
	h := newAheadHook(t)
	dir := t.TempDir()
	m1, m2 := filepath.Join(dir, "cell-m1"), filepath.Join(dir, "cell-m2")
	body := "cells:\n  m1: { config_dir: " + m1 + ", scope: globex, account: m }\n  m2: { config_dir: " + m2 + ", scope: globex, account: j }\n"
	if err := os.WriteFile(filepath.Join(dir, claudeprofile.Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tbl, err := claudeprofile.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	aheadOwn = realAheadOwn
	savedLoad, savedRead := rotLoadTable, aheadReadCell
	t.Cleanup(func() { rotLoadTable, aheadReadCell = savedLoad, savedRead })
	rotLoadTable = func() (*claudeprofile.Table, error) { return tbl, nil }
	now := time.Now()
	aheadReadCell = func(c claudeusage.CellInput) claudeusage.Snapshot {
		if c.ConfigDir == m1 {
			return aheadSnapAt(50, now.Add(-2*time.Hour))
		}
		return aheadSnapAt(99, now.Add(-time.Minute))
	}
	h.stop("Fini.", h.env("CLAUDE_CONFIG_DIR", m1))
	if len(h.starts) != 1 {
		t.Fatalf("starts=%v lines=%+v, want the same-uuid twin's fresher reading to count", h.starts, h.lines(""))
	}
}

// Over its switch by its own reading but without a target, whatever the
// reason, the hook says it once and stamps the backoff.
func TestWaitHookAheadHeldOnAnyNoTargetReason(t *testing.T) {
	for _, cs := range []struct {
		name   string
		ranked bool
		pct    float64
		reason string
	}{
		{"source under its switch in the table", true, 50, claudeusage.AheadSourceUnder},
		{"source absent from the table", false, 0, claudeusage.AheadSourceAbsent},
	} {
		t.Run(cs.name, func(t *testing.T) {
			h := newAheadHook(t)
			aheadPick = func(*claudeprofile.Table, claudeprofile.Cell, time.Time) (claudeusage.Choice, string, error) {
				h.pickCalls++
				r := []claudeusage.Candidate{{Label: "j", AccountUUID: "acct-j", Group: claudeusage.GroupFresh,
					SevenDay: &claudeusage.WindowView{RawPercent: 20}}}
				if cs.ranked {
					r = append(r, claudeusage.Candidate{Label: "m", AccountUUID: "acct-m", Group: claudeusage.GroupOverMargin,
						SevenDay: &claudeusage.WindowView{RawPercent: cs.pct}, Thresholds: h.th})
				}
				return claudeusage.Choice{Candidate: r[0], Ranking: r}, "acct-m", nil
			}
			h.stop("Fini.", h.env())
			ls := h.lines("ahead-held")
			if len(h.starts) != 0 || len(ls) != 1 || !strings.Contains(ls[0]["detail"], "reason="+cs.reason) {
				t.Fatalf("starts=%v lines=%+v", h.starts, h.lines(""))
			}
			if _, ok := lastAheadRefused(aheadHookSID); !ok {
				t.Fatal("no backoff stamp")
			}
			h.stop("Fini.", h.env())
			if h.ownCalls != 1 || h.pickCalls != 1 {
				t.Fatalf("second Stop re-read the table: own=%d picks=%d", h.ownCalls, h.pickCalls)
			}
		})
	}
}

// The real aheadPick, over a temp table and real .claude.json files: the
// source at 98 % and a fresh target at 20 % start a worker for the target.
func TestWaitHookAheadRealPickWiring(t *testing.T) {
	h := newAheadHook(t)
	dir := t.TempDir()
	m1, j1 := filepath.Join(dir, "cell-m1"), filepath.Join(dir, "cell-j1")
	body := "cells:\n  m1: { config_dir: " + m1 + ", scope: globex, account: m }\n  j1: { config_dir: " + j1 + ", scope: globex, account: j }\n"
	if err := os.WriteFile(filepath.Join(dir, claudeprofile.Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tbl, err := claudeprofile.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	write := func(cfg, uuid string, pct int) {
		if err := os.MkdirAll(cfg, 0o700); err != nil {
			t.Fatal(err)
		}
		reset := now.Add(72 * time.Hour).UTC().Format(time.RFC3339Nano)
		js := fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q,"organizationRateLimitTier":"default_claude_max_20x","userRateLimitTier":null},`+
			`"cachedUsageUtilization":{"fetchedAtMs":%d,"accountUuid":%q,"utilization":{"five_hour":{"utilization":5,"resets_at":%q},"seven_day":{"utilization":%d,"resets_at":%q}}}}`,
			uuid, now.UnixMilli(), uuid, now.Add(2*time.Hour).UTC().Format(time.RFC3339Nano), pct, reset)
		if err := os.WriteFile(filepath.Join(cfg, ".claude.json"), []byte(js), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(m1, "acct-m", 98)
	write(j1, "acct-j", 20)
	aheadOwn, aheadPick = realAheadOwn, realAheadPick
	savedLoad := rotLoadTable
	t.Cleanup(func() { rotLoadTable = savedLoad })
	rotLoadTable = func() (*claudeprofile.Table, error) { return tbl, nil }
	h.stop("Fini.", h.env("CLAUDE_CONFIG_DIR", m1))
	ls := h.lines("ahead-started")
	if len(h.starts) != 1 || len(ls) != 1 || !strings.Contains(ls[0]["detail"], "to=j ") {
		t.Fatalf("starts=%v lines=%+v", h.starts, h.lines(""))
	}
}
