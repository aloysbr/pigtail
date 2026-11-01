package daemon

import (
	"fmt"
	"syscall"
	"time"

	"github.com/aloysbr/pigtail/internal/bgwork"
	"github.com/aloysbr/pigtail/internal/claudesession"
)

// The idle pause's real seams (handlers.WakeDeps, relay_pause.go).

// pauseBackground is bgwork.Work with the real readings of the session's
// config dir and the screen the pause already read. pigtaild descends from
// no claude: no ancestor to exclude from the Bash-tool shells.
func pauseBackground(cfg, session string, pid int, screen string) ([]string, error) {
	return bgwork.Work(bgwork.Deps{
		Native: func(pid int) (claudesession.Native, bool) { return claudesession.ReadNative(cfg, pid) },
		Transcript: func(session string) (string, bool, error) {
			tr, ok, err := claudesession.Find(session, []string{cfg})
			if err != nil || !ok {
				return "", ok, err
			}
			return tr.JSONLPath(), true, nil
		},
		Pending:   claudesession.PendingBackground,
		Ancestors: func() ([]int, error) { return nil, nil },
		Shells:    claudesession.BashToolShells,
		Screen:    func() (string, error) { return screen, nil },
	}, session, pid)
}

// pauseSignal sends SIGTERM: claude saves its session and exits. A pid of 1
// or less is refused: kill(0) and kill(-1) signal a whole group or every
// process of the user (final review M4).
func pauseSignal(pid int) error {
	if pid <= 1 {
		return fmt.Errorf("refusing to signal pid %d", pid)
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

// pauseWaitGone polls the pid every 250 ms until it is gone or timeout.
func pauseWaitGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !claudesession.PidAlive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
}
