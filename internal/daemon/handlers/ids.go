package handlers

import (
	"crypto/rand"
	"encoding/hex"
)

// genID returns prefix followed by 16 hex chars of randomness. Matches the
// daemon's existing id scheme (8 random bytes, hex-encoded).
func genID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}
