// Package ports provides a port allocator that finds free ports within
// caller-supplied ranges and avoids reusing ports already claimed by other
// workspaces.
package ports

import (
	"fmt"
	"path/filepath"
	"sync"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// UsedLister returns the set of ports currently held by a given kind
// (e.g. "dev", "storybook") across all known workspaces. The Allocator
// calls it on every Allocate to seed its "do not pick" set; this is what
// keeps allocation correct after a daemon restart.
type UsedLister interface {
	UsedPorts(kind string) (map[int]bool, error)
}

// RegistrantLister is optionally implemented by a UsedLister: it names who
// registers a port — workspace rows of that kind, and whether a static
// server holds the number. The store implements it.
type RegistrantLister interface {
	PortRegistrants(kind string, port int) (workspaceIDs []string, static bool, err error)
}

// Blocker is one rule that refuses a resync of (kind, port) — the reasons
// Reserve refuses, each with whom it is about.
type Blocker struct {
	Reason      string // BlockRegistered | BlockPending | BlockEnvLocal
	WorkspaceID string // registered: the registering row ("" when Static, or when the lister cannot name it)
	Static      bool   // registered: a static server holds the number
	Dir         string // env_local: the worktree whose .env.local names the port
}

// Reasons of a Blocker — the "reason" detail of Reserve's refusals.
const (
	BlockRegistered = "registered"
	BlockPending    = "pending"
	BlockEnvLocal   = "env_local"
)

// ResyncQuery is one resync question: would Reserve(Kind, Port, Self,
// SelfRegistered) refuse, and on whom.
type ResyncQuery struct {
	Kind           string
	Port           int
	Self           string
	SelfRegistered int
}

// ResyncBlockers answers what Reserve(kind, port, self, selfRegistered) would
// refuse on, every blocker named, without reserving anything. Empty: a
// Reserve would succeed now.
func (a *Allocator) ResyncBlockers(kind string, port int, self string, selfRegistered int) ([]Blocker, error) {
	q := ResyncQuery{Kind: kind, Port: port, Self: self, SelfRegistered: selfRegistered}
	all, err := a.ResyncBlockersAll([]ResyncQuery{q})
	return all[q], err
}

// ResyncBlockersAll answers ResyncBlockers for every question of one caller
// at once — GET /servers asks it to plan fixes that run, on a route polled
// every few seconds. Each source is read once whatever the number of
// questions: EnvSources and every .env.local once, UsedPorts once per kind,
// PortRegistrants once per blocked (kind, port). The reads are made outside
// a.mu — a slow listing must never hold up an Allocate or a Reserve — and
// only the pending set is copied under it: a read-only answer as fresh as
// the call, stale at worst by the reads' duration (a race, which the fix's
// contract excludes).
func (a *Allocator) ResyncBlockersAll(qs []ResyncQuery) (map[ResyncQuery][]Blocker, error) {
	a.mu.Lock()
	pending := make(map[string]map[int]bool, len(a.pending))
	for k, ps := range a.pending {
		pending[k] = make(map[int]bool, len(ps))
		for p := range ps {
			pending[k][p] = true
		}
	}
	a.mu.Unlock()
	r := newBlockerReads(a, pending)
	out := make(map[ResyncQuery][]Blocker, len(qs))
	for _, q := range qs {
		bs, err := r.blockers(q, true)
		if err != nil {
			return nil, err
		}
		out[q] = bs
	}
	return out, nil
}

// blockerReads is Reserve's predicate over reads made at most once each:
// UsedPorts per kind, PortRegistrants per (kind, port), EnvSources and every
// .env.local once (envPortNamers over every source, self filtered per
// question — the same Dirs, in the same order, as excluding self's source
// first).
type blockerReads struct {
	a       *Allocator
	pending map[string]map[int]bool
	used    map[string]map[int]bool
	regs    map[ResyncQuery]registrants // keyed by (Kind, Port) only
	namers  map[int][]string
	envRead bool
}

type registrants struct {
	ids    []string
	static bool
}

func newBlockerReads(a *Allocator, pending map[string]map[int]bool) *blockerReads {
	return &blockerReads{a: a, pending: pending, used: map[string]map[int]bool{}, regs: map[ResyncQuery]registrants{}}
}

// blockers is Reserve's predicate, in its refusal order (registered, pending,
// env_local). all=false stops at the first blocker, unnamed — Reserve's own
// behaviour, unchanged.
func (r *blockerReads) blockers(q ResyncQuery, all bool) ([]Blocker, error) {
	var out []Blocker
	if r.a.used != nil && q.Port != q.SelfRegistered {
		used, ok := r.used[q.Kind]
		if !ok {
			var err error
			if used, err = r.a.used.UsedPorts(q.Kind); err != nil { // every row and every static server
				return nil, fmt.Errorf("ports: list used: %w", err)
			}
			r.used[q.Kind] = used
		}
		if used[q.Port] {
			named := false
			if rl, ok := r.a.used.(RegistrantLister); ok && all {
				key := ResyncQuery{Kind: q.Kind, Port: q.Port}
				reg, seen := r.regs[key]
				if !seen {
					ids, static, err := rl.PortRegistrants(q.Kind, q.Port)
					if err != nil {
						return nil, fmt.Errorf("ports: list registrants: %w", err)
					}
					reg = registrants{ids, static}
					r.regs[key] = reg
				}
				for _, id := range reg.ids {
					out = append(out, Blocker{Reason: BlockRegistered, WorkspaceID: id})
				}
				if reg.static {
					out = append(out, Blocker{Reason: BlockRegistered, Static: true})
				}
				named = len(reg.ids) > 0 || reg.static
			}
			if !named {
				out = append(out, Blocker{Reason: BlockRegistered})
			}
			if !all {
				return out, nil
			}
		}
	}
	if r.pending[q.Kind][q.Port] {
		out = append(out, Blocker{Reason: BlockPending})
		if !all {
			return out, nil
		}
	}
	if r.a.env != nil {
		if !r.envRead {
			srcs, err := r.a.env.EnvSources()
			if err != nil {
				return nil, fmt.Errorf("ports: list .env.local sources: %w", err)
			}
			r.namers, r.envRead = envPortNamers(srcs), true
		}
		for _, dir := range r.namers[q.Port] {
			if filepath.Clean(dir) == filepath.Clean(q.Self) {
				continue
			}
			out = append(out, Blocker{Reason: BlockEnvLocal, Dir: dir})
			if !all {
				return out, nil
			}
		}
	}
	return out, nil
}

// Allocator finds free ports within caller-supplied ranges, skipping
// ports already claimed in the store and ports currently bound on the
// machine.
//
// The allocator is stateless with respect to *which* ranges exist — it
// receives the range map at each Allocate call. The only state it holds
// is the in-flight ("pending") set, used to avoid double-allocation
// between Allocate and the moment the workspace is persisted.
type Allocator struct {
	used     UsedLister
	bindable func(port int) bool
	snapshot func() (map[int]bool, error)
	env      EnvLister

	mu      sync.Mutex
	pending map[string]map[int]bool // kind → {port: true}
}

// Option configures an Allocator.
type Option func(*Allocator)

// New constructs an Allocator that consults `used` for already-claimed
// ports. `used` may be nil for tests. By default a port must bind on both
// loopbacks (CanBind); options add or replace probes.
func New(used UsedLister, opts ...Option) *Allocator {
	a := &Allocator{used: used, pending: map[string]map[int]bool{}, bindable: CanBind}
	for _, o := range opts {
		o(a)
	}
	return a
}

// WithBindProbe replaces the OS bind probe (tests simulate a listener the
// real probe would see, e.g. an IPv6-only one).
func WithBindProbe(p func(port int) bool) Option { return func(a *Allocator) { a.bindable = p } }

// WithListenerSnapshot gives Allocate one inventory of the listening ports,
// taken once per call before the lock (lsof costs ~0.1 s for the whole
// machine, the same per port). It catches what the loopback binds cannot:
// wildcard and LAN listeners (D7: pigtaild never binds those itself).
func WithListenerSnapshot(f func() (map[int]bool, error)) Option {
	return func(a *Allocator) { a.snapshot = f }
}

// Allocate picks the lowest free port for the given kind, within the
// caller-supplied range map. Returns ErrPortExhausted (a pigtail.APIError)
// if no port is available.
func (a *Allocator) Allocate(ranges map[string]pigtail.PortRange, kind string) (int, error) {
	rng, ok := ranges[kind]
	if !ok {
		return 0, fmt.Errorf("ports: no range configured for kind %q", kind)
	}
	var listening map[int]bool
	if a.snapshot != nil {
		l, err := a.snapshot()
		if err != nil {
			return 0, fmt.Errorf("ports: listener snapshot: %w", err)
		}
		listening = l
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	var taken map[int]bool
	if a.used != nil {
		t, err := a.used.UsedPorts(kind)
		if err != nil {
			return 0, fmt.Errorf("ports: list used: %w", err)
		}
		taken = t
	}
	var named map[int]bool
	if a.env != nil {
		srcs, err := a.env.EnvSources()
		if err != nil {
			return 0, fmt.Errorf("ports: list .env.local sources: %w", err)
		}
		named = envPorts(srcs)
	}
	pending := a.pending[kind]

	for p := rng[0]; p <= rng[1]; p++ {
		if taken[p] || pending[p] || named[p] {
			continue
		}
		if !a.bindable(p) || listening[p] {
			continue
		}
		if pending == nil {
			pending = map[int]bool{}
			a.pending[kind] = pending
		}
		pending[p] = true
		return p, nil
	}
	return 0, &pigtail.APIError{
		Code:    pigtail.ErrPortExhausted,
		Message: fmt.Sprintf("no free port in range %d-%d for kind %q", rng[0], rng[1], kind),
	}
}

// Pending returns a copy of the in-flight ports of kind: allocated, not yet
// confirmed or released. For tests and diagnostics.
func (a *Allocator) Pending(kind string) map[int]bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[int]bool, len(a.pending[kind]))
	for p := range a.pending[kind] {
		out[p] = true
	}
	return out
}

// Reserve claims a port the caller chose (resync: the one .env.local already
// names) under the allocator's lock, with allocation's "taken" rules minus the
// bind probe — the port is expected to be self's own server's — and minus
// self's own .env.local and registered port (selfRegistered, 0 when none).
// It ends like an allocation: Confirm or Release. Its refusals are
// ResyncBlockers' first blocker; unlike ResyncBlockersAll it reads under
// the lock, as before, so that what it checked is what it reserves.
func (a *Allocator) Reserve(kind string, port int, self string, selfRegistered int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	bs, err := newBlockerReads(a, a.pending).blockers(ResyncQuery{Kind: kind, Port: port, Self: self, SelfRegistered: selfRegistered}, false)
	if err != nil {
		return err
	}
	if len(bs) > 0 {
		return reserveRefusal(kind, port, bs[0])
	}
	if a.pending[kind] == nil {
		a.pending[kind] = map[int]bool{}
	}
	a.pending[kind][port] = true
	return nil
}

// reserveRefusal is Reserve's error for its first blocker — messages and
// details byte-identical to before the predicate was shared.
func reserveRefusal(kind string, port int, b Blocker) error {
	switch b.Reason {
	case BlockRegistered:
		return &pigtail.APIError{Code: pigtail.ErrConflict,
			Message: fmt.Sprintf("port %d (%s) is registered to another workspace or a static server", port, kind),
			Details: map[string]interface{}{"kind": kind, "port": port, "reason": "registered"}}
	case BlockPending:
		return &pigtail.APIError{Code: pigtail.ErrConflict,
			Message: fmt.Sprintf("port %d (%s) is being allocated to another workspace", port, kind),
			Details: map[string]interface{}{"kind": kind, "port": port, "reason": "pending"}}
	default:
		return &pigtail.APIError{Code: pigtail.ErrConflict,
			Message: fmt.Sprintf("port %d (%s) is named in %s/.env.local", port, kind, b.Dir),
			Details: map[string]interface{}{"kind": kind, "port": port, "reason": "env_local", "holder_path": b.Dir}}
	}
}

// Confirm tells the allocator a port has been persisted to the store
// and can be dropped from the in-flight set.
func (a *Allocator) Confirm(kind string, port int) {
	a.mu.Lock()
	if m := a.pending[kind]; m != nil {
		delete(m, port)
	}
	a.mu.Unlock()
}

// Release reverses Allocate without ever having been Confirmed.
// Identical to Confirm in practice but expresses intent.
func (a *Allocator) Release(kind string, port int) { a.Confirm(kind, port) }
