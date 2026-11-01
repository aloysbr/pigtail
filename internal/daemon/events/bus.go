package events

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// defaultRingSize bounds the replay buffer kept in memory for SSE
// reconnects. ~24h of events at a moderate rate.
const defaultRingSize = 4096

// defaultSubBuffer bounds each subscriber's channel. A slow subscriber
// is dropped rather than blocking the hub.
const defaultSubBuffer = 256

// Subscription is the handle returned by Subscribe. The caller must call
// Close to release resources. Events arrive on C, which is closed when
// the subscription ends.
type Subscription struct {
	C      <-chan pigtail.Event
	cancel func()
}

// Close unsubscribes and drains the channel.
func (s *Subscription) Close() { s.cancel() }

// Bus is an in-memory pub/sub hub with a ring buffer for SSE replay.
type Bus struct {
	mu        sync.Mutex
	subs      map[int64]*subscriber
	nextSubID int64

	ringMu  sync.RWMutex
	ring    []pigtail.Event
	ringCap int

	dropped atomic.Uint64
}

type subscriber struct {
	ch     chan pigtail.Event
	types  []string // patterns; nil = all
	cancel func()
}

// New constructs a Bus with the default ring size.
func New() *Bus { return NewWithCapacity(defaultRingSize) }

// NewWithCapacity constructs a Bus with a custom ring buffer size.
func NewWithCapacity(ringCap int) *Bus {
	if ringCap <= 0 {
		ringCap = defaultRingSize
	}
	return &Bus{
		subs:    map[int64]*subscriber{},
		ring:    make([]pigtail.Event, 0, ringCap),
		ringCap: ringCap,
	}
}

// Publish records an event in the ring buffer and fans it out to matching
// subscribers. Returns the event ID (generated if Event.ID is empty).
func (b *Bus) Publish(e pigtail.Event) string {
	if e.ID == "" {
		e.ID = newEventID()
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}

	b.ringMu.Lock()
	if len(b.ring) >= b.ringCap {
		b.ring = append(b.ring[:0], b.ring[1:]...)
	}
	b.ring = append(b.ring, e)
	b.ringMu.Unlock()

	b.mu.Lock()
	for _, s := range b.subs {
		if !matches(s.types, string(e.Type)) {
			continue
		}
		select {
		case s.ch <- e:
		default:
			b.dropped.Add(1)
		}
	}
	b.mu.Unlock()
	return e.ID
}

// Subscribe registers a new subscriber. Pass empty types to receive all
// events, or patterns like "workspace.*", "agent.paused".
func (b *Bus) Subscribe(types ...string) *Subscription {
	ch := make(chan pigtail.Event, defaultSubBuffer)
	b.mu.Lock()
	b.nextSubID++
	id := b.nextSubID
	sub := &subscriber{ch: ch, types: types}
	b.subs[id] = sub
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			b.mu.Unlock()
			close(ch)
		})
	}
	sub.cancel = cancel
	return &Subscription{C: ch, cancel: cancel}
}

// Replay returns the events in the ring buffer with ID strictly greater
// than sinceID (or all if sinceID is empty), filtered by the given type
// patterns.
func (b *Bus) Replay(sinceID string, types []string) []pigtail.Event {
	b.ringMu.RLock()
	defer b.ringMu.RUnlock()
	out := make([]pigtail.Event, 0, len(b.ring))
	seen := sinceID == ""
	for _, e := range b.ring {
		if !seen {
			if e.ID == sinceID {
				seen = true
			}
			continue
		}
		if !matches(types, string(e.Type)) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Dropped returns the number of events that were dropped because a
// subscriber's buffer was full. Useful for observability.
func (b *Bus) Dropped() uint64 { return b.dropped.Load() }

func matches(patterns []string, eventType string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if p == eventType {
			return true
		}
		if strings.HasSuffix(p, ".*") {
			prefix := strings.TrimSuffix(p, "*")
			if strings.HasPrefix(eventType, prefix) {
				return true
			}
		}
	}
	return false
}

func newEventID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return pigtail.PrefixEvent + hex.EncodeToString(b[:])
}
