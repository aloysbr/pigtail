package cmuxcheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/aloysbr/pigtail/internal/maintenance"
)

const (
	PilotFromMarker      = "marker"            // D4: pigtail cmux pilot (lot 3)
	PilotFromMaintenance = "maintenance owner" // D4: second source
)

// PilotInfo is the report's pilot (D3–D5). Printed to the master only:
// Redacted builds a new Report and does not copy it (D18).
type PilotInfo struct {
	SurfaceID      string `json:"surface_id,omitempty"`
	Source         string `json:"source,omitempty"`          // PilotFromMarker | PilotFromMaintenance
	Message        string `json:"message,omitempty"`         // lot 3
	MessagePending bool   `json:"message_pending,omitempty"` // lot 3: the second pass retries it
	MessageTyped   bool   `json:"message_typed,omitempty"`   // lot 3: typed for this launch
}

// resolvePilot names the pilot surface before Classify (T-e): the one-shot
// marker first (lot 3), then the active maintenance's owner — the surface
// whose binding carries that conversation. A source that leads to no single
// surface is a note, never a guess. Reads the photos only, never Live.
func resolvePilot(o Options, before, now Photo, l Launch) (surface, source string, notes []string) {
	if m := o.Marker; m != nil {
		ok, why := m.ValidFor(l)
		switch {
		case !ok:
			notes = append(notes, "pilot marker ignored: "+why)
		case !photosHave(before, now, m.SurfaceID):
			notes = append(notes, "pilot marker names a surface in neither photo: ignored")
		case m.SessionID != "" && !boundTo(before, now, m.SurfaceID, m.SessionID):
			notes = append(notes, "pilot marker session is not the conversation bound to that surface: ignored")
		default:
			return surfaceKey(m.SurfaceID), PilotFromMarker, notes
		}
	}
	if o.MaintenanceOwner != "" {
		switch s, n := surfaceOfSession(before, now, o.MaintenanceOwner); n {
		case 1:
			return s, PilotFromMaintenance, notes
		case 0:
			notes = append(notes, "maintenance owner "+maintenance.ShortID(o.MaintenanceOwner)+" is bound to no surface: no pilot")
		default:
			notes = append(notes, "maintenance owner "+maintenance.ShortID(o.MaintenanceOwner)+" is bound to several surfaces: no pilot")
		}
	}
	return "", "", notes
}

// photosHave / boundTo compare by sameSurface: Photo.Panel compares case-
// sensitively, and a marker set by hand may be lower case.
func photosHave(before, now Photo, surface string) bool {
	for _, ph := range []Photo{before, now} {
		for _, p := range ph.Panels {
			if sameSurface(p.SurfaceID, surface) {
				return true
			}
		}
	}
	return false
}

func boundTo(before, now Photo, surface, sid string) bool {
	for _, ph := range []Photo{before, now} {
		for _, p := range ph.Panels {
			if sameSurface(p.SurfaceID, surface) && p.Binding != nil && p.Binding.CheckpointID == sid {
				return true
			}
		}
	}
	return false
}

// surfaceOfSession: the distinct surfaces whose binding carries sid, in the
// photo before and the current one.
func surfaceOfSession(before, now Photo, sid string) (string, int) {
	seen := map[string]bool{}
	var first string
	for _, ph := range []Photo{before, now} {
		for _, p := range ph.Panels {
			if p.Binding == nil || p.Binding.CheckpointID != sid || seen[surfaceKey(p.SurfaceID)] {
				continue
			}
			if len(seen) == 0 {
				first = p.SurfaceID
			}
			seen[surfaceKey(p.SurfaceID)] = true
		}
	}
	return first, len(seen)
}

const (
	PilotFile   = "pilot.json"
	PilotMaxAge = 2 * time.Hour // a marker set longer before the launch is not this relaunch's
)

// PilotMarker (D4): the session the user names before relaunching cmux (pigtail
// cmux pilot), valid for the next cmux launch only.
type PilotMarker struct {
	SurfaceID string    `json:"surface_id"`
	SessionID string    `json:"session_id,omitempty"` // the caller's, or with --surface the binding's (D4); "" only when unknown
	CmuxPID   int       `json:"cmux_pid"`             // the cmux running when it was set
	At        time.Time `json:"at"`
	UsedBy    int       `json:"used_by,omitempty"` // the cmux launch (pid) that consumed it
}

// ValidFor: set under an earlier cmux, before launch l, at most PilotMaxAge
// before it, and consumed by no other launch.
func (m PilotMarker) ValidFor(l Launch) (bool, string) {
	switch {
	case m.UsedBy != 0 && m.UsedBy != l.PID:
		return false, "consumed by an earlier cmux launch"
	case m.CmuxPID == l.PID:
		return false, "set under the running cmux: valid from its next launch"
	case !m.At.Before(l.Start):
		return false, "set after this cmux launch"
	case l.Start.Sub(m.At) > PilotMaxAge:
		return false, fmt.Sprintf("older than %s before this cmux launch", PilotMaxAge)
	}
	return true, ""
}

// SavePilot writes the marker atomically.
func SavePilot(dir string, m PilotMarker) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, PilotFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, PilotFile))
}

// LoadPilot reads the marker; an error wrapping fs.ErrNotExist when none.
func LoadPilot(dir string) (PilotMarker, error) {
	var m PilotMarker
	b, err := os.ReadFile(filepath.Join(dir, PilotFile))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("%s: %w", PilotFile, err)
	}
	return m, nil
}

// ClearPilot removes the marker; none is no error.
func ClearPilot(dir string) error {
	if err := os.Remove(filepath.Join(dir, PilotFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// MarkPilotUsed records the launch that consumed the marker (D4: one launch).
func MarkPilotUsed(dir string, launchPID int) error {
	m, err := LoadPilot(dir)
	if err != nil {
		return err
	}
	if m.UsedBy == launchPID {
		return nil
	}
	m.UsedBy = launchPID
	return SavePilot(dir, m)
}

// PilotMsgFile (revue PR B I2): the once-per-launch "pilot message typed"
// state, next to pilot.json. Written only by the pass that typed it (Enter
// sent, I1), read by every later pass — never rewritten by an inventory, a
// fatal pass or a pass that resolved no pilot, unlike last.json.
const PilotMsgFile = "pilot-msg.json"

// PilotMsgState: the cmux launch whose pilot got the message, which surface,
// and when.
type PilotMsgState struct {
	LaunchPID int       `json:"launch_pid"`
	SurfaceID string    `json:"surface_id"`
	At        time.Time `json:"at"`
}

// For: the message was typed during launch l — its pid, and not before it
// started (a pid reused by a later launch does not inherit it).
func (s PilotMsgState) For(l Launch) bool {
	return s.LaunchPID != 0 && s.LaunchPID == l.PID && !s.At.Before(l.Start)
}

// SavePilotMsg writes the state atomically.
func SavePilotMsg(dir string, s PilotMsgState) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, PilotMsgFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, PilotMsgFile))
}

// LoadPilotMsg reads the state; an error wrapping fs.ErrNotExist when none.
func LoadPilotMsg(dir string) (PilotMsgState, error) {
	var s PilotMsgState
	b, err := os.ReadFile(filepath.Join(dir, PilotMsgFile))
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("%s: %w", PilotMsgFile, err)
	}
	return s, nil
}
