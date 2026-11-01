// Package maintenance holds the system maintenance state set with `pigtail
// maintenance on|off` — by a Claude session, which then owns it, or by the user
// outside Claude Code (D16): one JSON file plus a one-line token
// that shell hooks read without parsing JSON. The daemon (freeze), the sidebar
// loop (pill), `pigtail hook claude-maintenance` (the notice, single delivery
// path, D12) and the user's own guards all read the same file, so the notice and
// the merge freeze work with the daemon down. Never expires (D4).
package maintenance

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	FileName       = "maintenance.json"
	TokenName      = "maintenance.token"
	SeenDirName    = "maintenance-seen"
	MaxReasonBytes = 500

	// LiftedName records, at lift time, whether the lifted maintenance had a
	// freeze and who lifted ("<id> freeze|nofreeze|unknown[ user|session
	// <label>]"): the state file is gone after `off`, and the lift notice says
	// « Tout est rouvert » only when needed, and by whom it was lifted.
	LiftedName = "maintenance.lifted"
)

type State struct {
	ID          string     `json:"id"`
	On          bool       `json:"on"`
	Reason      string     `json:"reason"`
	Freeze      bool       `json:"freeze"`
	Until       *time.Time `json:"until,omitempty"`
	Exempt      string     `json:"exempt,omitempty"`
	ExemptLabel string     `json:"exempt_label,omitempty"`
	// ExemptScope / OwnerScope (D22): the scope of the labelled session, as
	// /peers gave it when the label was read. Empty (an older state, or a
	// label that is only the id) = shown to the master only.
	ExemptScope string     `json:"exempt_scope,omitempty"`
	Since       time.Time  `json:"since"`
	LiftedAt    *time.Time `json:"lifted_at,omitempty"`
	// LiftedFreezeKnown: on a lifted state rebuilt from the token, Freeze
	// comes from maintenance.lifted of the same id; false = unknown.
	LiftedFreezeKnown bool `json:"-"`
	// Owner is the Claude session that ran `on` (CLAUDE_CODE_SESSION_ID); empty
	// = the user outside Claude Code. Exempt from the freeze; with the user, the only
	// caller that may lift it (D16).
	Owner      string `json:"owner,omitempty"`
	OwnerLabel string `json:"owner_label,omitempty"`
	OwnerScope string `json:"owner_scope,omitempty"`
	// On a lifted state rebuilt from the token: LiftedByUser = lifted with no
	// session; LiftedBy = the label of the session that lifted, LiftedByID its
	// id and LiftedByScope its scope (both empty in the older « session
	// <label> » form). All unset when maintenance.lifted does not say (older
	// binary).
	LiftedBy      string `json:"-"`
	LiftedByID    string `json:"-"`
	LiftedByScope string `json:"-"`
	LiftedByUser  bool   `json:"-"`
}

func NewID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "mnt_" + hex.EncodeToString(b[:])
}

// Load: the JSON exists only while a maintenance is on. Its errors carry the
// cause, not the path (see causeOnly). Without it, the
// token may still say which maintenance was lifted and when (for the one-time
// « levée » notice); that state is On=false: it freezes nothing.
func Load(dir string) (State, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if errors.Is(err, fs.ErrNotExist) {
		return liftedFromToken(dir)
	}
	if err != nil {
		return State{}, false, causeOnly(err)
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return State{}, false, err
	}
	if st.ID == "" {
		return State{}, false, errors.New("no id")
	}
	return st, true, nil
}

// causeOnly drops the state file's path from a filesystem error ("open
// <path>: permission denied" → "open: permission denied"). The path has one
// source: the messages that report an unreadable state (UnreadableMessage,
// CorruptNotice) name it themselves. errors.Is still sees the cause.
func causeOnly(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s: %w", pe.Op, pe.Err)
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return fmt.Errorf("%s: %w", le.Op, le.Err)
	}
	return err
}

func Save(dir string, st State) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, FileName), append(raw, '\n')); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, TokenName), []byte(TokenLine(st)))
}

// Lift ends a maintenance: maintenance.lifted records the freeze and who
// lifted (byID = "" for the user outside Claude Code, else the lifting session's
// id, label and scope — D22: the lift notice names it per reader); the token says « <id> off <epoch> » (the one-time
// lift notice reads it), then the state file is removed — absence means no
// maintenance. Works on a corrupt file (D8 repair). Order matters: a crash
// between the two leaves the JSON (still frozen, safe side) and `off` again
// finishes the job.
func Lift(dir, id string, now time.Time, byID, byLabel, byScope string) error {
	if id == "" {
		id = NewID()
	}
	// Best effort, before the token: a failure leaves the freeze unknown
	// (the lift notice then keeps « Tout est rouvert »), never a broken lift.
	_ = writeAtomic(filepath.Join(dir, LiftedName), []byte(id+" "+liftedFreeze(dir, id)+" "+lifterField(byID, byLabel, byScope)+"\n"))
	if err := writeAtomic(filepath.Join(dir, TokenName), []byte(fmt.Sprintf("%s off %d\n", id, now.Unix()))); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, FileName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncDir(dir)
}

// lifterField is the third field of maintenance.lifted: "user" (no
// session) or "sid <id> <scope|-> <label>" (the label is the rest of the
// line, newlines flattened). An older binary wrote "session <label>".
func lifterField(byID, byLabel, byScope string) string {
	if byID == "" {
		return "user"
	}
	if byScope == "" {
		byScope = "-"
	}
	return "sid " + byID + " " + byScope + " " + strings.ReplaceAll(byLabel, "\n", " ")
}

// liftedFreeze reads, from the state file about to be deleted, whether the
// maintenance `id` had a freeze: "freeze", "nofreeze", or "unknown" when the
// file is unreadable or names another id.
func liftedFreeze(dir, id string) string {
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		return "unknown"
	}
	var st State
	if json.Unmarshal(raw, &st) != nil || st.ID != id {
		return "unknown"
	}
	if st.Freeze {
		return "freeze"
	}
	return "nofreeze"
}

func liftedFromToken(dir string) (State, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, TokenName))
	if err != nil {
		return State{}, false, nil
	}
	f := strings.Fields(string(raw))
	if len(f) != 3 || f[1] != "off" || !strings.HasPrefix(f[0], "mnt_") {
		return State{}, false, nil // no JSON: no maintenance, whatever the token says
	}
	sec, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil {
		return State{}, false, nil
	}
	at := time.Unix(sec, 0)
	st := State{ID: f[0], On: false, LiftedAt: &at}
	// The freeze and the lifter are known only from maintenance.lifted of
	// this same id: "<id> <freeze|nofreeze|unknown>[ user|session <label>]"
	// (the two-field form of an older binary still reads, lifter unknown).
	if raw, err := os.ReadFile(filepath.Join(dir, LiftedName)); err == nil {
		lf := strings.SplitN(strings.TrimRight(string(raw), "\n"), " ", 3)
		if len(lf) >= 2 && lf[0] == st.ID {
			if lf[1] == "freeze" || lf[1] == "nofreeze" {
				st.Freeze, st.LiftedFreezeKnown = lf[1] == "freeze", true
			}
			if len(lf) == 3 {
				switch by := lf[2]; {
				case strings.HasPrefix(by, "sid "):
					if f := strings.SplitN(by, " ", 4); len(f) == 4 && f[1] != "" {
						st.LiftedByID, st.LiftedBy = f[1], f[3]
						if f[2] != "-" {
							st.LiftedByScope = f[2]
						}
					}
				case strings.HasPrefix(by, "session ") && len(by) > len("session "):
					st.LiftedBy = strings.TrimPrefix(by, "session ")
				case by != "" && !strings.HasPrefix(by, "sid") && !strings.HasPrefix(by, "session"):
					// "user", or the word an older binary wrote for the
					// person: any third field that names no session is a lift
					// by the user, with no session.
					st.LiftedByUser = true
				}
			}
		}
	}
	return st, true, nil
}

// UnknownID names a maintenance whose token cannot be read. It is a valid
// token id (mnt_ prefix), so `off` writes a readable lift for it and the
// sessions that saw the corrupt phase under it get that lift.
const UnknownID = "mnt_unknown"

// RawTokenID reads the id from the token line, UnknownID if it cannot.
func RawTokenID(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, TokenName))
	if err != nil {
		return UnknownID
	}
	if f := strings.Fields(string(raw)); len(f) > 0 && strings.HasPrefix(f[0], "mnt_") {
		return f[0]
	}
	return UnknownID
}

// syncFile is a seam: TestSaveInterruptedKeepsOldFile makes it fail.
var syncFile = (*os.File).Sync

// writeAtomic: temp file in the same directory, fsync, rename, fsync of the
// directory. A half-written state file cannot exist under its real name.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := syncFile(tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

func (s State) Late(now time.Time) bool { return s.On && s.Until != nil && now.After(*s.Until) }

func (s State) Phase(now time.Time) string {
	switch {
	case !s.On:
		return "off"
	case s.Late(now):
		return "late"
	}
	return "on"
}

func (s State) Token(now time.Time) string {
	return s.ID + ":" + s.Phase(now) // off: no grace, the lift stays until the next on
}

// FrozenFor: under a freeze, the owner and the exempt session pass; the
// header-less caller (empty session) is always frozen.
func (s State) FrozenFor(session string) bool {
	if !s.On || !s.Freeze {
		return false
	}
	return session == "" || (session != s.Owner && session != s.Exempt)
}

func TokenLine(s State) string {
	if s.On {
		var until int64
		if s.Until != nil {
			until = s.Until.Unix()
		}
		return fmt.Sprintf("%s on %d\n", s.ID, until)
	}
	var lifted int64
	if s.LiftedAt != nil {
		lifted = s.LiftedAt.Unix()
	}
	return fmt.Sprintf("%s off %d\n", s.ID, lifted)
}

func ParseUntil(v string, now time.Time) (time.Time, error) {
	v = strings.TrimSpace(v)
	if d, err := time.ParseDuration(v); err == nil {
		if d <= 0 {
			return time.Time{}, fmt.Errorf("--until %q: a duration must be positive", v)
		}
		return now.Add(d), nil
	}
	if hm, err := time.ParseInLocation("15:04", v, now.Location()); err == nil {
		t := time.Date(now.Year(), now.Month(), now.Day(), hm.Hour(), hm.Minute(), 0, 0, now.Location())
		if !t.After(now) {
			return time.Time{}, fmt.Errorf("--until %q is already past today — give a date (2026-10-05T09:00)", v)
		}
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04", v, now.Location()); err == nil && t.After(now) {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--until %q: want a duration (2h), a time today (18:30) or a future date (2026-10-05T09:00)", v)
}

// CanLift: the owner session, or the user outside Claude Code (no Claude session).
// An the user-owned maintenance (Owner empty) is lifted by the user alone.
func (s State) CanLift(session string) bool {
	return session == "" || (s.Owner != "" && session == s.Owner)
}

// ValidSession: a session id usable as a file name (witness, lock) and as an
// owner: non-empty, no path separator, no "..", at most 128 bytes.

func ValidSession(s string) bool {
	return s != "" && !strings.ContainsAny(s, `/\`) && !strings.Contains(s, "..") && len(s) <= 128
}
