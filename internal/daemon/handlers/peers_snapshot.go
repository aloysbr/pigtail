package handlers

import (
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// RecordSnapshot upserts a session snapshot the daemon took itself (the cmux
// sidebar loop's `cmux sessions list`, every 30 s), enriched like POST /peers.
// It keeps the registry live between hooks (spec §3.2).
//
// cmux lists sessions long after they ended: a row marked ended (SessionEnd)
// is upserted again only if its process runs and still names it — a resumed
// session. Otherwise the upsert, which clears ended_at, would turn a clean
// exit into a "dead" session.
func (rl *Relay) RecordSnapshot(ps []pigtail.PeerSession) error {
	workspaces, _ := rl.svc.Store.ListWorkspaces("")
	now := rl.cfg.Now()
	for _, p := range ps {
		if p.SessionID == "" {
			continue
		}
		prior := rl.priorOf(p.SessionID)
		p.Source = "snapshot"
		alive := rl.provenAlive(&p, prior)
		if prior.row != nil && prior.row.EndedAt != nil && !alive {
			continue
		}
		rl.enrich(&p, workspaces, alive, prior)
		p.UpdatedAt = now
		if err := rl.svc.Store.UpsertPeer(&p); err != nil {
			return err
		}
		rl.restoreEndedWaits(p.SessionID, p.Source)
	}
	return nil
}
