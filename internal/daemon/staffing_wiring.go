package daemon

import (
	"context"
	"fmt"
	"log"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/daemon/launch"
	"github.com/aloysbr/pigtail/internal/daemon/sessbudget"
	"github.com/aloysbr/pigtail/internal/daemon/staffing"
	"github.com/aloysbr/pigtail/internal/peers"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// wireStaffing builds the staffing scheduler (sub_x000000000000004) on the
// daemon's services and sets svc.Staffing and svc.SubjectsChanged. Called in
// buildRouter after wireLauncher, before the subject routes; nothing without
// a store or a launcher. Only the scheduler asks the session budget for a
// place: the launcher itself has none. Config.StaffingOverrides replaces
// seams in tests.
func (s *Server) wireStaffing(svc *handlers.Services) {
	if s.store == nil || s.launcher == nil {
		return
	}
	b := s.sessionBudget
	d := staffing.Deps{
		Store: s.store,
		// The Staffing mark makes the launcher pick by the weekly budget; every
		// other launch stays off that path (D6).
		Launch: func(ctx context.Context, sub pigtail.Subject) (pigtail.SessionLaunch, error) {
			return s.launcher.Launch(launch.WithStaffing(ctx), sub)
		},
		Launches: s.launcher.List,
		Burst:    s.launcher.Burst,
		Budget: staffing.Budget{
			Check: func() (bool, bool, string) {
				a := b.Check()
				return a.Enabled, a.Admitted, a.Reason
			},
			Admit: func(sc string) (string, bool, string) {
				a := b.TryAdmitAs(sessbudget.KindAgent, sc)
				return a.LeaseID, a.Admitted, a.Reason
			},
			Release:      b.ReleaseLease,
			MarkLaunched: b.MarkLaunched,
		},
		Machine:     s.staffingMachine,
		Maintenance: staffingMaintenance(svc),
		Margin:      handlers.StaffingMargin(svc),
		TokenBudget: handlers.StaffingTokenBudget(svc),
		Scopes: func() []string {
			names := scope.Active().Names()
			out := make([]string, len(names))
			for i, n := range names {
				out[i] = string(n)
			}
			return out
		},
		Alive:        peers.Alive,
		Native:       claudesession.ReadNative,
		Publish:      func(e pigtail.Event) { s.bus.Publish(e) },
		SettingsPath: staffing.SettingsPath(s.cfg.DBPath),
		LogPath:      staffing.LogPath(s.cfg.DBPath),
		Logf:         log.Printf,
	}
	if b == nil {
		d.Budget = staffing.Budget{} // no controller: the gate reads off (closed)
	}
	if s.cfg.StaffingOverrides != nil {
		s.cfg.StaffingOverrides(&d)
	}
	s.staffing = staffing.New(d)
	svc.Staffing = s.staffing
	svc.SubjectsChanged = s.staffing.Kick
}

// staffingMachine is the machine gate: a test run waiting for a CPU slot, or
// a heavy command queued for memory, holds the scheduler back. A nil or
// switched-off controller reads as free.
func (s *Server) staffingMachine() (bool, string) {
	if s.cpuSlots != nil {
		if n := s.cpuSlots.Status().Waiting; n > 0 {
			return false, fmt.Sprintf("cpu: %d test run(s) waiting for a slot", n)
		}
	}
	if s.admission != nil {
		if n := len(s.admission.Status().Queue); n > 0 {
			return false, fmt.Sprintf("memory admission: %d command(s) queued", n)
		}
	}
	return true, ""
}

// staffingMaintenance is the maintenance gate on the state watch: an
// unreadable state is an error (closed), no file is off, a state file is
// its own On. No watch closes the gate.
func staffingMaintenance(svc *handlers.Services) func() (bool, string, error) {
	return func() (bool, string, error) {
		if svc.Maintenance == nil {
			return true, "maintenance state not wired", nil
		}
		st, ok, err := svc.Maintenance.Current()
		if err != nil {
			return true, "", err
		}
		if !ok {
			return false, "", nil
		}
		return st.On, st.Reason, nil
	}
}

// startStaffing runs the scheduler's loop and the relay that kicks a tour on
// each wait, launch or pause event; spawn is Run's (joinable) goroutine
// starter. The end of a session has no event: the loop's tick covers it.
func (s *Server) startStaffing(ctx context.Context, spawn func(func())) {
	if s.staffing == nil {
		return
	}
	sch := s.staffing
	spawn(func() { sch.Run(ctx) })
	relay := s.watchEvents(sch.Kick, pigtail.EventSessionWaitChanged, pigtail.EventSessionLaunchChanged,
		pigtail.EventWaitOpened, pigtail.EventWaitAnswered, pigtail.EventWaitClosed, pigtail.EventSessionPauseChanged)
	spawn(func() { relay(ctx) })
}
