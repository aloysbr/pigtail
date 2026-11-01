// Package staffing is the staffing scheduler (sub_x000000000000004): a loop
// in pigtaild that launches, per scope, the best-ranked ready subject while
// the places taken stay under a ceiling, behind every existing gate.
package staffing

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	// DefaultMode is the mode a fresh install runs in (D6): the one place
	// that changes when the scheduler is let loose.
	DefaultMode = pigtail.StaffingAdvise
	// DefaultCeiling is N, the places the scheduler fills at most (D3).
	DefaultCeiling = 3
	// MaxCeiling is the session budget's own ceiling.
	MaxCeiling = 20

	// fallbackMode is what an unreadable settings file reads as: distinct
	// from DefaultMode so an unreadable file never reads as launch.
	fallbackMode = pigtail.StaffingAdvise

	settingsFileName = "staffing.json"
	settingsVersion  = 1
)

// Settings are the scheduler's persisted settings.
type Settings struct {
	Mode      pigtail.StaffingMode
	Ceiling   int
	UpdatedAt time.Time
}

// Validate refuses an unknown mode and a ceiling outside 1..MaxCeiling.
func (s Settings) Validate() error {
	switch s.Mode {
	case pigtail.StaffingOff, pigtail.StaffingAdvise, pigtail.StaffingLaunch:
	default:
		return fmt.Errorf("staffing mode %q: want off, advise or launch", s.Mode)
	}
	if s.Ceiling < 1 || s.Ceiling > MaxCeiling {
		return fmt.Errorf("staffing ceiling %d: want 1 to %d", s.Ceiling, MaxCeiling)
	}
	return nil
}

func defaultSettings() Settings { return Settings{Mode: DefaultMode, Ceiling: DefaultCeiling} }

// settingsFile is the on-disk shape.
type settingsFile struct {
	Version   int                  `json:"version"`
	Mode      pigtail.StaffingMode `json:"mode"`
	Ceiling   int                  `json:"ceiling"`
	UpdatedAt time.Time            `json:"updated_at"`
}

// SettingsPath returns where the settings live for a daemon whose database is
// at dbPath; "" (memory only) when dbPath is empty.
func SettingsPath(dbPath string) string {
	if dbPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(dbPath), settingsFileName)
}

// LoadSettings reads the settings at path. Absent (or no path) is a first
// start: the defaults, no error. Unreadable — a cut JSON, an unknown version,
// an invalid value — is the fallback (advise, DefaultCeiling) and the error.
func LoadSettings(path string) (Settings, error) {
	if path == "" {
		return defaultSettings(), nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaultSettings(), nil
	}
	fallback := Settings{Mode: fallbackMode, Ceiling: DefaultCeiling}
	if err != nil {
		return fallback, fmt.Errorf("read %s: %w", path, err)
	}
	var f settingsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fallback, fmt.Errorf("read %s: %w", path, err)
	}
	if f.Version != settingsVersion {
		return fallback, fmt.Errorf("read %s: format %d, this binary reads %d", path, f.Version, settingsVersion)
	}
	s := Settings{Mode: f.Mode, Ceiling: f.Ceiling, UpdatedAt: f.UpdatedAt}
	if err := s.Validate(); err != nil {
		return fallback, fmt.Errorf("read %s: %w", path, err)
	}
	return s, nil
}

// SaveSettings writes s atomically (unique temp + rename). No path keeps the
// settings in memory only.
func SaveSettings(path string, s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	data, err := json.Marshal(settingsFile{Version: settingsVersion, Mode: s.Mode, Ceiling: s.Ceiling, UpdatedAt: s.UpdatedAt.UTC()})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
