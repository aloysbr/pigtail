package runtimeclaim

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// readToken parses « <rtc_id> on|off <epoch> »; ok=false when absent or malformed.
func readToken(dir string) (id, phase string, epoch int64, ok bool) {
	raw, err := os.ReadFile(filepath.Join(dir, TokenName))
	if err != nil {
		return "", "", 0, false
	}
	f := strings.Fields(string(raw))
	if len(f) != 3 || !strings.HasPrefix(f[0], "rtc_") || (f[1] != "on" && f[1] != "off") {
		return "", "", 0, false
	}
	// digits only, like the bash grammar (ParseInt alone accepts « +1 » and « -1 »)
	if strings.Trim(f[2], "0123456789") != "" {
		return "", "", 0, false
	}
	e, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil {
		return "", "", 0, false
	}
	return f[0], f[1], e, true
}

// TokenKey is the cache key the hook script computes the
// same way: « <id>:on|off » from the token ("" when absent, "bad" when
// malformed), then « +j » or « +- » for the presence of the JSON.
func TokenKey(dir string) string {
	key := ""
	if _, err := os.Stat(filepath.Join(dir, TokenName)); err == nil {
		key = "bad"
		if id, phase, _, ok := readToken(dir); ok {
			key = id + ":" + phase
		}
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); errors.Is(err, fs.ErrNotExist) {
		return key + "+-"
	}
	return key + "+j"
}

// ValidSession: a session id usable as a file name (maintenance.ValidSession).
func ValidSession(s string) bool {
	return s != "" && !strings.ContainsAny(s, `/\`) && !strings.Contains(s, "..") && len(s) <= 128
}

// Witness is what one session was last told: Key = TokenKey at that judgement,
// Shown = « <id>:on|off » of the last notice shown.
type Witness struct{ Key, Shown string }

func ReadWitness(dir, session string) Witness {
	if !ValidSession(session) {
		return Witness{}
	}
	raw, err := os.ReadFile(filepath.Join(dir, SeenDirName, session))
	if err != nil {
		return Witness{}
	}
	k, sh, _ := strings.Cut(strings.TrimSpace(string(raw)), "|")
	return Witness{Key: k, Shown: sh}
}

// WriteWitness is called by `pigtail hook claude-runtime-claim` only (one writer).
func WriteWitness(dir, session string, w Witness) error {
	if !ValidSession(session) {
		return os.ErrInvalid
	}
	d := filepath.Join(dir, SeenDirName)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return causeOnly(err)
	}
	return writeAtomic(filepath.Join(d, session), []byte(w.Key+"|"+w.Shown+"\n"))
}

// LockSession takes the per-session claim for one judge cycle (non-blocking:
// ok=false means another call speaks for this session).
func LockSession(dir, session string) (unlock func(), ok bool, err error) {
	if !ValidSession(session) {
		return nil, false, os.ErrInvalid
	}
	d := filepath.Join(dir, SeenDirName)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return nil, false, causeOnly(err)
	}
	f, err := os.OpenFile(filepath.Join(d, session+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, causeOnly(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, true, nil
}
