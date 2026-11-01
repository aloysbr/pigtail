// Package roles holds the pure core of role supervision: the deadline of a
// role, the judge of an observation and the episode decision. Nothing here
// reads a file, a clock or the store.
package roles

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	alertPrefix     = "role-silent:"
	recoveredPrefix = "role-recovered:"
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidName reports whether name is a legal role name.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// AlertID is the journal id of the alert of one episode.
func AlertID(name string, key time.Time) string {
	return alertPrefix + name + ":" + strconv.FormatInt(key.UnixMilli(), 10)
}

// RecoveredID is the journal id that closes the episode of that key.
func RecoveredID(name string, key time.Time) string {
	return recoveredPrefix + name + ":" + strconv.FormatInt(key.UnixMilli(), 10)
}

// ParseAlertID splits "role-silent:<name>:<ms>".
func ParseAlertID(id string) (name string, key time.Time, ok bool) {
	rest, found := strings.CutPrefix(id, alertPrefix)
	if !found {
		return "", time.Time{}, false
	}
	i := strings.LastIndexByte(rest, ':')
	if i <= 0 {
		return "", time.Time{}, false
	}
	ms, err := strconv.ParseInt(rest[i+1:], 10, 64)
	if err != nil || !ValidName(rest[:i]) {
		return "", time.Time{}, false
	}
	return rest[:i], time.UnixMilli(ms).UTC(), true
}

// ParseRecoveredID splits "role-recovered:<name>:<ms>".
func ParseRecoveredID(id string) (name string, key time.Time, ok bool) {
	rest, found := strings.CutPrefix(id, recoveredPrefix)
	if !found {
		return "", time.Time{}, false
	}
	return ParseAlertID(alertPrefix + rest)
}
