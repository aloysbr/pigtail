package cmuxsync

import (
	"strconv"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/waittype"
)

const (
	// T-l: below the attention pill (90), above the maintenance pill (80) —
	// a fact of this workspace, more precise than the maintenance shown on
	// every workspace.
	checkPriority = 85
	checkIcon     = "stethoscope"
)

// CheckDefect is one surface the last `pigtail cmux check` left in defect
// and worth a pill (cmuxcheck.Row.Fixable or Row.Stuck, D15; a reported-only
// row is none, D16), built by the daemon from last.json (D13).
// Blocked: the row is blocked or its repair failed — `--fix` alone will not
// lift it.
type CheckDefect struct {
	SurfaceID string
	Blocked   bool
	// PID is the process the check saw on the surface (cmuxcheck.Row.PID);
	// 0 = none or several. At is the report's time (memory only): D3 reads
	// both to tell a pill whose surface now serves another process.
	PID int
	At  time.Time
}

// CheckPill is the check pill of one workspace.
type CheckPill struct {
	Left    int  // its surfaces in defect
	Blocked bool // one of them is blocked or failed: red
	// InputDead: its surfaces whose input the daemon's probe found dead
	// (sub_x000000000000024); the pill then says so, whatever Left says.
	InputDead int
}

// checkPills puts each defect on the live workspace whose open surfaces hold
// it. A surface no live workspace lists — closed since the check, or in a
// workspace whose surfaces are unknown (nil) — lights nothing.
func checkPills(defects []CheckDefect, ws []Workspace) map[string]CheckPill {
	if len(defects) == 0 {
		return nil
	}
	out := map[string]CheckPill{}
	for _, d := range defects {
		sid := strings.ToUpper(strings.TrimSpace(d.SurfaceID))
		if sid == "" {
			continue
		}
		for _, w := range ws {
			if w.Surfaces[sid] {
				p := out[w.ID]
				p.Left++
				p.Blocked = p.Blocked || d.Blocked
				out[w.ID] = p
				break
			}
		}
	}
	return out
}

// inputDeadValue is the pill's text for a dead input (G2,
// sub_x000000000000024): no session, no title, no count.
const inputDeadValue = "entrée morte · pigtail cmux check"

// checkStatus renders the pill: "Check · N left" in red when a defect is
// blocked or failed, else "Check · N to fix" in orange (`pigtail cmux check
// --fix` can lift it); a dead input (G1) says « entrée morte », orange, red
// when a blocked defect is there too. No session or title in the text (D23).
func checkStatus(p CheckPill) cmux.StatusEntry {
	g, v := waittype.Orange, "Check · "+strconv.Itoa(p.Left)+" to fix"
	if p.Blocked {
		g, v = waittype.Red, "Check · "+strconv.Itoa(p.Left)+" left"
	}
	if p.InputDead > 0 {
		v = inputDeadValue
	}
	return cmux.StatusEntry{Key: KeyCmuxCheck, Value: v, Icon: checkIcon, Color: g.Color(), Priority: checkPriority}
}

// inputDeadPills counts, per live workspace, the dead-input surfaces its
// open surfaces hold (the daemon's probe, sub_x000000000000024). A surface
// no live workspace lists lights nothing.
func inputDeadPills(surfaces []string, ws []Workspace) map[string]int {
	out := map[string]int{}
	for _, s := range surfaces {
		sid := strings.ToUpper(strings.TrimSpace(s))
		if sid == "" {
			continue
		}
		for _, w := range ws {
			if w.Surfaces[sid] {
				out[w.ID]++
				break
			}
		}
	}
	return out
}
