package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The staffing scheduler (sub_x000000000000004, Task 9) is documented: its
// contract in wire-protocol.md (routes, event, every outcome, gate and skip),
// (the README half moved out with the long reference, docs/reference.md).
func TestDocsStaffing(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(f string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		if len(b) == 0 {
			t.Fatalf("%s read empty", f)
		}
		return string(b)
	}
	wire := read("docs/wire-protocol.md")
	_, sec, ok := strings.Cut(wire, "### Staffing")
	if !ok {
		t.Fatal("wire-protocol.md has no § Staffing")
	}
	sec, _, _ = strings.Cut(sec, "\n### ")
	for _, s := range []string{"GET /staffing", "POST /staffing", "GET /staffing/decisions", string(pigtail.EventStaffingDecision),
		"staffing.json", "staffing-decisions.jsonl", "X-Pigtail-Session", "--cross-scope",
		pigtail.StaffingOutcomeIdle, pigtail.StaffingOutcomeWouldLaunch, pigtail.StaffingOutcomeLaunchStarted,
		pigtail.StaffingOutcomeLaunchRefused, pigtail.StaffingOutcomeLaunched, pigtail.StaffingOutcomeLaunchFailed,
		pigtail.StaffingOutcomeSettingsChanged, pigtail.StaffingOutcomeObservedLaunch,
		pigtail.StaffingGateNoFreePlace, pigtail.StaffingGateNoReadySubject, pigtail.StaffingGateOverMargin, pigtail.StaffingGateOverBudget,
		pigtail.StaffingGateMaintenance, pigtail.StaffingGateSessionBudgetOff, pigtail.StaffingGateSessionBudgetFull,
		pigtail.StaffingGateMachineBusy, pigtail.StaffingGateBurst, pigtail.StaffingGateUnreadable,
		pigtail.StaffingSkipHeld, pigtail.StaffingSkipPhantom, pigtail.StaffingSkipPaused, pigtail.StaffingSkipPending,
		pigtail.StaffingSkipWaitsHuman, pigtail.StaffingSkipDeps, pigtail.StaffingSkipCooldown, pigtail.StaffingSkipOverBudget,
		pigtail.StaffingLauncherScheduler, pigtail.StaffingLauncherPilot} {
		if !strings.Contains(sec, "`"+s) {
			t.Errorf("wire-protocol.md § Staffing lacks `%s", s)
		}
	}

	for _, s := range []string{"`busy`", "`idle`", "`LaunchGrace`"} {
		if !strings.Contains(sec, s) {
			t.Errorf("wire-protocol.md § Staffing lacks %s", s)
		}
	}
	if strings.Contains(sec, "keeps its place") {
		t.Error("wire-protocol.md § Staffing still says a waiting session keeps its place")
	}
}
