package daemon

import (
	"log"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/internal/ports"
)

// NewAllocator is the daemon's port allocator as production runs it: loopback
// binds, one bulk listener inventory per Allocate, and every live workspace's
// .env.local. cmd/pigtaild and daemon.New's default both call it.
func NewAllocator(st *store.Store) *ports.Allocator { return newAllocator(st, devserver.ListListeners) }

func newAllocator(st *store.Store, list func() (map[int][]int, error)) *ports.Allocator {
	return ports.New(st,
		ports.WithListenerSnapshot(listenerSnapshotWith(list, log.Printf)),
		ports.WithEnvSources(StoreEnvSources(st)))
}
