package handlers

import "log"

// logf writes a daemon-side diagnostic. It follows the house pattern (see
// watchdog.WithLogger / rammon): an injectable sink with a log.Printf default,
// so tests can capture output and the daemon needs no extra plumbing.
//
// Nil-safe on both the receiver and the field, so the many Services{…}
// literals in tests keep working untouched.
//
// log.Printf writes to stderr, which the launchd plist redirects to
// ~/.config/pigtail/daemon.log — that is where these lines end up in
// production.
func (s *Services) logf(format string, args ...any) {
	if s != nil && s.Logf != nil {
		s.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}
