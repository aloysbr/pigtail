package maintenance

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FrozenDirName holds one marker per session a freeze refused
// (<dir>/maintenance-frozen/<session>, content: the maintenance id): the
// sidebar shows it "Blocked · freeze" until the lift (subject
// sub_x000000000000032, D4). Written by the daemon's FreezeMW, by
// `pigtail session spawn`, and by the user's merge guards — same format.
const FrozenDirName = "maintenance-frozen"

// MarkFrozen records that the maintenance id refused a gesture of session.
func MarkFrozen(dir, session, id string) error {
	if !ValidSession(session) || id == "" {
		return errors.New("maintenance: frozen marker needs a session and an id")
	}
	d := filepath.Join(dir, FrozenDirName)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(d, session), []byte(id+"\n"))
}

// FrozenSessions lists the sessions refused by st, while st is on with a
// freeze; a marker of another maintenance is ignored.
func FrozenSessions(dir string, st State) map[string]bool {
	if !st.On || !st.Freeze || st.ID == "" {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(dir, FrozenDirName))
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !ValidSession(e.Name()) || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, FrozenDirName, e.Name()))
		if err == nil && strings.TrimSpace(string(raw)) == st.ID {
			out[e.Name()] = true
		}
	}
	return out
}

// ClearFrozen removes every marker (the lift).
func ClearFrozen(dir string) error {
	err := os.RemoveAll(filepath.Join(dir, FrozenDirName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
