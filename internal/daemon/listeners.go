package daemon

import (
	"log"

	"github.com/aloysbr/pigtail/internal/devserver"
)

// ListenerSnapshot is the allocator's production inventory: one bulk lsof
// (devserver.ListListeners) for the whole machine, reduced to its ports. An
// lsof that could not run yields an empty set and a log line: the loopback
// binds then decide alone, rather than the whole range reading exhausted.
func ListenerSnapshot() (map[int]bool, error) {
	return listenerSnapshotWith(devserver.ListListeners, log.Printf)()
}

func listenerSnapshotWith(list func() (map[int][]int, error), logf func(string, ...any)) func() (map[int]bool, error) {
	return func() (map[int]bool, error) {
		inv, err := list()
		if err != nil {
			logf("ports: listener inventory failed, trusting the loopback binds: %v", err)
			return map[int]bool{}, nil
		}
		out := make(map[int]bool, len(inv))
		for p, pids := range inv {
			if len(pids) > 0 {
				out[p] = true
			}
		}
		return out, nil
	}
}
