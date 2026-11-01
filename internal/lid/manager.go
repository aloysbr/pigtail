package lid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/powersrc"
	"github.com/aloysbr/pigtail/internal/daemon/thermal"
)

// Off records why and when sleep control was last turned off.
type Off struct {
	At     time.Time `json:"at"`
	Reason string    `json:"reason"` // "deadline" | "battery N%" | "thermal serious" | "thermal critical" | "manual" | "external"
}

// Status is the real sleep state plus what pigtail armed.
type Status struct {
	SleepDisabled bool
	Deadline      time.Time // zero = no pigtail timer
	LastOff       *Off
	LastError     string
	// GuardError lists the guard readings that failed at the last tick, mode
	// on: "battery reading failed: …", "thermal reading failed: …", "lid
	// reading failed: …", joined by "; " ("" = every wired guard could read).
	GuardError string
	// ThermalState is the last thermal reading that succeeded, mode on
	// ("nominal" | "fair" | "serious" | "critical"); "" off or never read.
	ThermalState string
	// LidClosed is the last lid reading, mode on; nil off, never read, or
	// when the last reading failed (lid unknown).
	LidClosed *bool
}

// ProbeResult is the answer of the D7 probe.
type ProbeResult struct {
	RuleOK  bool
	Warning string // "" or ProbeNoTimerWarning
}

// ProbeNoTimerWarning says the Mac is kept awake by something pigtail did not
// arm (the prototype's root watcher, a manual pmset).
const ProbeNoTimerWarning = "sleep is disabled with no timer — run pigtail lid off"

// Config wires a Manager.
type Config struct {
	StatePath string                         // "" = no persistence
	Runner    Runner                         // required
	Battery   func() (powersrc.Level, error) // nil = no battery guard
	// Thermal reads the thermal state; nil = no heat guard (and the lid is
	// not read either).
	Thermal func() (thermal.State, error)
	// LidClosed reads whether the lid is closed (true = closed), read only
	// when Thermal is set. nil with Thermal set = lid unknown at every tick
	// ("lid reading failed: no reader"): a forgotten wiring shows.
	LidClosed func() (bool, error)
	Now       func() time.Time     // nil = time.Now
	Logf      func(string, ...any) // nil = log.Printf
	OnChange  func(Change)         // nil = no notification; called outside the manager lock
}

// Change is one Travel Mode state change, for Config.OnChange.
type Change struct {
	On     bool
	Reason string // "on" | "manual" | "deadline" | "battery N%" | "thermal serious" | "thermal critical" | "external" | "auto-off failed"
}

// StatePath is the state file next to the database; "" for "".
func StatePath(dbPath string) string {
	if dbPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(dbPath), stateFile)
}

const stateVersion = 1

type fileState struct {
	Version  int       `json:"version"`
	On       bool      `json:"on"`
	Deadline time.Time `json:"deadline,omitzero"`
	LastOff  *Off      `json:"last_off,omitempty"`
}

// Manager owns sleep control: On/Off, the deadline, battery and lid-closed
// heat guards (Tick), and the D7 probe. One mutex serialises On, Off, Tick and
// Probe, pmset and sudo calls included (each bounded at 10 s); the battery,
// thermal and lid reads of Tick and the pmset read of Status happen outside
// it. OnChange runs after the lock is released.
type Manager struct {
	mu      sync.Mutex
	c       Config
	st      fileState
	lastErr string

	// Guard state, in memory only (a daemon restart starts it over), reset
	// by clearLocked at every stop.
	seriousStreak int    // consecutive "serious" readings with the lid closed
	guardErr      string // Status.GuardError
	thermalState  string // Status.ThermalState
	lidClosed     *bool  // Status.LidClosed
	lidLogged     string // last lid state logged: "" | "closed" | "open" | "unknown"
}

// New loads the persisted state: absent → blank; unreadable or another
// version → blank plus one log line (a Mac left at 1 then reads "no timer").
func New(c Config) *Manager {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logf == nil {
		c.Logf = log.Printf
	}
	m := &Manager{c: c, st: fileState{Version: stateVersion}}
	if c.StatePath == "" {
		return m
	}
	data, err := os.ReadFile(c.StatePath) //nolint:gosec // path is daemon-owned, derived from -db
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			m.logf("lid: cannot read %s, starting blank: %v", c.StatePath, err)
		}
		return m
	}
	var st fileState
	if err := json.Unmarshal(data, &st); err != nil {
		m.logf("lid: unreadable %s, starting blank: %v", c.StatePath, err)
		return m
	}
	if st.Version != stateVersion {
		m.logf("lid: %s has version %d, want %d, starting blank", c.StatePath, st.Version, stateVersion)
		return m
	}
	m.st = st
	return m
}

func (m *Manager) logf(format string, args ...any) { m.c.Logf(format, args...) }

// On disables sleep for hours (1..MaxHours) and arms the deadline. Calling it
// while already on re-arms the deadline to now+hours (D5).
func (m *Manager) On(hours int) (Status, error) {
	if hours < 1 || hours > MaxHours {
		return Status{}, ErrHours
	}
	var ch *Change
	defer m.notify(&ch) // registered before the lock: runs after the unlock
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.c.Runner.SetSleepDisabled(true); err != nil {
		return Status{}, err
	}
	prev := m.st
	now := m.c.Now()
	m.st.On = true
	m.st.Deadline = now.Add(time.Duration(hours) * time.Hour)
	if err := m.saveLocked(); err != nil {
		// Never "on" without a persisted deadline: undo the write.
		if rerr := m.c.Runner.SetSleepDisabled(false); rerr != nil {
			// Sleep stays disabled: keep the deadline in memory so this
			// daemon's Tick still restores sleep.
			m.logf("lid: rollback after a failed save failed: %v", rerr)
			// GET /lid now shows "on": the state did change.
			ch = &Change{On: true, Reason: "on"}
			return Status{}, fmt.Errorf("save lid state: %w (and restoring sleep failed: %w)", err, rerr)
		}
		m.st = prev
		m.st.On = false
		m.st.Deadline = time.Time{}
		return Status{}, fmt.Errorf("save lid state: %w", err)
	}
	m.lastErr = ""
	ch = &Change{On: true, Reason: "on"}
	return m.statusLocked(true), nil
}

// Off restores normal sleep, always running the sudo write (idempotent: it
// also covers sleep disabled with no pigtail timer).
func (m *Manager) Off() (Status, error) {
	var ch *Change
	defer m.notify(&ch) // registered before the lock: runs after the unlock
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.c.Runner.SetSleepDisabled(false); err != nil {
		return Status{}, err
	}
	ch = m.clearLocked(m.c.Now(), "manual")
	return m.statusLocked(false), nil
}

// Status reads the real state (pmset -g, outside the lock) and adds what
// pigtail armed.
func (m *Manager) Status() (Status, error) {
	out, err := m.c.Runner.ReadPM()
	if err != nil {
		return Status{}, fmt.Errorf("pmset -g: %w", err)
	}
	disabled := ParseSleepDisabled(out)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked(disabled), nil
}

func (m *Manager) statusLocked(disabled bool) Status {
	st := Status{SleepDisabled: disabled, LastError: m.lastErr}
	if m.st.On {
		st.Deadline = m.st.Deadline
		st.GuardError = m.guardErr
		st.ThermalState = m.thermalState
		if m.lidClosed != nil {
			c := *m.lidClosed // a copy, never the internal pointer
			st.LidClosed = &c
		}
	}
	if m.st.LastOff != nil {
		off := *m.st.LastOff
		st.LastOff = &off
	}
	return st
}

// Tick applies the deadline, the battery guard (lid open or closed) and the
// heat guard (lid closed only, D2): reason order deadline > thermal critical
// > battery N% > thermal serious. Off → no exec at all.
func (m *Manager) Tick() {
	m.mu.Lock()
	on := m.st.On
	m.mu.Unlock()
	if !on {
		return // off: no exec at all
	}
	// The battery read (exec, <= 10 s) happens BEFORE the lock: a slow
	// pmset -g batt must never hold m.mu against On/Off/Probe.
	var lv powersrc.Level
	lvErr := errors.New("no battery guard")
	if m.c.Battery != nil {
		lv, lvErr = m.c.Battery()
	}
	// The thermal and lid reads (exec, <= 10 s each) too.
	var th thermal.State
	var thErr, lidErr error
	var closed bool
	if m.c.Thermal != nil {
		th, thErr = m.c.Thermal()
		if m.c.LidClosed != nil {
			closed, lidErr = m.c.LidClosed()
		} else {
			lidErr = errors.New("no reader")
		}
	}
	var ch *Change
	defer m.notify(&ch) // registered before the lock: runs after the unlock
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.st.On { // On/Off may have run while the battery was read
		return
	}
	now := m.c.Now()
	if out, err := m.c.Runner.ReadPM(); err == nil && !ParseSleepDisabled(out) {
		ch = m.clearLocked(now, "external")
		return
	}
	var guard []string
	if m.c.Battery != nil && lvErr != nil {
		m.logf("lid: battery reading failed (deadline still applies): %v", lvErr)
		guard = append(guard, "battery reading failed: "+lvErr.Error())
	}
	critical, seriousDue := false, false
	if m.c.Thermal != nil {
		critical, seriousDue, guard = m.heatLocked(th, thErr, closed, lidErr, guard)
	}
	m.guardErr = strings.Join(guard, "; ")
	reason := ""
	switch {
	case !now.Before(m.st.Deadline):
		reason = "deadline"
	case critical:
		reason = "thermal critical"
	case m.c.Battery != nil && lvErr == nil && lv.OnBattery && lv.Percent >= 0 && lv.Percent <= MinBattery:
		reason = fmt.Sprintf("battery %d%%", lv.Percent)
	case seriousDue:
		reason = "thermal serious"
	}
	if reason == "" {
		return
	}
	if err := m.c.Runner.SetSleepDisabled(false); err != nil {
		msg := fmt.Sprintf("auto-off (%s) failed: %v", reason, err)
		if msg != m.lastErr { // once per distinct error, not at every retrying tick
			ch = &Change{On: true, Reason: "auto-off failed"}
		}
		m.lastErr = msg
		m.logf("lid: %s", m.lastErr)
		return
	}
	m.logf("lid: auto-off (%s)", reason)
	ch = m.clearLocked(now, reason)
}

// heatLocked applies one thermal and lid reading to the guard state (D2·t2)
// and says whether it calls for an off: critical (lid closed, now) or
// seriousDue (lid closed, SeriousTicks consecutive serious readings). The
// order matters: an unknown lid wins (no thermal rule at all, streak kept),
// an open lid resets the streak, a failed thermal reading keeps it. Caller
// holds m.mu.
func (m *Manager) heatLocked(th thermal.State, thErr error, closed bool, lidErr error, guard []string) (critical, seriousDue bool, _ []string) {
	lidState := "unknown"
	if lidErr == nil {
		lidState = "open"
		if closed {
			lidState = "closed"
		}
	}
	if lidState != m.lidLogged { // one line per transition, never per tick
		m.logf("lid: lid %s", lidState)
		m.lidLogged = lidState
	}
	if thErr != nil {
		m.logf("lid: thermal reading failed (no thermal off): %v", thErr)
		guard = append(guard, "thermal reading failed: "+thErr.Error())
	} else {
		m.thermalState = th.String()
	}
	if lidErr != nil {
		m.logf("lid: lid state reading failed (no thermal off): %v", lidErr)
		guard = append(guard, "lid reading failed: "+lidErr.Error())
		m.lidClosed = nil
		return false, false, guard
	}
	m.lidClosed = &closed
	if !closed {
		m.seriousStreak = 0
		return false, false, guard
	}
	if thErr != nil {
		return false, false, guard
	}
	switch th {
	case thermal.Serious:
		m.seriousStreak++
		m.logf("lid: thermal serious, lid closed (%d/%d)", m.seriousStreak, SeriousTicks)
	case thermal.Critical:
		critical = true
	default: // nominal, fair
		m.seriousStreak = 0
	}
	return critical, m.seriousStreak >= SeriousTicks, guard
}

// Probe is the D7 check that the sudoers rule works for this process: under
// the lock, read SleepDisabled and write that same value back through
// `sudo -n -k`. It never calls SetSleepDisabled and changes no state.
func (m *Manager) Probe() (ProbeResult, error) {
	m.mu.Lock() // D7: read and no-op write never interleave with Tick/On/Off
	defer m.mu.Unlock()
	out, err := m.c.Runner.ReadPM()
	if err != nil {
		return ProbeResult{}, fmt.Errorf("pmset -g: %w", err)
	}
	cur := ParseSleepDisabled(out)
	if err := m.c.Runner.ProbeSet(cur); err != nil {
		m.logf("lid: sudo probe: rule not in effect: %v", err)
		return ProbeResult{}, nil
	}
	res := ProbeResult{RuleOK: true}
	if cur && !m.st.On {
		if again, err := m.c.Runner.ReadPM(); err != nil {
			m.logf("lid: re-read after probe failed: %v", err)
		} else if ParseSleepDisabled(again) {
			res.Warning = ProbeNoTimerWarning
		}
	}
	return res, nil
}

// Run ticks once at once (a deadline passed while the daemon was down), then
// on every tick until ctx is done.
func (m *Manager) Run(ctx context.Context, tick <-chan time.Time) {
	m.Tick()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			m.Tick()
		}
	}
}

// clearLocked turns pigtail's timer off and returns the Change to notify.
func (m *Manager) clearLocked(now time.Time, reason string) *Change {
	m.st.On = false
	m.st.Deadline = time.Time{}
	m.st.LastOff = &Off{At: now, Reason: reason}
	m.lastErr = ""
	m.seriousStreak = 0
	m.guardErr = ""
	m.thermalState = ""
	m.lidClosed = nil
	m.lidLogged = ""
	if err := m.saveLocked(); err != nil {
		m.logf("lid: %v", err)
	}
	return &Change{On: false, Reason: reason}
}

// notify calls OnChange with *ch, if any. It is deferred before m.mu.Lock()
// so that it runs after the deferred unlock: a subscriber never holds the lock
// that serialises pmset and sudo.
func (m *Manager) notify(ch **Change) {
	if *ch != nil && m.c.OnChange != nil {
		m.c.OnChange(**ch)
	}
}

func (m *Manager) saveLocked() error {
	if m.c.StatePath == "" {
		return nil
	}
	m.st.Version = stateVersion
	return writeStateFile(m.c.StatePath, m.st)
}

// writeStateFile writes atomically (temp + rename), as rammon does: a reader
// landing mid-write would otherwise see a truncated file.
func writeStateFile(path string, s fileState) error {
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal lid state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp lid state: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp lid state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp lid state: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename lid state: %w", err)
	}
	return nil
}
