package setup

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/aloysbr/pigtail/internal/cmuxcheck"
	"github.com/aloysbr/pigtail/internal/daemon/cmuxwatch"
	"github.com/aloysbr/pigtail/internal/daemon/orphans"
	"github.com/aloysbr/pigtail/internal/daemon/tmpsweep"
)

const plistLabel = "sh.pigtail.daemon"

// Label returns the launchd job label. Exported so the CLI can print the
// literal command to stop the daemon instead of duplicating the string.
func Label() string { return plistLabel }

var plistTmpl = template.Must(template.New("plist").Funcs(template.FuncMap{
	"deref":  func(b *bool) bool { return *b },
	"derefs": func(s *string) string { return *s },
}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>sh.pigtail.daemon</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{.PigtaildPath}}</string>
    <string>--socket</string>
    <string>{{.ConfigDir}}/pigtail.sock</string>
    <string>--db</string>
    <string>{{.ConfigDir}}/pigtail.db</string>
    <string>--http</string>
    <string>{{if .HTTP}}{{derefs .HTTP}}{{end}}</string>
    <string>--ram-floor-mb</string>
    <string>{{.RAMFloorMB}}</string>
    <string>--ram-grace-seconds</string>
    <string>{{.RAMGraceSeconds}}</string>
    <string>--idle-purge-grace-seconds</string>
    <string>{{.IdlePurgeGraceSeconds}}</string>
    <string>--idle-session-pause-seconds</string>
    <string>{{.IdleSessionPauseSeconds}}</string>
    <string>--max-servers</string>
    <string>{{.MaxServers}}</string>
    <string>--cpu-slots</string>
    <string>{{.CPUSlots}}</string>
{{- if .RAMConfirmWindows}}
    <string>--ram-confirm-windows</string>
    <string>{{.RAMConfirmWindows}}</string>
{{- end}}
{{- if .BatteryGraceSeconds}}
    <string>--battery-grace-seconds</string>
    <string>{{.BatteryGraceSeconds}}</string>
{{- end}}
{{- if .AdmitMaxJobs}}
    <string>--admit-max-jobs</string>
    <string>{{.AdmitMaxJobs}}</string>
{{- end}}
{{- if .AdmitFloorMB}}
    <string>--admit-floor-mb</string>
    <string>{{.AdmitFloorMB}}</string>
{{- end}}
{{- if .AdmitDiskFloorGB}}
    <string>--admit-disk-floor-gb</string>
    <string>{{.AdmitDiskFloorGB}}</string>
{{- end}}
{{- if .DiskAlertGB}}
    <string>--disk-alert-gb</string>
    <string>{{.DiskAlertGB}}</string>
{{- end}}
{{- if .AdmitPressureLevel}}
    <string>--admit-pressure-level</string>
    <string>{{.AdmitPressureLevel}}</string>
{{- end}}
{{- if .AdmitSwapoutKBPerSec}}
    <string>--admit-swapout-kb-per-sec</string>
    <string>{{.AdmitSwapoutKBPerSec}}</string>
{{- end}}
{{- if .AdmitCompressKBPerSec}}
    <string>--admit-compress-kb-per-sec</string>
    <string>{{.AdmitCompressKBPerSec}}</string>
{{- end}}
{{- if .AdmitMaxStarveSeconds}}
    <string>--admit-max-starve-seconds</string>
    <string>{{.AdmitMaxStarveSeconds}}</string>
{{- end}}
{{- if .AdmitCompressCalmSeconds}}
    <string>--admit-compress-calm-seconds</string>
    <string>{{.AdmitCompressCalmSeconds}}</string>
{{- end}}
{{- if .AdmitCompressCalmMaxStarveSeconds}}
    <string>--admit-compress-calm-max-starve-seconds</string>
    <string>{{.AdmitCompressCalmMaxStarveSeconds}}</string>
{{- end}}
{{- if .AdmitMaxHoldSeconds}}
    <string>--admit-max-hold-seconds</string>
    <string>{{.AdmitMaxHoldSeconds}}</string>
{{- end}}
{{- range .AdmitClassLimits}}
    <string>--admit-class-limit</string>
    <string>{{.}}</string>
{{- end}}
{{- if .DBOffsiteDir}}
    <string>--db-offsite-dir</string>
    <string>{{.DBOffsiteDir}}</string>
{{- end}}
{{- if .DBOffsiteFallbackDest}}
    <string>--db-offsite-fallback-dest</string>
    <string>{{.DBOffsiteFallbackDest}}</string>
{{- end}}
{{- if .GateCmd}}
    <string>--gate-cmd</string>
    <string>{{.GateCmd}}</string>
{{- end}}
{{- if .SDKHost}}
    <string>--sdk-host</string>
    <string>{{.SDKHost}}</string>
{{- end}}
{{- if .SDKNode}}
    <string>--sdk-node</string>
    <string>{{.SDKNode}}</string>
{{- end}}
{{- if .SDKLaunch}}
    <string>--sdk-launch</string>
{{- end}}
{{- if .SessionFloor}}
    <string>--session-floor</string>
    <string>{{.SessionFloor}}</string>
{{- end}}
{{- if .SessionCeiling}}
    <string>--session-ceiling</string>
    <string>{{.SessionCeiling}}</string>
{{- end}}
{{- if .SessionWindow}}
    <string>--session-window</string>
    <string>{{.SessionWindow}}</string>
{{- end}}
{{- if .SessionMinGap}}
    <string>--session-min-gap</string>
    <string>{{.SessionMinGap}}</string>
{{- end}}
{{- if .SessionCompressKBPerSec}}
    <string>--session-compress-kb-per-sec</string>
    <string>{{.SessionCompressKBPerSec}}</string>
{{- end}}
{{- if .SessionMinAvailableMB}}
    <string>--session-min-available</string>
    <string>{{.SessionMinAvailableMB}}</string>
{{- end}}
{{- if .CmuxSidebar}}
{{- if deref .CmuxSidebar}}
    <string>--cmux-sidebar</string>
{{- else}}
    <string>--cmux-sidebar=false</string>
{{- end}}
{{- end}}
{{- range .CmuxSidebarWindows}}
    <string>--cmux-sidebar-window</string>
    <string>{{.}}</string>
{{- end}}
{{- if .CmuxClaudeCodeFix}}
{{- if deref .CmuxClaudeCodeFix}}
    <string>--cmux-claude-code-fix</string>
{{- else}}
    <string>--cmux-claude-code-fix=false</string>
{{- end}}
{{- end}}
{{- if .CmuxFolders}}
{{- if deref .CmuxFolders}}
    <string>--cmux-folders</string>
{{- else}}
    <string>--cmux-folders=false</string>
{{- end}}
{{- end}}
{{- if .CmuxFolderSort}}
{{- if deref .CmuxFolderSort}}
    <string>--cmux-folder-sort</string>
{{- else}}
    <string>--cmux-folder-sort=false</string>
{{- end}}
{{- end}}
{{- if .CmuxCheckAfterRelaunch}}
    <string>--cmux-check-after-relaunch</string>
    <string>{{.CmuxCheckAfterRelaunch}}</string>
{{- end}}
{{- if .CmuxCheckCtrlC}}
    <string>--cmux-check-ctrl-c</string>
    <string>{{.CmuxCheckCtrlC}}</string>
{{- end}}
{{- if .OrphanReaper}}
    <string>--orphan-reaper</string>
    <string>{{.OrphanReaper}}</string>
{{- end}}
{{- if .OrphanReapAfter}}
    <string>--orphan-reap-after</string>
    <string>{{.OrphanReapAfter}}</string>
{{- end}}
{{- if .TmpSweep}}
    <string>--tmp-sweep</string>
    <string>{{.TmpSweep}}</string>
{{- end}}
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ThrottleInterval</key>
  <integer>15</integer>
  <key>StandardOutPath</key>
  <string>{{.ConfigDir}}/daemon.log</string>
  <key>StandardErrorPath</key>
  <string>{{.ConfigDir}}/daemon.log</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>{{.Path}}</string>
  </dict>
</dict>
</plist>
`))

// Plist defaults. These are deployment choices, not code defaults: pigtaild's
// own flag defaults stay what cmd/pigtaild/main.go says. They live here
// because the plist is regenerated by `pigtail setup`, and a flag that is not
// in the template is a flag `pigtail setup` erases — which is exactly what
// happened to -idle-purge-grace-seconds and -max-servers, tuned by hand in
// ~/Library/LaunchAgents and silently lost on the next setup (2026-09-08).
const (
	defaultPlistRAMFloorMB            = 6144
	defaultPlistRAMGraceSeconds       = 600
	defaultPlistIdlePurgeGraceSeconds = 3600
	// defaultPlistIdleSessionPauseSeconds: pigtaild stops a Claude session
	// idle this long (0 = off); pigtaild's own flag default is the same.
	defaultPlistIdleSessionPauseSeconds = 3600
	defaultPlistMaxServers              = 8
	// -1 asks pigtaild for the platform CPU-slot budget. 0 would disable the
	// arbiter, so it cannot double as "unset".
	defaultPlistCPUSlots = -1
)

// The session budget `pigtail setup --session-budget` writes (orchestrator,
// tier A technical, 2026-10-22): floor 6, ceiling 20 (64 GB Mac, ~350 MB per
// claude process), window 5 min, min gap 40 s, the compressor threshold of the
// memory admission (16 MiB/s) and its available-memory floor (6 GiB, in MiB as
// pigtaild's -session-min-available takes it). Without the option setup writes
// none of it: the sampler runs from install, the gate (ceiling 0) is off until
// an explicit setup after a day of samples. A test holds the values to
// sessbudget.DefaultConfig.
const (
	SessionBudgetFloor            = 6
	SessionBudgetCeiling          = 20
	SessionBudgetWindow           = 5 * time.Minute
	SessionBudgetMinGap           = 40 * time.Second
	SessionBudgetCompressKBPerSec = 16384
	SessionBudgetMinAvailableMB   = 6144
)

// SessionBudgetParams holds only the session budget fields, set to the values
// above, for WithTuningFrom to overlay.
func SessionBudgetParams() PlistParams {
	return PlistParams{
		SessionFloor: IntPtr(SessionBudgetFloor), SessionCeiling: IntPtr(SessionBudgetCeiling),
		SessionWindow: SessionBudgetWindow, SessionMinGap: SessionBudgetMinGap,
		SessionCompressKBPerSec: IntPtr(SessionBudgetCompressKBPerSec), SessionMinAvailableMB: IntPtr(SessionBudgetMinAvailableMB),
	}
}

// IntPtr is the constructor for PlistParams's tuning fields. They are
// pointers precisely so that "the operator asked for 0" and "the plist never
// mentioned this flag" are two different states; writing a literal through
// this helper is how a caller says the former.
func IntPtr(v int) *int { return &v }

// PlistParams fills the launchd template. Every tuning field is optional and
// a nil pointer means "not set".
//
// Nil — not zero. Three of these flags document 0 as a *meaningful* value:
// -cpu-slots 0 disables the arbiter (one of the two kill switches the design
// names), -max-servers 0 turns the server cap off, and
// -idle-purge-grace-seconds 0 turns idle purging off. When zero doubled as
// "unset", withDefaults below rewrote each of them to a deployment default,
// so an operator who disabled the arbiter by hand in the plist had it
// silently re-enabled by the next `pigtail setup`. Pointers are what keep
// "absent" and "explicitly 0" apart.
//
// nil for RAMFloorMB, RAMGraceSeconds, IdlePurgeGraceSeconds, MaxServers and
// CPUSlots means "the deployment default" documented above.
//
// RAMConfirmWindows, BatteryGraceSeconds and DBOffsiteDir have no such
// deployment default — DBOffsiteDir in particular names a path specific to
// one machine's backup host — so nil/empty OMITS the flag from the plist instead,
// leaving pigtaild's own flag default (cmd/pigtaild/main.go) in effect. That
// is deliberately a different rule from the five fields above: defaulting
// -db-offsite-dir to some value would either break another machine's setup
// or, worse, silently disable a backup path that was never configured in the
// first place. A non-nil pointer to 0 is still emitted for those two, for
// the same reason as above: it is a value the operator chose.
type PlistParams struct {
	PigtaildPath string
	ConfigDir    string
	Path         string

	RAMFloorMB            *int
	RAMGraceSeconds       *int
	IdlePurgeGraceSeconds *int
	// IdleSessionPauseSeconds follows the same rule: nil is the deployment
	// default, an explicit 0 turns the idle pause off. A plist that still
	// carries the old --idle-session-close-seconds is read into it.
	IdleSessionPauseSeconds *int
	MaxServers              *int
	CPUSlots                *int

	RAMConfirmWindows   *int
	BatteryGraceSeconds *int

	// The memory-admission flags follow RAMConfirmWindows' rule: no
	// deployment default, nil OMITS the flag so pigtaild's own flag default
	// applies (cmd/pigtaild/main.go), and a non-nil pointer — 0 included,
	// which disables admission or turns a gate off — is emitted as chosen.
	// They must be here at all for the reason stated on the plist defaults
	// above: a flag the template does not know is a flag `pigtail setup`
	// either erases or refuses to carry.
	AdmitMaxJobs     *int
	AdmitFloorMB     *int
	AdmitDiskFloorGB *int
	// DiskAlertGB is pigtaild's -disk-alert-gb (nil omits the flag; 0 cuts the alert).
	DiskAlertGB           *int
	AdmitPressureLevel    *int
	AdmitSwapoutKBPerSec  *int
	AdmitCompressKBPerSec *int
	AdmitMaxHoldSeconds   *int
	AdmitMaxStarveSeconds *int
	// Compressor calm rule for the heavy classes.
	AdmitCompressCalmSeconds, AdmitCompressCalmMaxStarveSeconds *int
	// AdmitClassLimits are <class>:<n> pairs, one per emitted flag. Like the
	// offsite destinations it has no deployment default: an empty slice
	// omits the flag entirely and lets pigtaild's own default stand.
	AdmitClassLimits []string

	DBOffsiteDir string
	// DBOffsiteFallbackDest follows the exact same rule as DBOffsiteDir
	// above: no deployment default, nil/empty OMITS the flag rather than
	// writing a default value. It names a pinned SSH alias specific to one
	// machine, so the same reasoning applies verbatim.
	DBOffsiteFallbackDest string
	GateCmd               string // pigtaild -gate-cmd; same rule as DBOffsiteDir: no default, empty omits the flag

	// SDK sessions: pigtaild -sdk-host (the clients/sdkhost directory), -sdk-node
	// (the node binary) and -sdk-launch (opens the launch). Machine-specific
	// paths and an opt-in: no deployment default, empty/false omits the flag.
	SDKHost, SDKNode string
	SDKLaunch        bool

	// CmuxSidebar is pigtaild's -cmux-sidebar, a bool. It follows the
	// optional-flag rule with a *bool: nil OMITS the flag (pigtaild's own
	// default, off, applies), true emits a bare --cmux-sidebar, and an
	// explicit false is emitted as --cmux-sidebar=false — a choice the
	// operator made, carried like an explicit 0 above.
	CmuxSidebar *bool
	// CmuxSidebarWindows are the -cmux-sidebar-window UUIDs, one per emitted
	// flag. Mostly a test/debug knob, but carried rather than refused: a plist
	// that names one must not make `pigtail setup` abort, and dropping it
	// would silently widen the loop's writes to every cmux window.
	CmuxSidebarWindows []string
	// CmuxClaudeCodeFix is pigtaild's -cmux-claude-code-fix (temporary
	// claude_code patch, on by default). Same *bool rule as CmuxSidebar: nil
	// omits the flag, an explicit value is carried.
	CmuxClaudeCodeFix *bool
	// CmuxFolders is pigtaild's -cmux-folders (folder arrangement, off by
	// default, acting only while the sidebar loop is on). Same *bool rule as
	// CmuxSidebar.
	CmuxFolders *bool
	// CmuxFolderSort is pigtaild's -cmux-folder-sort (sort of the folder
	// arrangement, on by default). Same *bool rule
	// as CmuxSidebar: nil omits the flag, an explicit value is carried.
	CmuxFolderSort *bool
	// CmuxCheckAfterRelaunch is pigtaild's -cmux-check-after-relaunch
	// (off|report|pilot|fix). "" omits the flag: pigtaild's default (pilot,
	// of) applies; a value set by hand is carried.
	CmuxCheckAfterRelaunch string
	// CmuxCheckCtrlC is pigtaild's -cmux-check-ctrl-c (rescue|unread, revue
	// PR B I4). "" omits the flag: pigtaild's default (rescue) applies;
	// a value set by hand (the switch to unread after the acceptance) is
	// carried.
	CmuxCheckCtrlC string
	// OrphanReaper and OrphanReapAfter are pigtaild's -orphan-reaper
	// (on|observe|off) and -orphan-reap-after. ""/0 omit the flag: pigtaild's
	// defaults (on, 15m) apply; a value set by hand (the reaper turned off)
	// is carried.
	OrphanReaper    string
	OrphanReapAfter time.Duration
	// TmpSweep is pigtaild's -tmp-sweep (on|observe|off). "" omits the flag:
	// pigtaild's default (on) applies; a value set by hand is carried.
	TmpSweep string

	// The session budget flags (pigtaild -session-*, SessionBudgetParams) have
	// no deployment default: nil/zero OMITS the flag so pigtaild's own default
	// (gate off) applies, and a set value — a ceiling of 0 included, which
	// turns the gate off — is emitted and carried forward like any hand tuning.
	SessionFloor            *int
	SessionCeiling          *int
	SessionWindow           time.Duration
	SessionMinGap           time.Duration
	SessionCompressKBPerSec *int
	SessionMinAvailableMB   *int

	// HTTP is pigtaild's --http listen address. nil keeps the historical
	// template value, an empty string (API off). A non-nil value — empty
	// included — is carried from an existing plist like any hand tuning,
	// but only ever a loopback address (see checkLoopbackHTTP).
	HTTP *string
}

// withDefaults fills the unset (nil) tuning fields.
func (p PlistParams) withDefaults() PlistParams {
	if p.RAMFloorMB == nil {
		p.RAMFloorMB = IntPtr(defaultPlistRAMFloorMB)
	}
	if p.RAMGraceSeconds == nil {
		p.RAMGraceSeconds = IntPtr(defaultPlistRAMGraceSeconds)
	}
	if p.IdlePurgeGraceSeconds == nil {
		p.IdlePurgeGraceSeconds = IntPtr(defaultPlistIdlePurgeGraceSeconds)
	}
	if p.IdleSessionPauseSeconds == nil {
		p.IdleSessionPauseSeconds = IntPtr(defaultPlistIdleSessionPauseSeconds)
	}
	if p.MaxServers == nil {
		p.MaxServers = IntPtr(defaultPlistMaxServers)
	}
	if p.CPUSlots == nil {
		p.CPUSlots = IntPtr(defaultPlistCPUSlots)
	}
	return p
}

func GeneratePlist(p PlistParams) (string, error) {
	if p.HTTP != nil {
		if err := checkLoopbackHTTP(*p.HTTP); err != nil {
			return "", err
		}
	}
	var buf bytes.Buffer
	if err := plistTmpl.Execute(&buf, p.withDefaults()); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// WithTuningFrom overlays t's tuning fields onto p, leaving PigtaildPath,
// ConfigDir and Path untouched — those three are recomputed fresh on every
// setup (the current binary location, the current config dir, the current
// PATH), never carried forward from an old plist.
//
// The overlay is field-by-field conditional: a flag the old plist did not
// carry (nil) leaves p's own value alone rather than blanking it. With the
// int fields this could only be written as an unconditional assignment,
// because there was no way to tell "absent" from "zero"; pointers make the
// intended rule expressible, so it is stated here rather than deferred.
func (p PlistParams) WithTuningFrom(t PlistParams) PlistParams {
	if t.RAMFloorMB != nil {
		p.RAMFloorMB = t.RAMFloorMB
	}
	if t.RAMGraceSeconds != nil {
		p.RAMGraceSeconds = t.RAMGraceSeconds
	}
	if t.IdlePurgeGraceSeconds != nil {
		p.IdlePurgeGraceSeconds = t.IdlePurgeGraceSeconds
	}
	if t.IdleSessionPauseSeconds != nil {
		p.IdleSessionPauseSeconds = t.IdleSessionPauseSeconds
	}
	if t.MaxServers != nil {
		p.MaxServers = t.MaxServers
	}
	if t.CPUSlots != nil {
		p.CPUSlots = t.CPUSlots
	}
	for _, f := range []struct{ dst, src **int }{
		{&p.SessionFloor, &t.SessionFloor},
		{&p.SessionCeiling, &t.SessionCeiling},
		{&p.SessionCompressKBPerSec, &t.SessionCompressKBPerSec},
		{&p.SessionMinAvailableMB, &t.SessionMinAvailableMB},
	} {
		if *f.src != nil {
			*f.dst = *f.src
		}
	}
	if t.SessionWindow != 0 {
		p.SessionWindow = t.SessionWindow
	}
	if t.SessionMinGap != 0 {
		p.SessionMinGap = t.SessionMinGap
	}
	if t.RAMConfirmWindows != nil {
		p.RAMConfirmWindows = t.RAMConfirmWindows
	}
	if t.BatteryGraceSeconds != nil {
		p.BatteryGraceSeconds = t.BatteryGraceSeconds
	}
	for _, f := range []struct{ dst, src **int }{
		{&p.AdmitMaxJobs, &t.AdmitMaxJobs},
		{&p.AdmitFloorMB, &t.AdmitFloorMB},
		{&p.AdmitDiskFloorGB, &t.AdmitDiskFloorGB},
		{&p.DiskAlertGB, &t.DiskAlertGB},
		{&p.AdmitPressureLevel, &t.AdmitPressureLevel},
		{&p.AdmitSwapoutKBPerSec, &t.AdmitSwapoutKBPerSec},
		{&p.AdmitCompressKBPerSec, &t.AdmitCompressKBPerSec},
		{&p.AdmitMaxHoldSeconds, &t.AdmitMaxHoldSeconds},
		{&p.AdmitMaxStarveSeconds, &t.AdmitMaxStarveSeconds},
		{&p.AdmitCompressCalmSeconds, &t.AdmitCompressCalmSeconds},
		{&p.AdmitCompressCalmMaxStarveSeconds, &t.AdmitCompressCalmMaxStarveSeconds},
	} {
		if *f.src != nil {
			*f.dst = *f.src
		}
	}
	if len(t.AdmitClassLimits) > 0 {
		p.AdmitClassLimits = t.AdmitClassLimits
	}
	if t.DBOffsiteDir != "" {
		p.DBOffsiteDir = t.DBOffsiteDir
	}
	if t.DBOffsiteFallbackDest != "" {
		p.DBOffsiteFallbackDest = t.DBOffsiteFallbackDest
	}
	if t.GateCmd != "" {
		p.GateCmd = t.GateCmd
	}
	if t.SDKHost != "" {
		p.SDKHost = t.SDKHost
	}
	if t.SDKNode != "" {
		p.SDKNode = t.SDKNode
	}
	if t.SDKLaunch {
		p.SDKLaunch = true
	}
	if t.CmuxSidebar != nil {
		p.CmuxSidebar = t.CmuxSidebar
	}
	if t.HTTP != nil {
		p.HTTP = t.HTTP
	}
	if t.CmuxClaudeCodeFix != nil {
		p.CmuxClaudeCodeFix = t.CmuxClaudeCodeFix
	}
	if t.CmuxFolders != nil {
		p.CmuxFolders = t.CmuxFolders
	}
	if t.CmuxFolderSort != nil {
		p.CmuxFolderSort = t.CmuxFolderSort
	}
	if len(t.CmuxSidebarWindows) > 0 {
		p.CmuxSidebarWindows = t.CmuxSidebarWindows
	}
	if t.CmuxCheckAfterRelaunch != "" {
		p.CmuxCheckAfterRelaunch = t.CmuxCheckAfterRelaunch
	}
	if t.CmuxCheckCtrlC != "" {
		p.CmuxCheckCtrlC = t.CmuxCheckCtrlC
	}
	if t.OrphanReaper != "" {
		p.OrphanReaper = t.OrphanReaper
	}
	if t.OrphanReapAfter > 0 {
		p.OrphanReapAfter = t.OrphanReapAfter
	}
	if t.TmpSweep != "" {
		p.TmpSweep = t.TmpSweep
	}
	return p
}

// tuningFlags are exactly the flags the plist template can emit for
// PlistParams's tuning fields — the ones ReadPlistTuning knows how to carry
// forward from an existing plist.
var tuningFlags = map[string]bool{
	"--ram-floor-mb":                           true,
	"--ram-grace-seconds":                      true,
	"--idle-purge-grace-seconds":               true,
	"--idle-session-pause-seconds":             true,
	"--idle-session-close-seconds":             true, // the old name, read as an alias (T-b)
	"--max-servers":                            true,
	"--cpu-slots":                              true,
	"--ram-confirm-windows":                    true,
	"--battery-grace-seconds":                  true,
	"--admit-max-jobs":                         true,
	"--admit-floor-mb":                         true,
	"--admit-disk-floor-gb":                    true,
	"--disk-alert-gb":                          true,
	"--admit-pressure-level":                   true,
	"--admit-swapout-kb-per-sec":               true,
	"--admit-compress-kb-per-sec":              true,
	"--admit-max-hold-seconds":                 true,
	"--admit-max-starve-seconds":               true,
	"--admit-compress-calm-seconds":            true,
	"--admit-compress-calm-max-starve-seconds": true,
	"--admit-class-limit":                      true,
	"--db-offsite-dir":                         true,
	"--db-offsite-fallback-dest":               true,
	"--gate-cmd":                               true,
	"--sdk-host":                               true,
	"--sdk-node":                               true,
	"--session-floor":                          true,
	"--session-ceiling":                        true,
	"--session-window":                         true,
	"--session-min-gap":                        true,
	"--session-compress-kb-per-sec":            true,
	"--session-min-available":                  true,
	"--cmux-sidebar":                           true,
	"--cmux-sidebar-window":                    true,
	"--cmux-claude-code-fix":                   true,
	"--cmux-folders":                           true,
	"--cmux-folder-sort":                       true,
	"--cmux-check-after-relaunch":              true,
	"--cmux-check-ctrl-c":                      true,
	"--orphan-reaper":                          true,
	"--orphan-reap-after":                      true,
	"--tmp-sweep":                              true,
	"--http":                                   true,
}

// checkLoopbackHTTP refuses any --http value that would make pigtaild listen
// beyond this machine. The HTTP API has no authentication: it must only ever
// be reachable on loopback. Empty is allowed — it switches the API off.
// Carrying a hand-edited value forward must never be the way the daemon ends
// up on the whole network, so a non-loopback value aborts setup instead of
// being written.
func checkLoopbackHTTP(addr string) error {
	if addr == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err == nil && (host == "127.0.0.1" || host == "localhost" || host == "::1") {
		return nil
	}
	return fmt.Errorf("--http %q is not a loopback host:port (127.0.0.1, localhost or [::1]) — refusing to make pigtaild listen beyond this machine", addr)
}

// boolFlags are the tuning flags pigtaild declares as booleans. Go's flag
// package never takes a bool's value from the next argument: "-x" alone
// means true and only "-x=false" turns it off. Reading the next token as its
// value would swallow the following flag (the live plist has --cmux-sidebar
// right before -db-offsite-fallback-dest=…).
var boolFlags = map[string]bool{
	"--cmux-sidebar":         true,
	"--cmux-claude-code-fix": true,
	"--cmux-folders":         true,
	"--cmux-folder-sort":     true,
	"--sdk-launch":           true,
}

// nonTuningFlags are flags the template also emits but that are recomputed
// fresh every setup rather than carried forward (the socket/db paths live
// under the *current* ConfigDir). Seeing one of these while reading back an
// existing plist is expected, not a sign of an unrecognised flag. --http used
// to be here and was thrown away on read, so every setup switched the HTTP
// API off; it is now carried (tuningFlags), loopback only.
var nonTuningFlags = map[string]bool{
	"--socket": true,
	"--db":     true,
}

// ProgramArguments returns the ProgramArguments of a launchd plist's raw XML,
// the binary first: what launchd would start (pigtaild -check-args takes the
// rest, TestCheckArgsAcceptsEverySetupFlag).
func ProgramArguments(data []byte) ([]string, error) { return parseProgramArguments(data) }

// parseProgramArguments extracts the ProgramArguments string array from a
// launchd plist's raw XML, without pulling in a full plist library — the
// only shape this ever needs to understand is "one array of strings under
// one known key".
func parseProgramArguments(data []byte) ([]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var lastKey string
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "key":
			var key string
			if err := dec.DecodeElement(&key, &start); err != nil {
				return nil, err
			}
			lastKey = key
		case "array":
			if lastKey != "ProgramArguments" {
				if err := dec.Skip(); err != nil {
					return nil, err
				}
				continue
			}
			var arr struct {
				Strings []string `xml:"string"`
			}
			if err := dec.DecodeElement(&arr, &start); err != nil {
				return nil, err
			}
			return arr.Strings, nil
		}
	}
	return nil, fmt.Errorf("no ProgramArguments array found")
}

// tuningFromArgs walks a plist's ProgramArguments — the pigtaild binary path
// followed by its flags — and picks out the tuning values PlistParams knows
// about.
//
// It accepts every spelling Go's flag package accepts, because pigtaild parses
// with it and both the spec and the README spell these flags with a SINGLE
// dash: "-cpu-slots 9", "--cpu-slots 9" and "--cpu-slots=9" are the same flag.
// Requiring the double-dash pair form made `pigtail setup` abort outright on a
// plist hand-edited exactly the way the documentation shows — before the zsh
// completions and the daemon check, with no --force to get past it.
//
// Boolean flags (boolFlags) follow Go's bool rule instead: bare means true,
// "=value" is parsed with strconv.ParseBool, and the next argument is never
// consumed as their value.
//
// It still fails on anything it cannot account for: a flag with no value, a
// bare non-flag token, or a flag neither in tuningFlags nor nonTuningFlags.
// Silently dropping an unrecognised flag would be exactly the regression this
// whole mechanism exists to close, so an existing plist this function does not
// fully understand is refused rather than partially carried forward — and the
// refusal says what to do about it (see ReadPlistTuning).
func tuningFromArgs(args []string) (PlistParams, error) {
	var p PlistParams
	if len(args) == 0 {
		return p, fmt.Errorf("ProgramArguments is empty (expected at least the pigtaild path)")
	}
	rest := args[1:] // args[0] is the pigtaild binary path, not a flag
	for i := 0; i < len(rest); i++ {
		tok := rest[i]
		if !strings.HasPrefix(tok, "-") {
			return PlistParams{}, fmt.Errorf("expected a flag at position %d, found %q", i+1, tok)
		}
		// Go's flag package treats "-x" and "--x" as the same flag, and
		// accepts the value either attached with "=" or as the next argument.
		name := strings.TrimPrefix(strings.TrimPrefix(tok, "-"), "-")
		value, attached := "", false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name, value, attached = name[:eq], name[eq+1:], true
		}
		if name == "" {
			return PlistParams{}, fmt.Errorf("expected a flag at position %d, found %q", i+1, tok)
		}
		flag := "--" + name
		if boolFlags[flag] {
			on := true
			if attached {
				b, err := strconv.ParseBool(value)
				if err != nil {
					return PlistParams{}, fmt.Errorf("flag %q has non-boolean value %q: %w", flag, value, err)
				}
				on = b
			}
			switch flag {
			case "--cmux-sidebar":
				p.CmuxSidebar = &on
			case "--cmux-claude-code-fix":
				p.CmuxClaudeCodeFix = &on
			case "--cmux-folders":
				p.CmuxFolders = &on
			case "--cmux-folder-sort":
				p.CmuxFolderSort = &on
			case "--sdk-launch":
				p.SDKLaunch = on
			}
			continue
		}
		if !attached {
			if i+1 >= len(rest) {
				return PlistParams{}, fmt.Errorf("flag %q has no value", flag)
			}
			i++
			value = rest[i]
		}
		if nonTuningFlags[flag] {
			continue
		}
		if !tuningFlags[flag] {
			return PlistParams{}, fmt.Errorf("unrecognised flag %q — refusing to regenerate a plist it does not fully understand", flag)
		}
		if flag == "--db-offsite-dir" {
			p.DBOffsiteDir = value
			continue
		}
		if flag == "--admit-class-limit" {
			p.AdmitClassLimits = append(p.AdmitClassLimits, value)
			continue
		}
		if flag == "--db-offsite-fallback-dest" {
			p.DBOffsiteFallbackDest = value
			continue
		}
		if flag == "--gate-cmd" {
			p.GateCmd = value
			continue
		}
		if flag == "--sdk-host" {
			p.SDKHost = value
			continue
		}
		if flag == "--sdk-node" {
			p.SDKNode = value
			continue
		}
		if flag == "--http" {
			if err := checkLoopbackHTTP(value); err != nil {
				return PlistParams{}, err
			}
			v := value
			p.HTTP = &v
			continue
		}
		if flag == "--session-window" || flag == "--session-min-gap" {
			// pigtaild's flag.Duration: refuse here what it would refuse at start.
			d, err := time.ParseDuration(value)
			if err != nil || d <= 0 {
				return PlistParams{}, fmt.Errorf("flag %q has a value %q that is not a positive duration", flag, value)
			}
			if flag == "--session-window" {
				p.SessionWindow = d
			} else {
				p.SessionMinGap = d
			}
			continue
		}
		if flag == "--cmux-check-after-relaunch" {
			// pigtaild refuses to start on any other value: refuse it here
			// rather than install a daemon that will not start.
			m, err := cmuxwatch.ParseMode(value)
			if err != nil {
				return PlistParams{}, err
			}
			p.CmuxCheckAfterRelaunch = m
			continue
		}
		if flag == "--cmux-check-ctrl-c" {
			// Same rule: pigtaild refuses to start on any other value.
			q, err := cmuxcheck.ParseQuitPolicy(value)
			if err != nil {
				return PlistParams{}, err
			}
			p.CmuxCheckCtrlC = string(q)
			continue
		}
		if flag == "--orphan-reaper" {
			// pigtaild refuses to start on any other value.
			m, err := orphans.ParseMode(value)
			if err != nil {
				return PlistParams{}, err
			}
			p.OrphanReaper = string(m)
			continue
		}
		if flag == "--tmp-sweep" {
			// pigtaild refuses to start on any other value.
			m, err := tmpsweep.ParseMode(value)
			if err != nil {
				return PlistParams{}, err
			}
			p.TmpSweep = string(m)
			continue
		}
		if flag == "--orphan-reap-after" {
			d, err := time.ParseDuration(value)
			if err != nil || d <= 0 {
				return PlistParams{}, fmt.Errorf("flag %q has a value %q that is not a positive duration", flag, value)
			}
			p.OrphanReapAfter = d
			continue
		}
		if flag == "--cmux-sidebar-window" {
			p.CmuxSidebarWindows = append(p.CmuxSidebarWindows, value)
			continue
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			return PlistParams{}, fmt.Errorf("flag %q has non-integer value %q: %w", flag, value, err)
		}
		switch flag {
		case "--ram-floor-mb":
			p.RAMFloorMB = IntPtr(n)
		case "--ram-grace-seconds":
			p.RAMGraceSeconds = IntPtr(n)
		case "--idle-purge-grace-seconds":
			p.IdlePurgeGraceSeconds = IntPtr(n)
		case "--idle-session-pause-seconds", "--idle-session-close-seconds":
			p.IdleSessionPauseSeconds = IntPtr(n)
		case "--max-servers":
			p.MaxServers = IntPtr(n)
		case "--cpu-slots":
			p.CPUSlots = IntPtr(n)
		case "--ram-confirm-windows":
			p.RAMConfirmWindows = IntPtr(n)
		case "--battery-grace-seconds":
			p.BatteryGraceSeconds = IntPtr(n)
		case "--admit-max-jobs":
			p.AdmitMaxJobs = IntPtr(n)
		case "--admit-floor-mb":
			p.AdmitFloorMB = IntPtr(n)
		case "--admit-disk-floor-gb":
			p.AdmitDiskFloorGB = IntPtr(n)
		case "--disk-alert-gb":
			p.DiskAlertGB = IntPtr(n)
		case "--admit-pressure-level":
			p.AdmitPressureLevel = IntPtr(n)
		case "--admit-swapout-kb-per-sec":
			p.AdmitSwapoutKBPerSec = IntPtr(n)
		case "--admit-compress-kb-per-sec":
			p.AdmitCompressKBPerSec = IntPtr(n)
		case "--admit-max-hold-seconds":
			p.AdmitMaxHoldSeconds = IntPtr(n)
		case "--admit-max-starve-seconds":
			p.AdmitMaxStarveSeconds = IntPtr(n)
		case "--admit-compress-calm-seconds":
			p.AdmitCompressCalmSeconds = IntPtr(n)
		case "--admit-compress-calm-max-starve-seconds":
			p.AdmitCompressCalmMaxStarveSeconds = IntPtr(n)
		case "--session-floor":
			p.SessionFloor = IntPtr(n)
		case "--session-ceiling":
			p.SessionCeiling = IntPtr(n)
		case "--session-compress-kb-per-sec":
			p.SessionCompressKBPerSec = IntPtr(n)
		case "--session-min-available":
			p.SessionMinAvailableMB = IntPtr(n)
		}
	}
	return p, nil
}

// ReadPlistTuning reads the plist at path and extracts the tuning flags
// PlistParams knows how to carry forward. A path that does not exist yet
// (the common case: first-ever `pigtail setup`) is not an error — it
// returns a zero PlistParams, so GeneratePlist's own defaults apply.
//
// Anything else that goes wrong — the file cannot be read, its XML is
// malformed, its ProgramArguments array is truncated, or it names a flag
// this code does not recognise — IS an error, and ReadPlistTuning returns
// only that error, never a partially-filled PlistParams. Losing a
// hand-tuned setting to a silent parse bug would be worse than the
// regression this function exists to close, so a plist it cannot fully
// understand is refused rather than partially trusted.
func ReadPlistTuning(path string) (PlistParams, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return PlistParams{}, nil
		}
		return PlistParams{}, fmt.Errorf("read existing plist %s: %w", path, err)
	}
	args, err := parseProgramArguments(data)
	if err != nil {
		return PlistParams{}, fmt.Errorf("parse existing plist %s: %w", path, err)
	}
	tuning, err := tuningFromArgs(args)
	if err != nil {
		return PlistParams{}, fmt.Errorf(
			"plist %s: %w\n  fix: remove that flag from %s, or delete the file and re-run `pigtail setup` to regenerate it from defaults",
			path, err, path)
	}
	return tuning, nil
}

// ValidateSDK refuses the SDK flag combinations pigtaild refuses at start
// (cmd/pigtaild sdkConfig): -sdk-host and -sdk-node go together, and
// -sdk-launch needs both. A plist that fails here would crash-loop the daemon.
func (p PlistParams) ValidateSDK() error {
	switch {
	case p.SDKHost == "" && p.SDKNode == "":
		if p.SDKLaunch {
			return errors.New("--sdk-launch needs --sdk-host and --sdk-node (nothing written)")
		}
	case p.SDKHost == "":
		return errors.New("--sdk-node needs --sdk-host (nothing written)")
	case p.SDKNode == "":
		return errors.New("--sdk-host needs --sdk-node (nothing written)")
	}
	return nil
}

// PreserveTuning reads any plist already at existingPath and overlays its
// tuning flags onto params, so a re-run of `pigtail setup` carries forward
// values an operator tuned by hand (RAM/battery/eviction thresholds, the
// off-site backup path, …) instead of resetting them to the deployment
// defaults. When existingPath does not exist yet, params is returned
// unchanged. When it exists but cannot be fully understood, params is
// returned zeroed alongside the error — callers MUST check the error and
// not install the result.
func PreserveTuning(existingPath string, params PlistParams) (PlistParams, error) {
	tuning, err := ReadPlistTuning(existingPath)
	if err != nil {
		return PlistParams{}, err
	}
	return params.WithTuningFrom(tuning), nil
}

func PlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", plistLabel+".plist")
}

func InstallPlist(p PlistParams) error {
	content, err := GeneratePlist(p)
	if err != nil {
		return fmt.Errorf("generate plist: %w", err)
	}
	dest := PlistPath()
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	return os.WriteFile(dest, []byte(content), 0o644)
}

func LoadLaunchd() error {
	domain, err := guiDomain()
	if err != nil {
		return err
	}
	return loadJob(context.Background(), ExecRunner, domain, plistLabel, PlistPath())
}

func UnloadLaunchd() error {
	domain, err := guiDomain()
	if err != nil {
		return err
	}
	return unloadJob(context.Background(), ExecRunner, domain, plistLabel)
}

func RemovePlist() error {
	return os.Remove(PlistPath())
}
