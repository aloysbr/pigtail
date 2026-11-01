package maintenance

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Watch caches Load by the file's mtime and size: the daemon asks on every
// guarded request and every sidebar tick, and pays one stat.
type Watch struct {
	dir string
	mu  sync.Mutex
	mod time.Time
	sz  int64
	has bool
	st  State
	ok  bool
	err error
}

func NewWatch(dir string) *Watch { return &Watch{dir: dir} }

// SealsHeld says whether the store must hold focus seals (focus D26): true
// under a freeze, and on an unreadable state (D8, frozen, like FreezeMW);
// false with no watch wired, no maintenance, or a maintenance without
// --freeze. The daemon asks as a session-less caller.
func SealsHeld(w *Watch) bool {
	if w == nil {
		return false
	}
	st, ok, err := w.Current()
	if err != nil {
		return true
	}
	return ok && st.FrozenFor("")
}

func (w *Watch) Dir() string { return w.dir }

func (w *Watch) Current() (State, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fi, err := os.Stat(filepath.Join(w.dir, FileName))
	if errors.Is(err, fs.ErrNotExist) {
		// ONLY a missing file means no maintenance. Load reads the small
		// token for the lifted state (one-time notice); never frozen.
		w.has, w.st, w.ok, w.err = false, State{}, false, nil
		return Load(w.dir)
	}
	if err != nil {
		// D8: any other Stat error (permission, I/O) is an unreadable
		// state, hence frozen — never "no maintenance".
		w.has = false
		return State{}, false, causeOnly(err) // the path is named by the message, once
	}
	if w.has && fi.ModTime().Equal(w.mod) && fi.Size() == w.sz {
		return w.st, w.ok, w.err
	}
	w.st, w.ok, w.err = Load(w.dir)
	w.has, w.mod, w.sz = true, fi.ModTime(), fi.Size()
	return w.st, w.ok, w.err
}
