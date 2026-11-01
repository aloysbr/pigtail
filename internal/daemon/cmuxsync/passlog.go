package cmuxsync

import (
	"fmt"
	"time"
)

// SlowPassLog: a pass longer than this is logged with the time of each step,
// so a pill that lags names the cmux call that held the loop (D3 c).
const SlowPassLog = time.Second

// passTiming is the time of each step of the current pass, on the loop's
// clock (Deps.Now). Touched by the Run goroutine only. slow includes peers;
// each step includes the pill checkpoints run inside it (ckpt, ckpts).
type passTiming struct {
	slow, peers, reconcile, sync, arrange, claudeCode, ckpt time.Duration
	ckpts                                                   int
}

// timed runs f and adds its duration to *d.
func (r *Runner) timed(d *time.Duration, f func()) {
	t0 := r.deps.Now()
	f()
	*d += r.deps.Now().Sub(t0)
}

func (t passTiming) line(total time.Duration) string {
	rd := func(d time.Duration) time.Duration { return d.Round(10 * time.Millisecond) }
	return fmt.Sprintf("cmuxsync: pass %s (slow %s, peers %s, reconcile %s, sync %s, arrange %s, claude_code %s, checkpoints %d in %s)",
		rd(total), rd(t.slow), rd(t.peers), rd(t.reconcile), rd(t.sync), rd(t.arrange), rd(t.claudeCode), t.ckpts, rd(t.ckpt))
}
