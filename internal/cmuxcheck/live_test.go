package cmuxcheck

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
)

// fixtureProcs is the claude-token processes of a read-only ps of NIGHTLY
// 0.65 tabs (claudesession/testdata/ps-cmux-065.txt).
func fixtureProcs(t *testing.T) []claudesession.Proc {
	t.Helper()
	b, err := os.ReadFile("../claudesession/testdata/ps-cmux-065.txt")
	if err != nil {
		t.Fatal(err)
	}
	return claudesession.ParseClaudeProcs(b, -1)
}

// world065 puts the fixture on cmux surfaces: S-065 (claude 71731 and its two
// hook children), S-066 (claude 61096 and its two); only the claudes have a
// native file.
func world065(t *testing.T) *world {
	w := newWorld()
	start := tNow.Add(-time.Hour)
	for _, p := range fixtureProcs(t) {
		sf := "S-065"
		if p.PID < 70000 {
			sf = "S-066"
		}
		w.procs = append(w.procs, p)
		w.env[p.PID] = "claude CMUX_SURFACE_ID=" + sf + " CLAUDE_CONFIG_DIR=/cfg/a"
		w.screens[sf] = screenFile(t, "screen-idle-empty.txt")
		if p.PID == 71731 || p.PID == 61096 {
			w.native[p.PID] = claudesession.Native{PID: p.PID, SessionID: "sid-" + sf, Status: "idle", Kind: "interactive",
				StatusUpdatedAt: tNow.Add(-time.Hour).UnixMilli(), StartedAt: start.UnixMilli()}
		}
	}
	return w
}

func pidsOf(m map[string][]LiveClaude, surface string) []int {
	var out []int
	for _, lc := range m[surface] {
		out = append(out, lc.PID)
	}
	return out
}

func TestLiveDropsCmuxHookChildren(t *testing.T) {
	w := world065(t)
	got, err := Live(w.deps())
	if err != nil {
		t.Fatal(err)
	}
	if p := pidsOf(got, "S-065"); !reflect.DeepEqual(p, []int{71731}) {
		t.Fatalf("S-065 pids %v, want only the claude 71731", p)
	}
	if p := pidsOf(got, "S-066"); !reflect.DeepEqual(p, []int{61096}) {
		t.Fatalf("S-066 pids %v, want only the claude 61096", p)
	}
}

func TestProbeTargetsFindsSessionOnCmux065(t *testing.T) {
	w := world065(t)
	got := probeTargetsOf(t, w)
	if len(got) != 2 || got[0].Surface != "S-065" || got[0].PID != 71731 || got[1].Surface != "S-066" || got[1].PID != 61096 {
		t.Fatalf("targets %+v, want both 0.65 sessions", got)
	}
}

func liveOf(t *testing.T, w *world) map[string][]LiveClaude {
	t.Helper()
	got, err := Live(w.deps())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// addBare puts a claude-token process on a surface with its ancestors; a
// native file only when sid is not empty.
func addBare(t *testing.T, w *world, pid int, surface, sid string, ancestors ...int) {
	t.Helper()
	w.procs = append(w.procs, claudesession.Proc{PID: pid, Args: "claude", Ancestors: ancestors})
	w.env[pid] = "claude CMUX_SURFACE_ID=" + surface + " CLAUDE_CONFIG_DIR=/cfg/a"
	if sid != "" {
		w.native[pid] = claudesession.Native{PID: pid, SessionID: sid, Status: "idle", Kind: "interactive"}
	}
}

// Guard (green on the base): two unrelated claudes on one surface both stay,
// one of them with no native file and no ancestry (only a native-less
// descendant is dropped), and ProbeTargets still refuses the surface.
func TestLiveKeepsTwoUnrelatedClaudesOnSurface(t *testing.T) {
	w := newWorld()
	addBare(t, w, 10, "S-A", "sid-10")
	addBare(t, w, 11, "S-A", "")
	w.screens["S-A"] = screenFile(t, "screen-idle-empty.txt")
	if p := pidsOf(liveOf(t, w), "S-A"); !reflect.DeepEqual(p, []int{10, 11}) {
		t.Fatalf("pids %v, want both", p)
	}
	if got := probeTargetsOf(t, w); len(got) != 0 {
		t.Fatalf("targets %+v, want none (two claudes on the surface)", got)
	}
}

// Guard: a descendant of a process of another surface is kept.
func TestLiveKeepsDescendantOnOtherSurface(t *testing.T) {
	w := newWorld()
	addBare(t, w, 20, "S-A", "sid-20")
	addBare(t, w, 21, "S-B", "", 20)
	if p := pidsOf(liveOf(t, w), "S-B"); !reflect.DeepEqual(p, []int{21}) {
		t.Fatalf("S-B pids %v, want 21 kept", p)
	}
}

// Guard: a wrapper without a native file whose child (with one) is the real
// claude: the child stays.
func TestLiveKeepsNativeChildOfWrapper(t *testing.T) {
	w := newWorld()
	addBare(t, w, 30, "S-A", "")
	addBare(t, w, 31, "S-A", "sid-31", 30)
	if p := pidsOf(liveOf(t, w), "S-A"); !reflect.DeepEqual(p, []int{30, 31}) {
		t.Fatalf("pids %v, want the wrapper and its native child", p)
	}
}
