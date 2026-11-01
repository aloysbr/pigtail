package websession

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SessionFileName is the session secret's file, in the pigtail config dir.
// Deleting it (then restarting pigtaild) signs every browser out.
const SessionFileName = "web-session"

// LegacySessionFileName is the former name of the secret's file, adopted
// (renamed) at load so the secret is kept. The cookie that carries it changed
// name (`pigtail_web`), so browsers and the app are signed out once.
const LegacySessionFileName = "cockpit-session"

// Session is the secret every signed-in browser's cookie carries.
type Session struct {
	secret string
}

// LoadSession reads the secret at path, creating it (0600, 256 random bits)
// when absent — the same pattern as the API token.
//
// A secret still under the legacy file name is adopted first: renamed to path
// (when path does not exist), so the secret is kept (the cookie name changed,
// so every browser and the app sign in again once). A rename
// error is returned, never swallowed into a fresh secret.
func LoadSession(path string) (*Session, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		legacy := filepath.Join(filepath.Dir(path), LegacySessionFileName)
		if _, lerr := os.Stat(legacy); lerr == nil {
			if err := os.Rename(legacy, path); err != nil {
				return nil, fmt.Errorf("adopt legacy web session file %s: %w", legacy, err)
			}
		}
	}
	if b, err := os.ReadFile(path); err == nil {
		v := strings.TrimSpace(string(b))
		if v == "" {
			return nil, fmt.Errorf("web session file %s is empty: delete it to make a new one", path)
		}
		return &Session{secret: v}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	v := hex.EncodeToString(b[:])
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(v + "\n"); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return &Session{secret: v}, nil
}

// Value is the secret, set as the cookie's value at login.
func (s *Session) Value() string { return s.secret }

// Check reports whether v is the secret, in constant time. A nil session or
// an empty value never matches.
func (s *Session) Check(v string) bool {
	if s == nil || s.secret == "" || v == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(v), []byte(s.secret)) == 1
}
