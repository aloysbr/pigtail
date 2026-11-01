package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// rotAddJournal writes one journal row as the session (a seam: tests capture
// the rows). Scope left empty: the caller's own.
var rotAddJournal = func(ctx context.Context, getenv func(string) string, e pigtail.JournalEntry) error {
	_, err := sessionClient(getenv).AddJournal(ctx, e)
	return err
}

// retryRotate runs attempt, and again after rotateRetryEvery while its
// refusal is a transient one (waittype.RotationTransient: no input box yet,
// no native file yet), for at most rotateOncePer from the first try, never
// past the limit's reset, and only while the session's limit wait still
// carries this Until. A session in Standby is never typed in (B5): checked
// before every try, the first included. The Ahead mode and --dry-run do not
// loop. The session lock is held by runRotate for the whole loop.
func (w *rotateWorker) retryRotate(attempt func() error) error {
	if w.opt.Ahead || w.opt.DryRun {
		return attempt()
	}
	start := rotateNow()
	for k := 1; ; k++ {
		if inStandby(w.getenv, w.opt.Session) {
			w.endRetry(k, "standby")
			w.logf("refused: session in Standby")
			w.hook("refused", "reason=standby")
			return errors.New(rotateLimitPrefix + "session in Standby")
		}
		w.lastRefusal = ""
		err := attempt()
		why := w.lastRefusal
		switch {
		case err == nil:
			return nil
		case why == "" || !waittype.RotationTransient(why):
			if k > 1 {
				w.endRetry(k, "refused: "+why)
			}
			return err
		}
		if over := w.retryWindowOver(start); over != "" {
			w.endRetry(k, over)
			return err
		}
		w.retryJournal(fmt.Sprintf("%d", k), "rotate-retry", fmt.Sprintf("retry %d after: %s", k, why))
		rotateSleep(rotateRetryEvery)
		// The pause takes a minute: the window and the reset are judged again
		// before the next try, not only before the pause.
		if over := w.retryWindowOver(start); over != "" {
			w.endRetry(k, over)
			return err
		}
		if !w.limitStillSame() {
			w.endRetry(k, "limit wait gone")
			return err
		}
	}
}

// retryWindowOver names why the loop must stop now, "" when it may go on:
// rotateOncePer elapsed since start, or the limit's reset (Until) reached.
func (w *rotateWorker) retryWindowOver(start time.Time) string {
	now := rotateNow()
	switch {
	case !now.Before(start.Add(rotateOncePer)):
		return "window over"
	case !w.opt.Until.IsZero() && !now.Before(w.opt.Until):
		return "reset passed"
	}
	return ""
}

// limitStillSame: the session's limit wait is still there with the Until this
// worker was started for. A wait that cannot be read ends the loop.
func (w *rotateWorker) limitStillSame() bool {
	if w.opt.Until.IsZero() {
		return true
	}
	ws, err := rotSessionWaits(w.ctx, w.getenv, w.opt.Session)
	if err != nil {
		w.logf("limit wait unreadable: %v", err)
		return false
	}
	for _, x := range ws {
		if x.Type == string(waittype.Limit) && x.Until != nil && x.Until.Equal(w.opt.Until) {
			return true
		}
	}
	return false
}

func (w *rotateWorker) endRetry(k int, summary string) {
	w.retryJournal("ended", "rotate-retry-ended", fmt.Sprintf("try %d · %s", k, summary))
}

// shortSession is the session's short id (8 characters), as the relay's
// wakeShort names it in its own rows.
func shortSession(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func (w *rotateWorker) retryJournal(suffix, kind, summary string) {
	id := fmt.Sprintf("rotate-retry:%s:%d:%s", w.opt.Session, w.opt.Until.Unix(), suffix)
	e := pigtail.JournalEntry{ID: id, Timestamp: rotateNow(), Kind: kind,
		Summary: shortSession(w.opt.Session) + ": limit · " + strings.Join(strings.Fields(summary), " "), Source: "pigtail:session/" + w.opt.Session}
	if err := e.Validate(); err != nil {
		w.logf("journal %s not written: %v", kind, err)
		return
	}
	ctx, cancel := context.WithTimeout(w.ctx, relayHookTimeout)
	defer cancel()
	if err := rotAddJournal(ctx, w.getenv, e); err != nil {
		w.logf("journal %s not written: %v", kind, err)
	}
}
