package daemon

import (
	"context"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
)

// The offsite copy proves that a backup lands somewhere else. It does not
// prove that "somewhere else" survives the way the operator thinks it does:
// a VPN node key can expire for days while the
// backup kept reading ok, because ~/.ssh/config routes the LAN first and the
// daemon never had a reason to try the pinned alias it was actually meant to
// depend on. This file is what tries it anyway, on its own schedule,
// independently of whether the primary copy ever needed it.
//
// Two pieces, split the same way offsite.go and store/offsite_remote.go
// split the primary copy from its transport:
//
//   - exerciseFallbackIfDue, a method on offsiteBackupper so it can share
//     stateMu with declare() and attempt(): the exercise's six fallback_*
//     fields live on the exact same OffsiteState struct, in the exact same
//     file, and updateState is the only path allowed to touch it;
//   - runOffsiteFallback, a method on Server, on the same model as
//     backup.go's runDBBackups: it owns the ticker, offsiteBackupper owns
//     the write.
//
// Why a persisted deadline, not a plain 6h ticker (the pitfall this whole
// file exists to avoid): on darwin, mach_absolute_time — the clock a
// time.Ticker is built on — does not advance while the machine sleeps. On a
// laptop used four hours a day, "every 6h" silently becomes "every 36
// calendar hours", and nothing says so: the daemon looks like it is running
// the exercise on schedule the whole time. store.Store.BackupRotatedIfStale
// (internal/daemon/store/backup.go) already solved this for the local
// rotation by stat'ing a file instead of trusting an in-process ticker to
// have counted six real hours; exerciseFallbackIfDue does the equivalent
// read against offsite-state.json's own FallbackLastTryAt, and
// runOffsiteFallback's ticker only has to wake up often enough to notice
// that the deadline has passed, not hit it exactly.
//
// A happy consequence of that persisted deadline, not a separate rule:
// offsiteFallbackDueInterval is what spaces any two consecutive exercise
// attempts at least that far apart, by construction — an exercise that just
// ran will not be judged due again for six hours no matter how often
// runOffsiteFallback's own ticker fires in between. So the two-failure
// streak store.offsiteFallbackWarnStreak escalates on (see
// store/offsite_fallback.go) already means "at least twelve hours with no
// successful exercise" on its own; nothing here needs to check age a second
// time.
const (
	// offsiteFallbackDueInterval is a package-local alias for store's own
	// exported store.OffsiteFallbackInterval (offsite_fallback.go), under a
	// short name this file's own doc comments already use throughout.
	// Read directly, not mirrored: this constant used to be a second,
	// unexported 6*time.Hour literal of its own, kept equal to store's by a
	// comment asking whoever changed one to remember the other — with
	// nothing that would ever notice if they didn't. The store-side
	// contract test (TestOffsiteFallbackContractConstants,
	// store/offsite_fallback_test.go) now pins the one value this reads,
	// so there is exactly one six-hour literal in the whole slice, not two
	// describing the same fact from either side of the package boundary.
	offsiteFallbackDueInterval = store.OffsiteFallbackInterval

	// offsiteFallbackExerciseTimeout bounds the context this package hands
	// to probe, ahead of whatever timeout the probe applies internally
	// (store.ExerciseOffsiteFallback wraps the same 30s again, once it has
	// a context to wrap). Belt and suspenders: a probe field a test
	// replaces with something that ignores context cancellation must still
	// not be able to stall runOffsiteFallback's loop past this.
	offsiteFallbackExerciseTimeout = 30 * time.Second

	// offsiteFallbackTickInterval is how often runOffsiteFallback wakes up
	// to check whether the persisted deadline (FallbackLastTryAt plus
	// offsiteFallbackDueInterval) has passed. Ten minutes, not six hours: a
	// long ticker is exactly the pitfall the deadline-on-disk design avoids
	// — see this file's own package doc above. A short, frequent wake-up
	// against a cheap, in-memory-cached-by-the-OS stat/read is nearly free,
	// and it is what keeps the six-hour cadence accurate across sleep
	// instead of merely accurate across uptime.
	offsiteFallbackTickInterval = 10 * time.Minute
)

// fallbackGuardsOK reports whether fallbackDest is a route worth exercising
// at all against destDir and dbPath — the three *correction* guards, on the
// same rule store.ClassifyOffsiteFallback applies when rendering status,
// checked here directly rather than through that function because there is
// no classified OffsiteHealth to hand it yet at this point in the call:
//
//   - fallbackDest is empty: nothing configured, mirroring destDir's own
//     "empty disables it" contract;
//   - fallbackDest does not parse as a [user@]host:/path, or names the same
//     destination as destDir: exercising it would prove nothing about a
//     redundant route, since there would not be one;
//   - dbPath has no file-backed state to persist a deadline to
//     (store.OffsiteStatePath returns ""): an unnamed or in-memory Store
//     makes WriteOffsiteState a silent no-op and ReadOffsiteState always the
//     zero value, so a scheduled caller reading "not due yet" from that
//     would never see a persisted try and would run on every tick instead
//     of every six hours.
//
// Shared, unconditionally, by both exerciseFallbackIfDue's scheduled path
// and RunOffsiteFallbackNow's (offsite_run.go) immediate one: task 8's own
// human-triggered run bypasses the *deadline* below, on explicit request,
// but never these three — see RunOffsiteFallbackNow's own doc comment.
func (o *offsiteBackupper) fallbackGuardsOK(dbPath, destDir, fallbackDest string) bool {
	if fallbackDest == "" {
		return false
	}
	if _, _, parses := store.ParseRemoteDest(fallbackDest); !parses || fallbackDest == destDir {
		return false
	}
	return store.OffsiteStatePath(dbPath) != ""
}

// acquireFallbackSlot claims fbInFlight, mirroring run()'s own inFlight
// guard (offsite.go) but on the fallback's own field — see fbInFlight's doc
// comment for why the two are never merged. Returns false when an exercise
// is already running. Paired with releaseFallbackSlot, always via defer.
func (o *offsiteBackupper) acquireFallbackSlot() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.fbInFlight {
		return false
	}
	o.fbInFlight = true
	return true
}

func (o *offsiteBackupper) releaseFallbackSlot() {
	o.mu.Lock()
	o.fbInFlight = false
	o.mu.Unlock()
}

// doFallbackExercise runs one probe against fallbackDest and records the
// outcome — the corps commun task 8 extracted out of exerciseFallbackIfDue
// so RunOffsiteFallbackNow (offsite_run.go) can reuse it verbatim instead of
// growing a second copy of the same read-probe-write sequence. The caller
// must already hold the fbInFlight slot (acquireFallbackSlot) and release it
// (releaseFallbackSlot, deferred) once this returns; this function itself
// touches neither.
//
// Runs the probe under its own bounded context and records the outcome
// through exactly one updateState call, on the same model as attempt(): the
// mutation abstains (and updateState skips the write) if a concurrent
// redeclare changed FallbackDest while the probe was running, so this
// exercise's result is never stamped onto a route that is no longer the one
// configured.
//
// fbLastTryFloor exists for the write that never lands: a directory gone
// read-only, a remote disk full mid-write. store.WriteOffsiteState degrades that to
// a logged-and-swallowed error (updateState), so FallbackLastTryAt on disk
// stays at whatever it was before — possibly its zero value forever — and
// every later tick would read the deadline as already passed, turning four
// exercises a day into a hundred and forty-four real connections. Taking the
// later of the disk value and the floor (in exerciseFallbackIfDue's own
// deadline check, below) survives both a stale floor (the process just
// started, disk is ahead) and a stale disk value (the last write failed,
// memory is ahead).
//
// The floor is bumped only when updateState reports the write itself did
// not land — never unconditionally on every attempt. An attempt whose write
// succeeds already has its own timestamp sitting on disk, which the next
// tick's read picks up on its own; bumping the floor there too would be
// redundant on the ordinary path and actively wrong on the one path that
// legitimately rewrites FallbackLastTryAt directly (this package's own
// tests, simulating hours elapsing without an in-process ticker actually
// waiting them out) — a floor set to the moment of a *successful* write
// would outrank that rewrite forever, since nothing after a real success
// ever lowers it.
func (o *offsiteBackupper) doFallbackExercise(dbPath, destDir, fallbackDest string) {
	ctx, cancel := context.WithTimeout(context.Background(), offsiteFallbackExerciseTimeout)
	defer cancel()
	via, err := o.probe(ctx, fallbackDest)
	now := time.Now()

	result, applied, wrote := o.updateState(dbPath, func(s *store.OffsiteState) bool {
		if s.FallbackDest != fallbackDest {
			// A redeclare landed while the probe was running: this outcome
			// belongs to a route that is no longer configured. Same
			// abstention attempt() applies for the same reason — see
			// TestAttemptNotesNoTransitionWhenDestinationChangedMidCopy.
			return false
		}
		s.FallbackLastTryAt = now
		if err != nil {
			s.FallbackLastErr = err.Error()
			s.FallbackConsecutiveFailures++
		} else {
			s.FallbackLastOKAt = now
			s.FallbackLastErr = ""
			s.FallbackConsecutiveFailures = 0
			s.FallbackVia = via
		}
		return true
	})
	if applied && !wrote {
		o.mu.Lock()
		if now.After(o.fbLastTryFloor) {
			o.fbLastTryFloor = now
		}
		o.mu.Unlock()
	}
	if applied {
		o.noteFallbackTransition(fallbackDest, result)
	}
}

// exerciseFallbackIfDue runs one fallback exercise against fallbackDest, the
// pinned alias configured alongside the primary destDir, unless any of two
// things says not to, ahead of fallbackGuardsOK's own three:
//
//   - the last try — the later of the persisted FallbackLastTryAt and the
//     in-memory fbLastTryFloor — is younger than offsiteFallbackDueInterval:
//     not due yet;
//   - fbInFlight is already true: an exercise is still running (or, on a
//     misbehaving probe, stuck) from an earlier call. It is checked and set
//     under o.mu, independently of inFlight — see fbInFlight's own doc
//     comment (offsite.go) for why the primary copy and the exercise must
//     never be able to block one another.
func (o *offsiteBackupper) exerciseFallbackIfDue(dbPath, destDir, fallbackDest string) {
	if !o.fallbackGuardsOK(dbPath, destDir, fallbackDest) {
		return
	}

	state := store.ReadOffsiteState(dbPath)
	lastTry := state.FallbackLastTryAt

	o.mu.Lock()
	if o.fbLastTryFloor.After(lastTry) {
		lastTry = o.fbLastTryFloor
	}
	o.mu.Unlock()

	if !lastTry.IsZero() && time.Since(lastTry) < offsiteFallbackDueInterval {
		return
	}

	if !o.acquireFallbackSlot() {
		return
	}
	defer o.releaseFallbackSlot()

	o.doFallbackExercise(dbPath, destDir, fallbackDest)
}

// noteFallbackTransition logs the fallback exercise's edges only — into
// "not working" and back to "working again" — on the same model as
// noteTransition, but with the two edges gated on two different things,
// not one shared condition the way a first version of this function read.
//
//   - Entering "not working" is gated on the fully classified, muted
//     severity (store.ClassifyOffsiteFallback), never on the raw error
//     alone: ClassifyOffsiteFallback mutes the fallback's own severity to ok
//     whenever the primary route already needs attention, because a laptop
//     away from its backup host makes both routes fail for the same one reason, and
//     two alarms for one outage is how alarms stop getting read (see that
//     function's own doc comment). That muting is correct here: it defers
//     the "not working" announcement while the primary is already the
//     louder problem, on purpose.
//   - Recovering into "working again" is gated on the fallback's own raw
//     fact — its failure streak actually reaching zero — never on the muted
//     severity crossing back under warn. The muting above is a floor on
//     how *alarming* the fallback reads while the primary is unhealthy; it
//     is not evidence the fallback itself got better, and treating a drop
//     in the muted severity as a recovery reproduces exactly the bug this
//     feature exists to close: two exercises fail, the primary happens to
//     go stale in between (muting the fallback back to ok on the read
//     alone), a third exercise fails too, and the muted severity's edge
//     would still log "is working again" while the key is still expired.
//     ConsecutiveFailures reaching zero is the one fact that is never
//     muted — see OffsiteFallbackHealth.ConsecutiveFailures's own doc
//     comment (store/offsite_fallback.go).
//
// fbWarned is updated on exactly the same two edges, not on every call: a
// third failure landing while the primary mutes the severity must leave
// fbWarned at true (still warned, nothing recovered) rather than following
// the muted severity back down — the bug above, one layer earlier — because
// that is what a real recovery later needs to find still set, to log
// "working again" instead of nothing at all.
func (o *offsiteBackupper) noteFallbackTransition(fallbackDest string, state store.OffsiteState) {
	now := time.Now()
	primary := store.ClassifyOffsite(state, now)
	health := store.ClassifyOffsiteFallback(state, primary, now)
	nowWarn := store.OffsiteSeverityAtLeast(health.Severity, store.SeverityWarn)
	recovered := state.FallbackConsecutiveFailures == 0

	o.mu.Lock()
	wasWarn := o.fbWarned
	switch {
	case nowWarn && !wasWarn:
		o.fbWarned = true
	case recovered && wasWarn:
		o.fbWarned = false
	}
	o.mu.Unlock()

	switch {
	case nowWarn && !wasWarn:
		o.log("daemon: offsite fallback path to %s is not working: %s", fallbackDest, state.FallbackLastErr)
	case recovered && wasWarn:
		o.log("daemon: offsite fallback path to %s is working again", fallbackDest)
	}
}

// runOffsiteFallback is the ticker loop, started by Run (server.go) next to
// runDBBackups, under the same cfg.DBOffsiteDir != "" && cfg.DBOffsiteFallbackDest
// != "" condition that guards the goroutine launch. An immediate pass first
// — the same "don't make the operator wait a full period to see the first
// result" shape backupDBOnce's own initial call has — then a wake-up every
// offsiteFallbackTickInterval to re-check the persisted deadline. Each call
// is exerciseFallbackIfDue's own responsibility to skip when not yet due; a
// short, frequent wake-up here is what keeps the six-hour cadence accurate
// across sleep, per this file's package doc.
func (s *Server) runOffsiteFallback(ctx context.Context) {
	if s.offsite == nil || s.store == nil {
		return
	}
	dbPath := s.store.Path()
	destDir := s.cfg.DBOffsiteDir
	fallbackDest := s.cfg.DBOffsiteFallbackDest

	t := time.NewTicker(offsiteFallbackTickInterval)
	defer t.Stop()

	s.offsite.exerciseFallbackIfDue(dbPath, destDir, fallbackDest)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.offsite.exerciseFallbackIfDue(dbPath, destDir, fallbackDest)
		}
	}
}
