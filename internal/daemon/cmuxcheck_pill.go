package daemon

import (
	"errors"
	"io/fs"

	"github.com/aloysbr/pigtail/internal/cmuxcheck"
	"github.com/aloysbr/pigtail/internal/daemon/cmuxsync"
)

// cmuxCheckDefects turns the last `pigtail cmux check` report into the
// sidebar's pigtail-chk pill (D13, T-l): its rows `--fix` can lift (orange)
// or only a hand can (blocked or failed: Blocked, red — D15), only when the
// report is about the cmux running now. D16: a remaining row with no action,
// neither blocked nor failed (background work lost, D9), is no pill — it
// stays in the report and on the session's screen. No report yet, a fatal
// one (nothing was looked at: its exit 2 shows in pigtaild's log, which
// retries, and in `--last`), cmux down or relaunched since → none. A partial
// pass (--surface) was merged into the previous report by SaveLast (É25):
// the other surfaces keep their rows, so their pills.
func cmuxCheckDefects(dir, sentinel string) ([]cmuxsync.CheckDefect, error) {
	rep, err := cmuxcheck.LoadLast(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if rep.Fatal != "" {
		return nil, nil
	}
	if pid, err := cmuxcheck.ReadSentinel(sentinel); err != nil || pid != rep.Launch.PID {
		return nil, nil // cmux down, or a report about an earlier launch
	}
	var out []cmuxsync.CheckDefect
	for _, row := range rep.Rows {
		switch {
		case row.InputDead:
			// Not a check defect (sub_x000000000000024, bloquant 15): the
			// signal lives in the daemon's probe tracker only, or last.json
			// would keep « 1 to fix » after the input came back.
		case row.Fixable():
			out = append(out, cmuxsync.CheckDefect{SurfaceID: row.SurfaceID, PID: row.PID, At: rep.At})
		case row.Stuck():
			out = append(out, cmuxsync.CheckDefect{SurfaceID: row.SurfaceID, Blocked: true, PID: row.PID, At: rep.At})
		} // D16: reported only, no pill
	}
	return out, nil
}
