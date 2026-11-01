package rammon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The activity tracker and the death ring are in-memory only, so a pigtaild
// restart wipes both: the first sweep has no prior CPU sample, every idle
// counter resets to seconds, and a server that died around the restart is never
// reported because nothing remembers it existed. This file is what carries the
// two across a restart.
//
// It is a cache, not a record. Losing it costs exactly the behaviour of before
// this change, which is why every read failure falls back to a blank slate
// rather than stopping the daemon, and why it lives in a plain JSON file next
// to the database rather than in the database itself: no schema, no migration,
// and no periodic writes into a file that has no backup rotation.

// stateVersion is bumped whenever the on-disk shape changes. A file from
// another version is discarded rather than guessed at.
const stateVersion = 1

// stateFileName is the basename written next to the daemon's database.
const stateFileName = "rammon-state.json"

// deathRestoreWindow bounds how long after a save a restored key set may still
// be reported as deaths.
//
// A daemon redeploy takes seconds, so the state file is fresh and a server
// whose port is free on the first sweep genuinely died while we were not
// looking — exactly the gap this file closes. A machine reboot also leaves
// every persisted port free, but there the servers did not die, they were never
// restarted; reporting them would be a volley of false deaths on every boot.
// Wall-clock age is what separates the two without a platform-specific boot
// probe. It is a heuristic and it is stated as one: a reboot completing within
// the window produces spurious deaths, which expire on their own after
// deathWindow and kill nothing.
const deathRestoreWindow = 2 * time.Minute

// persistedSample is one point of an entry's activity window.
type persistedSample struct {
	At       time.Time `json:"at"`
	CPU      float64   `json:"cpu"`
	NewConns int       `json:"new_conns,omitempty"`
}

// persistedEntry is one activity tracker entry as written to disk.
type persistedEntry struct {
	Key        string    `json:"key"`
	LastCPU    float64   `json:"last_cpu"`
	LastActive time.Time `json:"last_active"`
	Port       int       `json:"port"`
	PIDs       []int     `json:"pids"`
	// Samples, Peers and PeersKnown carry the activity window across a
	// redeploy. Additive and omitted when empty, so stateVersion is
	// unchanged: an old binary ignores them, and a file without them (the
	// previous version's) loads with one sample seeded at SavedAt.
	Samples    []persistedSample `json:"samples,omitempty"`
	Peers      []string          `json:"peers,omitempty"`
	PeersKnown bool              `json:"peers_known,omitempty"`
	// Fresh keeps a fill in progress across a redeploy: an entry this
	// daemon's predecessor first saw minutes ago is still not idle-able.
	Fresh bool `json:"fresh,omitempty"`
}

// persistedPin is one pin as written to disk. Pins ride in the same cache file
// as the tracker, additively (stateVersion unchanged): an older binary ignores
// the field, and a file without it loads with no pin — exactly the behaviour
// before pins were persisted.
type persistedPin struct {
	Key     string    `json:"key"`
	Until   time.Time `json:"until,omitzero"`
	Durable bool      `json:"durable,omitempty"`
}

// persistedState is the whole file.
type persistedState struct {
	Version int              `json:"version"`
	SavedAt time.Time        `json:"saved_at"`
	Entries []persistedEntry `json:"entries"`
	Deaths  []Death          `json:"deaths"`
	Pins    []persistedPin   `json:"pins,omitempty"`
}

// StatePath returns the state file path for a daemon whose database is dbPath.
// Empty for an empty dbPath — an in-memory or unnamed database has no home to
// write beside, and persistence is simply off.
func StatePath(dbPath string) string {
	if dbPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(dbPath), stateFileName)
}

// writeStateFile writes the state atomically: a reader landing mid-write would
// otherwise see a truncated file and discard perfectly good state. Same
// temp+rename reasoning as devserver.writeOwner.
func writeStateFile(path string, s persistedState) error {
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp state: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp state: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename state: %w", err)
	}
	return nil
}

// readStateFile loads the state, rejecting anything it cannot vouch for.
func readStateFile(path string) (persistedState, error) {
	var s persistedState
	data, err := os.ReadFile(path) //nolint:gosec // path is daemon-owned, derived from -db
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("parse state %s: %w", path, err)
	}
	if s.Version != stateVersion {
		return persistedState{}, fmt.Errorf("state %s: version %d, want %d", path, s.Version, stateVersion)
	}
	return s, nil
}
