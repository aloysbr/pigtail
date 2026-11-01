package daemon

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
)

// The offsite copy is the only part of the backup chain that talks to
// something pigtail does not control, and that shapes everything here.
//
// A write to a hung network *mount* blocks for as long as the mount feels like
// it, and a context does not interrupt a read already inside the kernel — so
// for that transport "cancel it" is not on the table.
//
// It is on the table for the remote transport, and the two must not be
// confused: ssh and rsync are subprocesses, which a context does kill. They are
// bounded in store/offsite_remote.go. Reading the paragraph below as covering
// both is what left them unbounded, and the guarantee that only one attempt is
// ever in flight then turned a single hung ssh into "no offsite copy will ever
// run again until the daemon restarts".
//
// What remains true for both is refusing to let a slow destination hurt
// anything else:
//
//   - it runs **off** the backup loop, so a hung NAS cannot stop the local
//     backups, which still work and are still the ones that matter most;
//   - only **one** copy is ever in flight, so an hourly ticker against an
//     unreachable destination leaks one blocked goroutine, not one per hour;
//   - it runs **after** the local backup, never before.
//
// Failures are reported, never fatal — a daemon that stopped managing
// worktrees because a NAS was asleep would cause the outage the backup exists
// to survive. But "reported" has to mean something: an unreachable destination
// is the normal state of a laptop away from its NAS, so logging it every hour
// produces a log nobody reads. Only transitions get a line; the standing state
// lives in offsite-state.json, where `pigtail db list` finds it with the daemon
// stopped.

// offsiteBackupper owns the one-at-a-time discipline and the transition log.
// The copy and the logger are fields so tests can drive both without a NAS.
type offsiteBackupper struct {
	copy func(dbPath, destDir string, keep int) error
	logf func(format string, args ...interface{})
	// sameDeviceFn is the injected same-device detector, on the same model as
	// copy and logf: tests exercise the fallback and failure paths without a
	// real filesystem to stat.
	sameDeviceFn func(dbPath, destDir string) (bool, error)

	// probe runs the fallback exercise: one ssh round trip against the
	// pinned alias, reporting the address the far side saw the connection
	// arrive from. A field on the same model as copy, sameDeviceFn and
	// logf, so offsite_fallback_test.go can drive exerciseFallbackIfDue
	// without an SSH server. See offsite_fallback.go.
	probe func(context.Context, string) (string, error)

	mu sync.Mutex
	// copies tracks the detached goroutines run (below) and
	// RunOffsiteFallbackNow (offsite_run.go) spawn. They answer to no
	// context — by design: a copy is not something a cancellation should
	// leave half-written — which makes them the one set of writers
	// Server.Run does not start itself and so could not otherwise join at
	// shutdown. inFlight/fbInFlight already say *whether* one is running;
	// this says when it has stopped, which is what a drain needs and what a
	// bool cannot express. See Server.drainWorkers (server.go).
	copies sync.WaitGroup
	// stateMu guards the state file, which declare, attempt and
	// exerciseFallbackIfDue all write.
	stateMu          sync.Mutex
	inFlight         bool
	failing          bool // last attempt failed; used to log only the transitions
	alarming         bool // last classified severity was alarm; used to log only the transitions
	warnedSameDevice bool
	// fbInFlight is inFlight's own counterpart for the fallback exercise —
	// deliberately a second field, not a shared one. The primary copy and
	// the exercise work to two different destinations for two different
	// reasons, and neither may ever wait on the other's slot: a NAS with a
	// hung primary mount must not block the exercise that proves the
	// *redundant* route still works, and a slow exercise must never delay
	// the backup that actually matters. See offsite_fallback.go for the
	// -race test that pins this.
	fbInFlight bool
	// fbWarned mirrors failing/alarming above, but for the fallback's own
	// classified severity (store.ClassifyOffsiteFallback), so the
	// transition log fires once per crossing rather than once per failed
	// exercise.
	fbWarned bool
	// fbLastTryFloor is an in-memory floor under FallbackLastTryAt: the
	// later of it and whatever offsite-state.json's own field reads is what
	// exerciseFallbackIfDue treats as the last try, so a write that never
	// landed — a read-only directory, a store with nowhere on disk to keep
	// the file — cannot make the persisted deadline read as already passed
	// on every later tick. See exerciseFallbackIfDue's own doc comment
	// (offsite_fallback.go) for the failure this exists to close: four
	// exercises a day silently becoming a hundred and forty-four real
	// connections.
	fbLastTryFloor time.Time
}

func newOffsiteBackupper() *offsiteBackupper {
	return &offsiteBackupper{
		copy:         store.BackupOffsite,
		logf:         log.Printf,
		sameDeviceFn: store.SameDevice,
		probe:        store.ExerciseOffsiteFallback,
	}
}

// log routes through the injected sink, falling back to the standard logger.
// The fallback is not politeness: the sink is only ever reached on a failure
// path, so a nil field would turn "could not write the state file" — a thing
// worth knowing — into a panic inside a background goroutine, taking the
// daemon down over a backup report.
func (o *offsiteBackupper) log(format string, args ...interface{}) {
	if o.logf != nil {
		o.logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// sameDevice reports whether dbPath and destDir share a filesystem, routed
// through the injected sameDeviceFn when a test provided one, falling back to
// store.SameDevice otherwise. The fallback is what lets the many struct
// literals in this package's tests set only copy and logf, as they already
// did before this field existed, without a nil-field panic the moment
// attempt() runs against a local destination.
func (o *offsiteBackupper) sameDevice(dbPath, destDir string) (bool, error) {
	if o.sameDeviceFn != nil {
		return o.sameDeviceFn(dbPath, destDir)
	}
	return store.SameDevice(dbPath, destDir)
}

// run starts a copy unless one is already in flight, and returns immediately
// — before the copy itself has done any I/O — reporting whether it actually
// started. The bool return is task 8's own addition: RunOffsiteNow
// (offsite_run.go) is a thin wrapper around this exact method, and the "was
// a copy already in flight" answer is precisely offsite_started in POST
// /backup/offsite/run's response. backupDBOnce (backup.go), the only other
// caller, discards it — a scheduled tick has nothing to report it to.
func (o *offsiteBackupper) run(dbPath, destDir string) bool {
	o.mu.Lock()
	if o.inFlight {
		o.mu.Unlock()
		return false
	}
	o.inFlight = true
	o.mu.Unlock()

	o.copies.Add(1)
	go func() {
		defer o.copies.Done()
		defer func() {
			o.mu.Lock()
			o.inFlight = false
			o.mu.Unlock()
		}()
		o.attempt(dbPath, destDir)
	}()
	return true
}

// waitForCopies blocks until every goroutine started by run and
// RunOffsiteFallbackNow has returned. Called only by Server.drainWorkers
// (server.go), under that function's own timeout — nothing here bounds it,
// because the two callers it waits on are already bounded by their own
// transports' timeouts.
func (o *offsiteBackupper) waitForCopies() { o.copies.Wait() }

// runSync is run without the goroutine, for tests and for any caller that
// genuinely wants to wait.
func (o *offsiteBackupper) runSync(dbPath, destDir string) {
	o.mu.Lock()
	if o.inFlight {
		o.mu.Unlock()
		return
	}
	o.inFlight = true
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		o.inFlight = false
		o.mu.Unlock()
	}()
	o.attempt(dbPath, destDir)
}

// declare records the configured destination before anything is attempted.
//
// Called at startup, and it is what makes the *absence* of a report
// impossible. Before it existed, a destination the daemon never managed to
// touch left no state file at all — and `db list` cannot tell "configured but
// nothing ever happened" from "no destination configured", so it showed
// nothing and the listing read as healthy. Writing the destination down the
// moment it is known removes that ambiguity.
//
// A previous success is carried forward: a restart is not evidence that the
// last copy never landed. So is the failure streak — a restart is not
// evidence the outage ended either, and before this it was: the only memo of
// consecutive failures used to be offsiteBackupper.failing, an in-RAM bool,
// so a restart mid-outage silently erased it and the alarm it fed. The carry
// is conditioned on DestDir matching what was already persisted: a changed
// destination is a different backup relationship and starts at zero. An
// in-flight marker is not carried at all — whatever goroutine it described
// died with the previous process.
//
// DeclaredAt follows the same carry-or-reset rule, with one addition: it is
// backfilled to now whenever it is missing, matching destination or not.
// That covers both a genuinely new destination (DeclaredAt is zero because
// the struct was just reset above) and a state file written by a daemon
// build that predates this field (same destination, DeclaredAt still zero) —
// neither should leave declared_at unset forever, since that would disable
// the alarm this field exists to feed for anyone upgrading in place.
//
// Routed through updateState like attempt(): everything else this state file
// carries (or will carry — see updateState's own doc) is preserved on a
// matching destination for free, rather than through a fixed field-by-field
// whitelist a later task would have to remember to extend.
//
// fallbackDest declares the exercise's own destination, on the exact same
// carry-or-reset rule as destDir above, but checked independently: a primary
// destination that has not changed does not by itself justify carrying the
// exercise's history forward if *its own* destination has — a redeclare
// naming a different pinned alias is a different redundant route, with a
// clean slate of its own. Called unconditionally whenever a primary
// destination is configured, even when fallbackDest is empty (nothing
// configured); called from server.go, ahead of runDBBackups and
// runOffsiteFallback both — see that call site's own comment for why the
// order matters.
func (o *offsiteBackupper) declare(dbPath, destDir, fallbackDest string) {
	now := time.Now()
	o.updateState(dbPath, func(s *store.OffsiteState) bool {
		primaryChanged := s.DestDir != destDir
		if primaryChanged {
			*s = store.OffsiteState{DestDir: destDir}
		} else {
			s.AttemptStartedAt = time.Time{}
		}
		if primaryChanged || s.FallbackDest != fallbackDest {
			s.FallbackDest = fallbackDest
			s.FallbackLastOKAt = time.Time{}
			s.FallbackLastTryAt = time.Time{}
			s.FallbackLastErr = ""
			s.FallbackConsecutiveFailures = 0
			s.FallbackVia = ""
		}
		if s.DeclaredAt.IsZero() {
			s.DeclaredAt = now
		}
		return true
	})
}

// attempt performs one copy and records what happened.
//
// The state is written **twice**, through updateState, so it always goes
// through a fresh read-modify-write rather than a state kept in a local
// variable across the whole call: once before the copy, marking it in
// flight, and once after with the outcome. The first write is the whole
// point — see OffsiteState.AttemptStartedAt. A copy that never returns used
// to report nothing whatsoever; now it reports that it started and never
// finished, which is the one fact a poller cannot otherwise discover.
//
// Both calls re-check the persisted destination against destDir, the same
// carry-or-reset rule declare() already applies: a changed destination is a
// different backup relationship, and this attempt's report belongs to
// whichever one is current, not to whichever one it started against.
func (o *offsiteBackupper) attempt(dbPath, destDir string) {
	now := time.Now()

	// Marked in flight and persisted before touching the destination. Note the
	// ordering against SameDevice below: that call stats the destination, so it
	// can hang on a bad mount too, and it must not run before the record
	// exists.
	o.updateState(dbPath, func(s *store.OffsiteState) bool {
		if s.DestDir != destDir {
			// A changed destination is a different backup relationship: its
			// failure history and last success belong to it, not to
			// whatever the previous destination reported.
			*s = store.OffsiteState{DestDir: destDir}
		}
		s.LastTryAt = now
		s.AttemptStartedAt = now
		// The error on record describes the *previous* attempt, not this
		// one, which has not run yet — carrying it into the in-flight
		// write would make ClassifyOffsite read a copy that just started
		// as still failing (LastErr != "" outranks Attempting() in its
		// switch), turning a normal retry into a false alarm the same way
		// a carried-forward ConsecutiveFailures would. Cleared here,
		// restored below if this attempt fails too.
		s.LastErr = ""
		return true
	})

	// Only meaningful for a local destination: a remote one is on another
	// machine by construction, and stat'ing "host:/path" here would be asking
	// the local filesystem about a string it cannot resolve.
	//
	// Computed once, outside the lock: SameDevice stats the destination and
	// can hang on a bad mount, so it must not be attempted while holding
	// stateMu. Applied below only if it succeeded — a stat that fails,
	// transiently or not, leaves whatever value was already persisted
	// rather than resetting it to false, so a real warning does not
	// silently disappear because of a momentary stat failure.
	var same bool
	var sameKnown bool
	if _, _, remote := store.ParseRemoteDest(destDir); !remote {
		if v, err := o.sameDevice(dbPath, destDir); err == nil {
			same, sameKnown = v, true
		}
	}

	err := o.copy(dbPath, destDir, store.BackupGenerations)

	// applied is false when someone redeclared a different destination while
	// the copy was running: stamping this attempt's outcome onto it would be
	// a lie about a backup relationship that is no longer current, so the
	// mutation abstains — updateState then leaves the file untouched — and
	// state is whatever that other destination's own state already was, not
	// this attempt's. noteTransition must not run against it: it would log
	// and update the in-RAM failing/alarming flags from the wrong
	// destination's classification, potentially naming this attempt's
	// destDir and err in the same breath.
	state, applied, _ := o.updateState(dbPath, func(s *store.OffsiteState) bool {
		if s.DestDir != destDir {
			return false
		}
		if sameKnown {
			s.SameDevice = same
		}
		if err != nil {
			s.LastErr = err.Error()
			s.ConsecutiveFailures++
		} else {
			// Stamped at completion, not at `now`. A first real copy
			// took 38 minutes over a waking SMB mount, which made "last copy:
			// 1h05m ago" the answer for a copy that had landed 27 minutes earlier.
			// The gap is widest exactly where freshness matters most.
			s.LastOKAt = time.Now()
			s.LastErr = ""
			s.ConsecutiveFailures = 0
		}
		// Cleared either way: a failed copy is a failure, not a stuck one.
		s.AttemptStartedAt = time.Time{}
		return true
	})

	if applied {
		o.noteTransition(err, destDir, state)
	}
}

// updateState is the only path allowed to touch offsite-state.json: it holds
// stateMu across the read, the mutation and the write, so a read-modify-write
// cycle from one caller can never interleave with another's.
//
// Before this existed, the analogous logic in attempt() read the previous
// state, held its answer in a local variable for as long as the copy took —
// up to ten minutes — and only then wrote it back. stateMu serialised the
// writes themselves, but not that gap: a second writer's own
// read-modify-write landing inside it would be silently overwritten the
// moment attempt() finally wrote. The exerciser landing in a later task is
// exactly that second writer, so this has to hold for any caller, not just
// attempt().
//
// fn reports whether it actually mutated state; when it returns false,
// updateState skips the write entirely rather than persisting a rewrite that
// would be byte-for-byte what was already on disk, and the second, bool
// return value is false so the caller can tell an applied change from an
// abstention — attempt() uses exactly that to skip noteTransition when a
// concurrent redeclare made its own outcome no longer apply.
//
// The third, bool return value reports whether the write actually landed on
// disk — false when store.WriteOffsiteState errored, which this function
// already logs and otherwise swallows, on purpose: a daemon that stopped
// managing worktrees because a NAS went read-only would be worse than a
// dropped state report. Most callers have no use for it and discard it with
// `_`, exactly as they discarded the two-value return before this existed —
// exerciseFallbackIfDue (offsite_fallback.go) is the one caller that reads
// it, to keep its own in-memory floor from drifting out of step with a
// write that never happened.
func (o *offsiteBackupper) updateState(dbPath string, fn func(*store.OffsiteState) bool) (store.OffsiteState, bool, bool) {
	o.stateMu.Lock()
	defer o.stateMu.Unlock()
	state := store.ReadOffsiteState(dbPath)
	if !fn(&state) {
		return state, false, false
	}
	if err := store.WriteOffsiteState(dbPath, state); err != nil {
		o.log("daemon: could not record the offsite backup state: %v", err)
		return state, true, false
	}
	return state, true, true
}

// noteTransition logs the edges only. A destination that has been unreachable
// for three weeks says so once, and says so again when it comes back.
//
// The first failure alone used to be the whole story: on 2026-09-06 that one
// line was the only thing the daemon ever logged about the outage, and nobody
// was reading the log at the moment it was written. By the time the streak
// crossed into alarm — the fact that should have been impossible to miss —
// nothing new reached the log at all; the alarm existed only in
// offsite-state.json, reachable solely by someone thinking to run `pigtail
// servers`. This also logs the transition into alarm severity, once per
// transition, so a daemon log tailed live carries the escalation itself, not
// just its first, unremarkable symptom.
func (o *offsiteBackupper) noteTransition(err error, destDir string, state store.OffsiteState) {
	health := store.ClassifyOffsite(state, time.Now())
	nowAlarming := health.Severity == store.SeverityAlarm

	o.mu.Lock()
	wasFailing := o.failing
	o.failing = err != nil
	wasAlarming := o.alarming
	o.alarming = nowAlarming
	// A destination on the same filesystem is worth saying once, not hourly:
	// it looks exactly like protection while surviving nothing the local
	// generations don't. Said on the first success, because a warning about a
	// destination that isn't working yet is noise on top of noise.
	warnSameDevice := err == nil && state.SameDevice && !o.warnedSameDevice
	if warnSameDevice {
		o.warnedSameDevice = true
	}
	o.mu.Unlock()

	switch {
	case err != nil && !wasFailing:
		o.log("daemon: offsite backup to %s is failing: %v", destDir, err)
	case err == nil && wasFailing:
		o.log("daemon: offsite backup to %s is working again", destDir)
	}
	if warnSameDevice {
		o.log("daemon: offsite destination %s shares a filesystem with the database; "+
			"it does not survive losing the disk", destDir)
	}
	if nowAlarming && !wasAlarming {
		o.log("daemon: offsite backup to %s reached alarm: %s", destDir, store.RenderOffsiteDetail(health))
	}
}
