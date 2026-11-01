package cmuxsync

import (
	"testing"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/space"
)

// folderTargets classifies each workspace (D31, D36): bound, off-topic,
// young, or expired.

var targetFolders = []string{"Hedge", "Terrace", space.NoSubject}

func targetOf(t *testing.T, f Filing) (string, bool) {
	t.Helper()
	got := folderTargets([]string{"N"}, nil, nil, nil, nil, f, targetFolders)
	v, ok := got["N"]
	return v, ok
}

func TestTargetsBoundWins(t *testing.T) {
	t.Parallel()
	got, ok := targetOf(t, Filing{Bound: map[string]string{"N": "Terrace"}, OffTopic: map[string]string{"N": ""}, Young: map[string]bool{"N": true}})
	if !ok || got != "Terrace" {
		t.Fatalf("target = %q, %v", got, ok)
	}
}

func TestTargetsBoundWithoutSpaceIsNoSubject(t *testing.T) {
	t.Parallel()
	got, ok := targetOf(t, Filing{Bound: map[string]string{"N": ""}, Young: map[string]bool{"N": true}})
	if !ok || got != space.NoSubject {
		t.Fatalf("bound without a space is qualified, no grace: %q, %v", got, ok)
	}
}

func TestTargetsOffTopicBeatsYoung(t *testing.T) {
	t.Parallel()
	got, ok := targetOf(t, Filing{OffTopic: map[string]string{"N": ""}, Young: map[string]bool{"N": true}})
	if !ok || got != space.NoSubject {
		t.Fatalf("D36: %q, %v", got, ok)
	}
}

// D1 (sub_x000000000000019): an off-topic declaration whose resolved space
// is a folder goes there, even young; a space that is no folder, or none,
// files it in « Inbox » at once (D36).
func TestTargetsOffTopicSpace(t *testing.T) {
	t.Parallel()
	got, ok := targetOf(t, Filing{OffTopic: map[string]string{"N": "Terrace"}, Young: map[string]bool{"N": true}})
	if !ok || got != "Terrace" {
		t.Fatalf("off-topic in Terrace, young: %q, %v", got, ok)
	}
}

func TestTargetsOffTopicSpaceNotFolder(t *testing.T) {
	t.Parallel()
	for _, sp := range []string{"Garden", space.Standby} { // Garden is not in targetFolders
		if got, ok := targetOf(t, Filing{OffTopic: map[string]string{"N": sp}}); !ok || got != space.NoSubject {
			t.Fatalf("space %q (no folder): %q, %v, want « Inbox »", sp, got, ok)
		}
	}
}

func TestTargetsOffTopicNoSpace(t *testing.T) {
	t.Parallel()
	if got, ok := targetOf(t, Filing{OffTopic: map[string]string{"N": ""}, Young: map[string]bool{"N": true}}); !ok || got != space.NoSubject {
		t.Fatalf("D36: %q, %v", got, ok)
	}
}

func TestTargetsOffTopicSpaceBoundAndStandbyWin(t *testing.T) {
	t.Parallel()
	if got, _ := targetOf(t, Filing{Bound: map[string]string{"N": "Hedge"}, OffTopic: map[string]string{"N": "Terrace"}}); got != "Hedge" {
		t.Fatalf("bound wins over an off-topic space: %q", got)
	}
	f := Filing{Standby: map[string]bool{"N": true}, OffTopic: map[string]string{"N": "Terrace"}}
	if got := folderTargets([]string{"N"}, nil, nil, nil, nil, f, standbyFolders); got["N"] != space.Standby {
		t.Fatalf("Standby wins over an off-topic space: %q", got["N"])
	}
}

func TestTargetsYoungNeverFiled(t *testing.T) {
	t.Parallel()
	if got, ok := targetOf(t, Filing{Young: map[string]bool{"N": true}}); ok {
		t.Fatalf("young unbound is not filed, got %q", got)
	}
	// A young member of a collapsed « Terrace »: no add, no create, stays.
	sys := g("G-S", "Terrace", "GS", true, "A", "N")
	sys.Collapsed = true
	order := []string{"GS", "A", "N"}
	tg := folderTargets(order, []cmux.Group{sys}, nil, nil, nil,
		Filing{Bound: map[string]string{"A": "Terrace"}, Young: map[string]bool{"N": true}}, targetFolders)
	ops := Arrange(ArrangeInput{Folders: targetFolders, Order: order, Groups: []cmux.Group{sys}, Target: tg, Hold: map[string]bool{"N": true}})
	if len(ops) != 0 {
		t.Fatalf("ops = %+v, want none", ops)
	}
}

func TestTargetsExpiredIsNoSubject(t *testing.T) {
	t.Parallel()
	got, ok := targetOf(t, Filing{})
	if !ok || got != space.NoSubject {
		t.Fatalf("D7 after the grace: %q, %v", got, ok)
	}
}

func TestTargetsExclusionsUnchanged(t *testing.T) {
	t.Parallel()
	pinnedGroup := g("G-P", "Mine", "PA", false, "PM")
	pinnedGroup.Pinned = true
	gen := g("G-G", "Whatever", "GEN", true)
	order := []string{"PIN", "PIN2", "PA", "PM", "GEN", "PEND", "HOST"}
	f := Filing{Bound: map[string]string{"PIN": "Terrace", "PM": "Terrace"}, OffTopic: map[string]string{"PEND": ""}}
	got := folderTargets(order, []cmux.Group{pinnedGroup, gen}, map[string]bool{"PIN": true, "PIN2": true}, nil, map[string]bool{"PEND": true}, f, targetFolders)
	for _, ws := range []string{"PIN2", "PA", "PM", "GEN", "PEND"} {
		if v, ok := got[ws]; ok {
			t.Fatalf("%s must stay out of the map, got %q", ws, v)
		}
	}
	if got["PIN"] != "Terrace" { // inverted (D3 of sub_x000000000000014): a bound pinned tab is filed
		t.Fatalf("bound pinned PIN = %q, want Terrace", got["PIN"])
	}
	if got["HOST"] != space.NoSubject {
		t.Fatalf("HOST = %q", got["HOST"])
	}
	// A generated anchor that hosts a session is filed like any workspace.
	got = folderTargets([]string{"GEN"}, []cmux.Group{gen}, nil, map[string]bool{"GEN": true}, nil, Filing{}, targetFolders)
	if got["GEN"] != space.NoSubject {
		t.Fatalf("hosting anchor = %q", got["GEN"])
	}
}

func TestTargetsNeverStandby(t *testing.T) {
	t.Parallel()
	got, _ := targetOf(t, Filing{Bound: map[string]string{"N": space.Standby}})
	if got != space.NoSubject {
		t.Fatalf("target = %q", got)
	}
}

var standbyFolders = []string{"Hedge", "Terrace", space.NoSubject, space.Standby}

func TestTargetsStandbyBeatsBoundOffTopicYoung(t *testing.T) {
	t.Parallel()
	f := Filing{Standby: map[string]bool{"N": true}, Bound: map[string]string{"N": "Terrace"},
		OffTopic: map[string]string{"N": ""}, Young: map[string]bool{"N": true}}
	got := folderTargets([]string{"N"}, nil, nil, nil, nil, f, standbyFolders)
	if got["N"] != space.Standby {
		t.Fatalf("Standby > bound > off-topic > young: %q", got["N"])
	}
}

func TestTargetsStandbyNeedsItsFolderAndSkipsPinned(t *testing.T) {
	t.Parallel()
	f := Filing{Standby: map[string]bool{"N": true}}
	if got := folderTargets([]string{"N"}, nil, nil, nil, nil, f, targetFolders); got["N"] != space.NoSubject {
		t.Fatalf("no « Standby » folder: falls to the other rules, got %q", got["N"])
	}
	if got := folderTargets([]string{"N"}, nil, map[string]bool{"N": true}, nil, nil, f, standbyFolders); len(got) != 0 {
		t.Fatalf("a pinned workspace is never filed: %v", got)
	}
}

func TestTargetsYoungInStandbyFolderIsFiledOut(t *testing.T) {
	t.Parallel()
	stb := g("G-B", space.Standby, "GB", true, "S", "Y")
	stb.Collapsed = true
	sys := g("G-S", "Terrace", "GS", true, "Z")
	f := Filing{Standby: map[string]bool{"S": true}, Young: map[string]bool{"Y": true, "Z": true}}
	got := folderTargets([]string{"GS", "Z", "GB", "S", "Y"}, []cmux.Group{sys, stb}, nil, nil, nil, f, standbyFolders)
	if got["Y"] != space.NoSubject {
		t.Fatalf("a young workspace in « Standby » goes to « Inbox »: %q", got["Y"])
	}
	if _, filed := got["Z"]; filed {
		t.Fatalf("a young one elsewhere keeps its grace: %q", got["Z"])
	}
	if got["S"] != space.Standby {
		t.Fatalf("S stays: %q", got["S"])
	}
}
