package daemon

import (
	"context"
	"log"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
)

// dbBackupInterval is how often the daemon snapshots its own database. Hourly
// is the cadence the loss is measured against: an hour of worktree state, port
// allocations and subject-layer edits is an afternoon's work at worst, and a
// tighter cadence would buy little against a corruption nobody notices for a
// day anyway.
const dbBackupInterval = time.Hour

// runDBBackups keeps a rolling set of database snapshots beside the live file.
//
// The database is the one piece of pigtail whose loss is not recoverable by
// re-running something: it holds worktree state, port allocations and the
// subject layer, and a crash mid-write leaves nothing to restore from. This
// loop is that floor.
//
// A backup failure is logged, never fatal: a daemon that stopped managing
// worktrees because it could not write a snapshot would cause the outage the
// snapshot exists to survive.
func (s *Server) runDBBackups(ctx context.Context) {
	t := time.NewTicker(dbBackupInterval)
	defer t.Stop()

	// The offsite destination (and the fallback exercise's own destination,
	// if any) is declared once, in server.go, before this loop and
	// runOffsiteFallback both start — not here. See that call site's own
	// comment for why the declaration must land before either goroutine's
	// first write.
	s.backupDBOnce()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.backupDBOnce()
		}
	}
}

func (s *Server) backupDBOnce() {
	if s.store == nil {
		return
	}
	// IfStale rather than an unconditional backup: the call at start would
	// otherwise let a restart loop rotate through every generation in minutes.
	done, err := s.store.BackupRotatedIfStale(store.BackupGenerations, dbBackupInterval)
	if err != nil {
		log.Printf("daemon: database backup failed: %v", err)
		return
	}
	if done {
		log.Printf("daemon: database backed up to %s.0 (keeping %d generations)",
			s.store.Path(), store.BackupGenerations)
		// Only after a fresh local generation, and only *after* it: the
		// offsite copy duplicates what the rotation just produced, and a
		// destination that hangs must never be able to delay the backup that
		// still works. run returns immediately; see offsite.go.
		if s.cfg.DBOffsiteDir != "" && s.offsite != nil {
			s.offsite.run(s.store.Path(), s.cfg.DBOffsiteDir)
		}
	}
}
