package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/cmuxcheck"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/setup"
)

// surfaceUUIDRe: a cmux surface UUID, strictly (8-4-4-4-12 hex).
var surfaceUUIDRe = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// Seams — tests replace them; the real ones read cmux's sentinel and photo.
var (
	// pilotSentinel: the running cmux's pid.
	pilotSentinel = func() (int, error) { return cmuxcheck.ReadSentinel(cmuxcheck.DefaultSentinel()) }

	// pilotBinding (D4, revue 1 #12): the session and account bound to a
	// surface in cmux's current photo — a --surface marker keeps a session
	// too, so the session check of resolvePilot always applies.
	pilotBinding = func(surface string) (sid, configDir string, err error) {
		ph, err := cmuxcheck.LoadPhoto(filepath.Join(cmuxcheck.CmuxDir(), cmuxcheck.CurrentFile))
		if err != nil {
			return "", "", fmt.Errorf("cmux's current photo: %w", err)
		}
		for _, p := range ph.Panels {
			if strings.EqualFold(p.SurfaceID, surface) && p.Binding != nil && p.Binding.CheckpointID != "" {
				return p.Binding.CheckpointID, p.Binding.ConfigDir(), nil
			}
		}
		return "", "", errors.New("no bound session on that surface in cmux's current photo")
	}

	// pilotScopeOK (revue 1 #12, CLAUDE.md § Isolation criterion): outside
	// the master, the binding's account must belong to the caller's own
	// scope — a cell agent never points pigtaild's typing at another scope's
	// pane.
	pilotScopeOK = func(configDir string) (bool, error) {
		sc, err := callerScope()
		if err != nil {
			return false, err
		}
		if scope.SeesAll(scope.Scope(sc)) {
			return true, nil
		}
		cells, _ := claudeprofile.Load(setup.ConfigDir())
		bsc, ok := cells.ScopeOf(configDir)
		if !ok {
			return false, nil
		}
		b, err := scope.Parse(bsc)
		if err != nil {
			return false, nil //nolint:nilerr // an unknown scope is not this cell's: refused, never named
		}
		c, err := scope.Parse(sc)
		return err == nil && b == c, nil
	}
)

// cmuxPilotCmd (D4): name the session pigtaild repairs alone after the next
// cmux relaunch, and that gets the message (D5). One-shot.
func cmuxPilotCmd() *cobra.Command {
	var surface string
	var show, clear bool
	cmd := &cobra.Command{
		Use:   "pilot",
		Short: "Name the session pigtaild repairs first after the next cmux relaunch (one-shot)",
		Long: `Set before relaunching cmux. After the relaunch, pigtaild repairs this
session alone (the others are reported) and types it a message asking it to
run "pigtail cmux check --last" and present what is left to you. Valid for
the next cmux launch only. Default: the pane this command runs in. Outside
the master cell, a pending marker bound to another scope's session is never
overwritten nor cleared.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out, dir := cmd.OutOrStdout(), cmuxcheck.StateDir()
			if !show { // revue PR B M6: never overwrite nor clear another scope's marker
				if err := guardOtherScopeMarker(dir); err != nil {
					return err
				}
			}
			switch {
			case clear:
				if err := cmuxcheck.ClearPilot(dir); err != nil {
					return err
				}
				fmt.Fprintln(out, "pilot cleared")
				return nil
			case show:
				m, err := cmuxcheck.LoadPilot(dir)
				if errors.Is(err, fs.ErrNotExist) {
					fmt.Fprintln(out, "no pilot set")
					return nil
				}
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "pilot: surface %s, set %s under cmux pid %d%s\n", m.SurfaceID, m.At.Local().Format(time.DateTime), m.CmuxPID, usedLine(m))
				return nil
			}
			sid := ""
			named := surface != ""
			if !named {
				surface, sid = os.Getenv(cmux.EnvSurfaceID), os.Getenv("CLAUDE_CODE_SESSION_ID")
			}
			if surface == "" {
				return errors.New("cmux pilot: not in a cmux pane — name one with --surface <UUID>")
			}
			if !surfaceUUIDRe.MatchString(surface) {
				return fmt.Errorf("cmux pilot: --surface %q is not a cmux surface UUID", surface)
			}
			if named { // revue 1 #12: the session from the binding; the caller's scope only
				bsid, cfg, err := pilotBinding(surface)
				if err != nil {
					return fmt.Errorf("cmux pilot: --surface %s: %w", strings.ToUpper(surface), err)
				}
				ok, err := pilotScopeOK(cfg)
				if err != nil {
					return fmt.Errorf("cmux pilot: %w", err)
				}
				if !ok {
					return fmt.Errorf("cmux pilot: --surface %s: no session of this scope is bound there", strings.ToUpper(surface))
				}
				sid = bsid
			}
			pid, err := pilotSentinel()
			if err != nil {
				return fmt.Errorf("cmux pilot: running cmux unknown: %w", err)
			}
			m := cmuxcheck.PilotMarker{SurfaceID: strings.ToUpper(surface), SessionID: sid, CmuxPID: pid, At: time.Now()}
			if err := cmuxcheck.SavePilot(dir, m); err != nil {
				return err
			}
			fmt.Fprintf(out, "pilot set: surface %s — valid for the next cmux launch\n", m.SurfaceID)
			return nil
		},
	}
	cmd.Flags().StringVar(&surface, "surface", "", "the pilot's cmux surface UUID (default: this pane)")
	cmd.Flags().BoolVar(&show, "show", false, "print the marker")
	cmd.Flags().BoolVar(&clear, "clear", false, "remove the marker")
	return cmd
}

// guardOtherScopeMarker (revue PR B M6, CLAUDE.md § Isolation criterion):
// outside the master, a pending marker (not consumed by a launch) whose
// surface is bound, in cmux's current photo, to another scope's session is
// neither overwritten nor cleared — the --surface rule, applied to the marker
// already there. The refusal names nothing of it. A marker whose surface has
// no binding any more would be ignored at the launch (resolvePilot): free; an
// unreadable one too.
func guardOtherScopeMarker(dir string) error {
	m, err := cmuxcheck.LoadPilot(dir)
	if err != nil || m.UsedBy != 0 {
		return nil //nolint:nilerr // none, unreadable or consumed: nothing to protect
	}
	_, cfg, err := pilotBinding(m.SurfaceID)
	if err != nil {
		return nil //nolint:nilerr // unbound now: resolvePilot would ignore it
	}
	ok, err := pilotScopeOK(cfg)
	if err != nil {
		return fmt.Errorf("cmux pilot: %w", err)
	}
	if !ok {
		return errors.New("cmux pilot: a pending pilot marker of another scope is set — not overwritten nor cleared")
	}
	return nil
}

func usedLine(m cmuxcheck.PilotMarker) string {
	if m.UsedBy == 0 {
		return ", not used yet"
	}
	return fmt.Sprintf(", used by cmux launch %d", m.UsedBy)
}
