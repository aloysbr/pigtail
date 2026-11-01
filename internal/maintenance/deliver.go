package maintenance

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
)

// TokenKey is the cache key the bash fast path computes the same way
// (the maintenance hook script): it changes whenever there may be
// something new to say. Contract table T1–T7.
func TokenKey(dir string, now time.Time) string {
	raw, err := os.ReadFile(filepath.Join(dir, TokenName))
	if err != nil {
		return ""
	}
	f := strings.Fields(string(raw))
	if len(f) != 3 || !strings.HasPrefix(f[0], "mnt_") {
		return "bad"
	}
	epoch, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil {
		return "bad"
	}
	switch f[1] {
	case "on":
		if epoch > 0 && now.Unix() > epoch {
			return f[0] + ":late"
		}
		return f[0] + ":on"
	case "off":
		return f[0] + ":off" // no grace: kept until the next `on` replaces the token
	}
	return "bad"
}

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

// WriteWitness is called by `pigtail hook claude-maintenance` and nothing
// else (D12: one writer).
func WriteWitness(dir, session string, w Witness) error {
	if !ValidSession(session) {
		return os.ErrInvalid
	}
	d := filepath.Join(dir, SeenDirName)
	// Errors carry the cause only (causeOnly): the caller names the witness path.
	if err := os.MkdirAll(d, 0o700); err != nil {
		return causeOnly(err)
	}
	return causeOnly(writeAtomic(filepath.Join(d, session), []byte(w.Key+"|"+w.Shown+"\n")))
}

// LockSession takes the per-session exclusive claim for one judge cycle
// (flock LOCK_EX|LOCK_NB on maintenance-seen/<sid>.lock). ok=false: another
// call holds it — the caller says nothing.
func LockSession(dir, session string) (unlock func(), ok bool, err error) {
	if !ValidSession(session) {
		return nil, false, os.ErrInvalid
	}
	d := filepath.Join(dir, SeenDirName)
	// Errors carry the cause only (causeOnly): the caller names the .lock path.
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

type Decision struct {
	Show            bool
	ID, Phase, Text string
	Next            Witness
	// State is the state judged (zero for the corrupt phase): the envelope
	// reads who set or lifted it from here.
	State State
}

// CorruptNotice is the session-facing notice for an unreadable state (French,
// D14 header). UnreadableMessage stays the English refusal of the daemon and CLI.
func CorruptNotice(dir string, err error) string {
	return "Message système pigtail — l'état de maintenance est illisible (" + filepath.Join(dir, FileName) + " : " + err.Error() +
		"). Tout ce que couvre le gel reste gelé, merges des agents compris. Ne relance pas un geste refusé, ne le contourne pas. " +
		"Réparation par " + human() + " seul, hors de Claude Code (`pigtail maintenance off`) : qui l'a posée est illisible aussi. Dis-lui dans ta prochaine réponse que tu as reçu cet avis.\n"
}

// Decide is the only comparison (D12): at most one notice per <id>:<phase>.
// FastKey is TokenKey plus the presence of the JSON: what the bash fast path
// compares with the witness (the JSON content is covered by -nt).
func FastKey(dir string, now time.Time) string {
	if _, err := os.Stat(filepath.Join(dir, FileName)); errors.Is(err, fs.ErrNotExist) {
		return TokenKey(dir, now) + "+-"
	}
	return TokenKey(dir, now) + "+j"
}

func Decide(dir string, w Witness, now time.Time, session string, reader scope.Scope) Decision {
	d := Decision{Next: Witness{Key: FastKey(dir, now), Shown: w.Shown}}
	if d.Next.Key == "+-" {
		return d
	}
	st, ok, err := Load(dir)
	switch {
	case err != nil:
		d.ID, d.Phase, d.Text = RawTokenID(dir), "corrupt", CorruptNotice(dir, err)
	case ok && st.On:
		d.ID, d.Phase, d.Text, d.State = st.ID, st.Phase(now), Render(st, now, session, reader), st
	case ok:
		d.ID, d.Phase, d.Text, d.State = st.ID, "off", Render(st, now, session, reader), st
	default:
		return d
	}
	shown := d.ID + ":" + d.Phase
	if w.Shown == shown || (d.Phase == "off" && !strings.HasPrefix(w.Shown, d.ID+":")) {
		return d
	}
	d.Show, d.Next.Shown = true, shown
	return d
}
