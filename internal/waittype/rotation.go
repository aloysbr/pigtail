package waittype

import "strings"

// The refusal texts of the rotation worker (cmd/pigtail/session_rotate.go)
// that a later try can lift: the screen shows no input box yet, or the
// session's native file is not there yet. They are shared so the worker's
// retry and the relay's screen rule cannot drift apart.
const (
	RotationNoInputBox   = "no input box on screen"
	RotationNoNativeFile = "no native session file"
)

// RotationTransient reports whether a rotation refusal is one a later try can
// lift. "launcher proto unreadable" is not: it comes from the worker's frozen
// environment, a new try would read the same value.
func RotationTransient(why string) bool {
	return strings.Contains(why, RotationNoInputBox) || strings.Contains(why, RotationNoNativeFile)
}
