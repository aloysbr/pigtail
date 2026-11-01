//go:build !unix

package devserver

// Without a POSIX session to derive an identity from, a session stays
// anonymous — exactly the pre-existing behaviour. Windows is not a supported
// target today; this file exists so adding it would not require touching the
// ownership logic.

const sessionPrefix = "sid:"

func sessionIdentity() string { return "" }

// Without a way to interrogate a session, every identity is reported alive —
// the conservative direction: a spared server stays spared.
func OwnerAlive(string) bool { return true }
