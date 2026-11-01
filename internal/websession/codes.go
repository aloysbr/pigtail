// Package websession holds the web front's login primitives: single-use login
// codes (memory only) and the session secret its cookie carries (a 0600
// file, so a daemon restart keeps the browser signed in).
package websession

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"sync"
	"time"
)

// CodeTTL is how long a login code stays redeemable.
const CodeTTL = 2 * time.Minute

// MaxPending caps the codes waiting to be redeemed; issuing past it evicts
// the oldest.
const MaxPending = 16

// codeBytes is the code's entropy: 256 bits.
const codeBytes = 32

// Codes issues and redeems single-use login codes. Codes live in memory
// only: a daemon restart invalidates them, on purpose.
type Codes struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	pending map[[sha256.Size]byte]time.Time // code hash → issued at
}

// NewCodes returns an empty code set. now is the clock (time.Now in
// production, a fake in tests).
func NewCodes(ttl time.Duration, now func() time.Time) *Codes {
	if now == nil {
		now = time.Now
	}
	return &Codes{ttl: ttl, now: now, pending: map[[sha256.Size]byte]time.Time{}}
}

// Issue returns a fresh code (raw URL base64). Expired codes are purged
// first; past MaxPending the oldest pending code is dropped.
func (c *Codes) Issue() (string, error) {
	var b [codeBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	code := base64.RawURLEncoding.EncodeToString(b[:])
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, at := range c.pending {
		if now.Sub(at) >= c.ttl {
			delete(c.pending, k)
		}
	}
	for len(c.pending) >= MaxPending {
		var oldest [sha256.Size]byte
		var oldestAt time.Time
		first := true
		for k, at := range c.pending {
			if first || at.Before(oldestAt) {
				oldest, oldestAt, first = k, at, false
			}
		}
		delete(c.pending, oldest)
	}
	c.pending[sha256.Sum256([]byte(code))] = now
	return code, nil
}

// Redeem consumes code: true once for a code issued less than the TTL ago,
// false otherwise (unknown, already used, expired). Codes are keyed by
// their hash, so the lookup's timing says nothing about a valid code.
func (c *Codes) Redeem(code string) bool {
	if code == "" {
		return false
	}
	k := sha256.Sum256([]byte(code))
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := c.pending[k]
	if !ok {
		return false
	}
	delete(c.pending, k)
	return c.now().Sub(at) < c.ttl
}

func (c *Codes) pendingCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}
