package daemon

// This file is task 8's own new surface: POST /backup/offsite/run's "I just
// fixed something, prove it right now" trigger, as opposed to waiting for
// the hourly primary rotation (backup.go) or the six-hourly fallback
// exercise (offsite_fallback.go). It adds no new state and no new I/O of its
// own — every line here is a thin, exported wrapper around behaviour
// offsite.go and offsite_fallback.go already own, so handlers.RunOffsiteBackup
// (internal/daemon/handlers/offsite_run.go) has something to call across the
// package boundary without offsiteBackupper itself ever needing to become
// exported.
//
// Both methods below return before either route's own I/O has run: RunOffsiteNow
// is run() itself, which already only ever blocks on a mutex; RunOffsiteFallbackNow
// dispatches doFallbackExercise onto its own goroutine rather than running it
// inline the way exerciseFallbackIfDue does — inline is fine there because
// runOffsiteFallback's ticker loop is already off the request path, but this
// method is called from directly inside an HTTP handler, and blocking it for
// up to offsiteFallbackExerciseTimeout would block the response for as long
// as a hung SSH handshake takes.

// RunOffsiteNow starts an immediate primary copy unless one is already in
// flight, reporting whether it actually started. A direct call to run() —
// see that method's own doc comment (offsite.go) for the in-flight guard
// this reports.
func (o *offsiteBackupper) RunOffsiteNow(dbPath, destDir string) bool {
	return o.run(dbPath, destDir)
}

// RunOffsiteFallbackNow runs one fallback exercise immediately, bypassing
// the 6h due-check exerciseFallbackIfDue applies — a human explicitly asked
// for this, right now, not a schedule — but never bypassing
// fallbackGuardsOK's three correction guards (offsite_fallback.go): an
// empty, malformed, or same-as-primary destination, or a store with no
// file-backed state to persist to, still exercises nothing here either.
//
// Guarded by fbInFlight, the exact same field exerciseFallbackIfDue's own
// scheduled path acquires through acquireFallbackSlot — deliberately the
// same field, not a second one: this is still the one fallback route, and
// the scheduled six-hourly tick and this explicit trigger must never be able
// to run two probes against it at once. inFlight (the primary copy's own
// guard) is a different field entirely, acquired only by RunOffsiteNow/run/
// runSync — see fbInFlight's own doc comment (offsite.go) for why the two
// are kept apart: the primary route may start while the fallback route is
// still busy, and vice versa, which a single shared guard could not express.
//
// started reports whether an exercise was actually launched: false when a
// guard refused it, or one was already running. configured reports whether
// a fallback destination string is set at all, independent of whether it is
// valid — the same "set, not necessarily usable" meaning
// store.OffsiteFallbackHealth.Configured carries on the read side
// (store/offsite_fallback.go): a malformed or same-as-primary alias still
// reads configured=true here, started=false, distinct from the empty-string
// case where both read false.
func (o *offsiteBackupper) RunOffsiteFallbackNow(dbPath, destDir, fallbackDest string) (started, configured bool) {
	configured = fallbackDest != ""
	if !o.fallbackGuardsOK(dbPath, destDir, fallbackDest) {
		return false, configured
	}
	if !o.acquireFallbackSlot() {
		return false, configured
	}
	o.copies.Add(1)
	go func() {
		defer o.copies.Done()
		defer o.releaseFallbackSlot()
		o.doFallbackExercise(dbPath, destDir, fallbackDest)
	}()
	return true, configured
}
