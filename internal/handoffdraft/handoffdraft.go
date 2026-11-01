// Package handoffdraft is the per-session draft the Stop hook writes at the
// end of every interactive turn and pigtaild promotes into a "handoff-auto"
// subject_log line when the session ends. The directory is resolved by each
// side the way the wait markers are (next to the daemon socket), so the hook
// and the daemon agree by construction.
package handoffdraft

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxTLDRBytes bounds the TL;DR kept in a draft.
	MaxTLDRBytes = 4000
	// MaxTailRunes bounds the fallback tail when no TL;DR marker exists.
	MaxTailRunes = 1500
	// MaxFileBytes bounds a draft file read.
	MaxFileBytes = 16 * 1024

	tldrMarker = "**TL;DR**"
)

// Draft is what the hook knows at the end of a turn.
type Draft struct {
	SessionID string    `json:"session_id"`
	TurnID    string    `json:"turn_id"`
	TurnAt    time.Time `json:"turn_at"` // start of the turn read on the whole transcript, never the Stop time
	Cwd       string    `json:"cwd,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	TLDR      string    `json:"tldr,omitempty"` // text after the last **TL;DR** marker
	Tail      string    `json:"tail,omitempty"` // bounded fallback when there is no marker
}

func validSession(id string) error {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00") || strings.Contains(id, "..") {
		return fmt.Errorf("handoffdraft: invalid session id %q", id)
	}
	return nil
}

func path(dir, session string) string  { return filepath.Join(dir, session+".json") }
func handledPath(dir, s string) string { return filepath.Join(dir, s+".handled") }
func writeAtomic(dir, final string, b []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".draft-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Write stores the draft atomically (tmp 0600 + rename; dir 0700).
func Write(dir string, d Draft) error {
	if err := validSession(d.SessionID); err != nil {
		return err
	}
	if d.TurnID == "" {
		return errors.New("handoffdraft: empty turn id")
	}
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return writeAtomic(dir, path(dir, d.SessionID), b)
}

// Read returns the session's draft. A missing draft satisfies os.IsNotExist.
func Read(dir, session string) (Draft, error) {
	var d Draft
	if err := validSession(session); err != nil {
		return d, err
	}
	f, err := os.Open(path(dir, session))
	if err != nil {
		return d, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, MaxFileBytes+1)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return d, err
	}
	if n > MaxFileBytes {
		return d, fmt.Errorf("handoffdraft: %s too large", session)
	}
	if err := json.Unmarshal(buf[:n], &d); err != nil {
		return Draft{}, fmt.Errorf("handoffdraft: %s: %w", session, err)
	}
	return d, nil
}

// Remove deletes the session's draft; absent is nil.
func Remove(dir, session string) error {
	if err := validSession(session); err != nil {
		return err
	}
	if err := os.Remove(path(dir, session)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RemoveIfTurn removes the draft only if it still carries turnID: the hook may
// have rewritten it for a later turn since the caller read it.
func RemoveIfTurn(dir, session, turnID string) (bool, error) {
	d, err := Read(dir, session)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if d.TurnID != turnID {
		return false, nil
	}
	if err := Remove(dir, session); err != nil {
		return false, err
	}
	return true, nil
}

// List returns the session ids holding a draft, sorted; a missing dir is empty.
func List(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || strings.HasPrefix(n, ".") || !strings.HasSuffix(n, ".json") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(n, ".json"))
	}
	sort.Strings(ids)
	return ids, nil
}

// MoveAside renames <session>.json to <session>.json.bad (a corrupt draft).
func MoveAside(dir, session string) error {
	if err := validSession(session); err != nil {
		return err
	}
	err := os.Rename(path(dir, session), path(dir, session)+".bad")
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// PurgeStale removes the "<session>.handled" and "<session>.json.bad" files
// whose mtime is older than maxAge. Live drafts (.json) are never touched. A
// missing directory is no error.
func PurgeStale(dir string, maxAge time.Duration, now time.Time) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var first error
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || (!strings.HasSuffix(n, ".handled") && !strings.HasSuffix(n, ".json.bad")) {
			continue
		}
		fi, err := e.Info()
		if err != nil || now.Sub(fi.ModTime()) <= maxAge {
			continue
		}
		if err := os.Remove(filepath.Join(dir, n)); err != nil && !os.IsNotExist(err) && first == nil {
			first = err
		}
	}
	return first
}

// MarkHandled records the last turn id the daemon promoted or abandoned.
func MarkHandled(dir, session, turnID string) error {
	if err := validSession(session); err != nil {
		return err
	}
	return writeAtomic(dir, handledPath(dir, session), []byte(turnID))
}

// ReadHandled returns the last handled turn id, "" if none.
func ReadHandled(dir, session string) string {
	if validSession(session) != nil {
		return ""
	}
	b, err := os.ReadFile(handledPath(dir, session))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ExtractTLDR returns the text after the last **TL;DR** marker (≤ 4000 bytes,
// cut on a rune boundary). Without a marker it returns the bounded tail.
func ExtractTLDR(msg string) (tldr, tail string) {
	if i := strings.LastIndex(msg, tldrMarker); i >= 0 {
		t := strings.TrimSpace(msg[i+len(tldrMarker):])
		if len(t) > MaxTLDRBytes {
			t = t[:MaxTLDRBytes]
			for !utf8.ValidString(t) {
				t = t[:len(t)-1]
			}
		}
		return t, ""
	}
	r := []rune(strings.TrimSpace(msg))
	if len(r) > MaxTailRunes {
		r = r[len(r)-MaxTailRunes:]
	}
	return "", string(r)
}
