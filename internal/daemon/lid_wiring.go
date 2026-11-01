package daemon

import (
	"context"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/lid"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// newLidManager builds the lid manager only when a runner is configured:
// pigtaild sets lid.ExecRunner{}, powersrc.ReadLevel, thermal.Read and
// clamshell.Closed on darwin, tests leave them all nil, so no test server
// ever executes pmset, sudo, notifyutil or ioreg. Every state
// change is published as lid.changed on bus (D26); bus nil = no callback.
func newLidManager(cfg Config, bus *events.Bus) *lid.Manager {
	if cfg.LidRunner == nil {
		return nil
	}
	var onChange func(lid.Change)
	if bus != nil {
		onChange = func(c lid.Change) {
			bus.Publish(pigtail.Event{Type: pigtail.EventLidChanged,
				Data: map[string]interface{}{"on": c.On, "reason": c.Reason}})
		}
	}
	return lid.New(lid.Config{
		StatePath: lid.StatePath(cfg.DBPath),
		Runner:    cfg.LidRunner,
		Battery:   cfg.LidBattery, // nil in tests: no real pmset -g batt
		Thermal:   cfg.LidThermal, // nil in tests: no real notifyutil
		LidClosed: cfg.LidClosed,  // nil in tests: no real ioreg
		OnChange:  onChange,
	})
}

func (s *Server) runLid(ctx context.Context) {
	t := time.NewTicker(lid.TickEvery)
	defer t.Stop()
	s.lid.Run(ctx, t.C)
}
