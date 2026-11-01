package daemonwatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Event is something the watchdog did, reported to the journal once the
// daemon answers again.
type Event struct {
	Kind   string    `json:"kind"` // "kick" | "gave-up"
	At     time.Time `json:"at"`
	Detail string    `json:"detail"`
}

// State is the watchdog's memory between two runs (one file).
type State struct {
	LastRun    time.Time   `json:"last_run"`
	LastSkip   string      `json:"last_skip,omitempty"`
	Failures   int         `json:"failures"`
	LastWedged time.Time   `json:"last_wedged,omitempty"`
	Kicks      []time.Time `json:"kicks,omitempty"`
	GaveUp     *time.Time  `json:"gave_up,omitempty"`
	Unreported []Event     `json:"unreported,omitempty"`
}

// staleAfter is how old LastRun may be before the LaunchAgent counts as not
// running the watchdog (it runs every minute).
const staleAfter = 3 * time.Minute

// StaleRun reports a state whose last run is missing or older than 3 minutes.
func StaleRun(s State, now time.Time) bool {
	return s.LastRun.IsZero() || now.Sub(s.LastRun) > staleAfter
}

// LoadState reads the state file; a missing file is the zero State.
func LoadState(path string) (State, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, fmt.Errorf("daemon-watchdog state %s: %w", path, err)
	}
	return s, nil
}

// SaveState writes the state atomically (temp file + rename).
func SaveState(path string, s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".daemon-watchdog-*")
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
