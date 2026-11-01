package cmuxsync

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// The folded state of the folders, by folder name (spec 7,
// sub_x000000000000041): a folder emptied, deleted and recreated is born in
// the state the user last left it in, even after a daemon restart. A JSON file
// next to the daemon's database (cmux-folders.json, wired by pigtaild), not a
// table: interface memory rewritten whole, no query, no migration, and a
// branch daemon (--db elsewhere) never overwrites the real one's.

// LoadCollapseFile reads the folded state. An absent file is an empty map; an
// unreadable or invalid one is an error, never a partial map.
func LoadCollapseFile(path string) (map[string]bool, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the daemon's own state file
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]bool
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("cmux folders state %s: %w", path, err)
	}
	if m == nil {
		m = map[string]bool{}
	}
	return m, nil
}

// SaveCollapseFile writes the folded state atomically (see writeFileAtomic).
func SaveCollapseFile(path string, m map[string]bool) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b)
}

// writeFileAtomic writes b to path through a temporary file, then a rename: a
// crash leaves the old file or the new one, never half of one. Shared by the
// daemon's small state files (folded state, birth clock).
func writeFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
