package cmuxsync

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The server pill (spec §4.1, lot 3): `dev :5173 up` under the cmux
// workspace that started the server — its owner, CMUX_WORKSPACE_ID at
// `pigtail dev` time.
//
// What pigtaild can really tell apart:
//   - up: the port listens (rammon.Inspect, a live probe).
//   - dead: rammon recorded a death — the server vanished while no stop
//     was announced (every pigtail stop path, the CLI's included, marks its
//     stop first). Known from rammon's sweep (15 s by default), announced on
//     the bus as resources.server_died.
//   - stopped: seen up, gone now, and a rammon sweep made after it was seen
//     gone recorded no death. That is a deliberate stop (pigtail stop, idle
//     purge, eviction). Until that sweep the server keeps its last state,
//     up: a crash rammon has not judged yet never reads stopped. With rammon
//     switched off (no sweep ever) a disappearance reads stopped at once,
//     and nothing ever reads dead.
//   - conflict: rammon recorded a port conflict for the worktree — its port
//     is held by another worktree's process, or its .env.local names another
//     port than the registry. Shown under the cmux workspace of the
//     worktree in conflict, it wins over up for the same port, and is not
//     retained: gone from the next snapshot, the line goes.

// ServerState is one server's state on the pill and on the wire.
type ServerState string

// Server states, on the wire and on the pill alike.
const (
	ServerUp      ServerState = "up"
	ServerStopped ServerState = "stopped"
	ServerDead    ServerState = "dead"
	// ServerConflict: the worktree's port is in conflict (rammon.Conflicts).
	ServerConflict ServerState = "conflict"
)

// Retention of the states that outlive the process (spec §4.2 table).
const (
	// StoppedRetention: a stopped server's line goes after 5 min.
	StoppedRetention = 5 * time.Minute
	// DeadRetention mirrors rammon's death window: past it, a death is no
	// longer reported, and the line goes.
	DeadRetention = 30 * time.Minute
	// SweepWaitMax bounds the wait for rammon's judgement of a server seen
	// gone: past it (rammon stalled), the server reads stopped anyway.
	SweepWaitMax = 2 * time.Minute
)

// Rendering of the server pill. No glyph in the text: cmux draws the icon
// of the worst state in front of it; the colour is the gravity of the worst
// state (waittype/gravity.go): dead red, conflict orange, up and stopped
// grey (D7).
const (
	serversPriority = 60
	serverSep       = " · "
)

var serverLook = map[ServerState]struct {
	label, icon, color string
	rank               int // the worst state colours the line
}{
	ServerDead:     {"dead", "xmark.circle", waittype.Red.Color(), 4},
	ServerConflict: {"conflict", "exclamationmark.triangle", waittype.Orange.Color(), 3},
	ServerUp:       {"up", "play.fill", waittype.Grey.Color(), 2}, // D7: green means work ready only
	ServerStopped:  {"stopped", "stop.fill", waittype.Grey.Color(), 1},
}

// serverNames is the one table of server names as the pill shows them, in
// priority order: the line is narrow and cut from the second server on, so a
// long name gets a short one, and among twins on one port (`dev+web`) the
// first listed here is the one shown. A name absent from the table shows as
// is, and ranks after every listed one.
var serverNames = []struct{ name, short string }{
	{"dev", "dev"},
	{"storybook", "sb"},
}

// serverDisplayName is the single name a server (or a `+`-joined set of
// twins) shows on the pill: the twin ranked first by serverNames, else the
// shortest once shortened, alphabetical on a tie. The wire keeps the real
// names.
func serverDisplayName(joined string) string {
	best, bestRank := "", 0
	for _, n := range strings.Split(joined, "+") {
		if n == "" {
			continue
		}
		short, rank := n, len(serverNames)
		for i, e := range serverNames {
			if e.name == n {
				short, rank = e.short, i
				break
			}
		}
		switch {
		case best == "",
			rank < bestRank,
			rank == bestRank && len(short) < len(best),
			rank == bestRank && len(short) == len(best) && short < best:
			best, bestRank = short, rank
		}
	}
	return best
}

// Server is one dev server as the sidebar shows it, attached to the cmux
// workspace that owns it. Twins listed under several names on one port (one
// process) are one Server named `dev+web`.
type Server struct {
	WorkspaceID string // cmux workspace UUID, upper case
	Name        string
	Port        int
	State       ServerState
}

// serversPill renders a workspace's servers on one line, sorted by the name
// shown, to stay within the sidebar's three visible lines. No server, no
// pill.
func serversPill(ss []Server) (cmux.StatusEntry, bool) {
	if len(ss) == 0 {
		return cmux.StatusEntry{}, false
	}
	type shown struct {
		name string
		Server
	}
	sorted := make([]shown, 0, len(ss))
	for _, s := range ss {
		sorted = append(sorted, shown{serverDisplayName(s.Name), s})
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].name != sorted[j].name {
			return sorted[i].name < sorted[j].name
		}
		return sorted[i].Port < sorted[j].Port
	})
	parts := make([]string, 0, len(sorted))
	worst := ServerStopped
	for _, s := range sorted {
		parts = append(parts, s.name+" :"+strconv.Itoa(s.Port)+" "+serverLook[s.State].label)
		if serverLook[s.State].rank > serverLook[worst].rank {
			worst = s.State
		}
	}
	look := serverLook[worst]
	return cmux.StatusEntry{
		Key: KeyServers, Value: sidebarText(strings.Join(parts, serverSep)),
		Icon: look.icon, Color: look.color, Priority: serversPriority,
	}, true
}

// wireServers is the servers of a workspace for the API and the bus.
func wireServers(ss []Server) []pigtail.CmuxSidebarServer {
	out := make([]pigtail.CmuxSidebarServer, 0, len(ss))
	for _, s := range ss {
		out = append(out, pigtail.CmuxSidebarServer{Name: s.Name, Port: s.Port, State: string(s.State)})
	}
	return out
}

// ServerObs is a listening dev server as pigtaild sees it (rammon.Inspect).
type ServerObs struct {
	WorkspaceID string // pigtail workspace (worktree)
	Server      string
	Port        int
	Owner       string // the session that started it; a cmux workspace UUID inside cmux
}

// ServerDeath is a death rammon recorded, with the owner of the server that
// died (read back from its marker, which a death leaves in place).
type ServerDeath struct {
	WorkspaceID string
	Servers     []string
	Port        int
	At          time.Time
	Owner       string
}

// ServerSnapshot is one reading of the servers: who listens, who died, and
// when rammon last judged deaths (zero: never, the monitor is off or has not
// swept yet).
type ServerSnapshot struct {
	Up      []ServerObs
	Deaths  []ServerDeath
	SweptAt time.Time
	// Conflicts are the worktrees whose port is in conflict, one per cmux
	// workspace to show it under (Owner).
	Conflicts []ServerObs
}

// ServerTracker gives each server its state from successive snapshots. It
// only remembers what a snapshot cannot say: when a server was last seen up
// (a death before that is an old one), and since when it is stopped. Memory
// only: a daemon restart forgets the stopped ones, which is acceptable
// (spec §4.1); the dead ones come back from rammon's persisted ring.
type ServerTracker struct {
	entries map[serverKey]*trackedServer
}

// serverKey is one process: a pigtail workspace and a port.
type serverKey struct {
	ws   string
	port int
}

type trackedServer struct {
	owner     string
	names     []string
	lastUp    time.Time
	state     ServerState
	goneAt    time.Time // first seen gone, waiting for rammon's judgement
	stoppedAt time.Time
}

// Observe folds a snapshot taken at now and returns the servers to show,
// sorted by cmux workspace then name. A server with no owner is dropped:
// there is no workspace to show it under.
func (t *ServerTracker) Observe(now time.Time, snap ServerSnapshot) []Server {
	if t.entries == nil {
		t.entries = map[serverKey]*trackedServer{}
	}
	upNow := map[serverKey]*trackedServer{}
	for _, o := range snap.Up {
		k := serverKey{o.WorkspaceID, o.Port}
		e := upNow[k]
		if e == nil {
			e = &trackedServer{state: ServerUp, lastUp: now}
			upNow[k] = e
		}
		e.names = appendName(e.names, o.Server)
		if e.owner == "" {
			e.owner = normOwner(o.Owner)
		}
	}
	deaths := map[serverKey]ServerDeath{}
	for _, d := range snap.Deaths {
		if now.Sub(d.At) >= DeadRetention {
			continue
		}
		k := serverKey{d.WorkspaceID, d.Port}
		if prev, ok := deaths[k]; !ok || d.At.After(prev.At) {
			deaths[k] = d
		}
	}

	for k, e := range upNow {
		t.entries[k] = e
	}
	for k, d := range deaths {
		if upNow[k] != nil {
			continue // restarted since
		}
		e := t.entries[k]
		if e != nil && e.lastUp.After(d.At) {
			continue // seen up after that death: an old one
		}
		if e == nil {
			e = &trackedServer{}
			t.entries[k] = e
		}
		e.state = ServerDead
		if o := normOwner(d.Owner); o != "" {
			e.owner = o
		}
		if len(d.Servers) > 0 {
			e.names = nil
			for _, n := range d.Servers {
				e.names = appendName(e.names, n)
			}
		}
	}
	for k, e := range t.entries {
		if upNow[k] != nil {
			continue
		}
		if _, dead := deaths[k]; dead && e.state == ServerDead {
			continue
		}
		switch e.state {
		case ServerUp:
			if e.goneAt.IsZero() {
				e.goneAt = now
			}
			if !judged(snap.SweptAt, e.goneAt, now) {
				continue // gone, not judged yet: keep showing it up
			}
			e.state, e.stoppedAt = ServerStopped, now
		case ServerDead:
			delete(t.entries, k) // its death left the window: gone, not stopped
			continue
		}
		if now.Sub(e.stoppedAt) >= StoppedRetention {
			delete(t.entries, k)
		}
	}

	var out []Server
	for k, e := range t.entries {
		if e.owner == "" {
			continue
		}
		out = append(out, Server{WorkspaceID: e.owner, Name: strings.Join(e.names, "+"), Port: k.port, State: e.state})
	}
	// Conflicts are not tracked: they come from the snapshot alone, win over
	// whatever line the same cmux workspace shows for that port, and leave
	// with the snapshot that no longer reports them.
	for _, c := range snap.Conflicts {
		owner := normOwner(c.Owner)
		if owner == "" {
			continue
		}
		replaced := false
		for i := range out {
			if out[i].WorkspaceID == owner && out[i].Port == c.Port {
				out[i].State = ServerConflict
				replaced = true
			}
		}
		if !replaced {
			out = append(out, Server{WorkspaceID: owner, Name: c.Server, Port: c.Port, State: ServerConflict})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].WorkspaceID != out[j].WorkspaceID {
			return out[i].WorkspaceID < out[j].WorkspaceID
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// judged reports whether rammon had its say on a server seen gone at goneAt:
// a sweep strictly after it saw the port free too and recorded a death or
// not. No sweep ever (monitor off) is a judgement too — none will come.
func judged(sweptAt, goneAt, now time.Time) bool {
	return sweptAt.IsZero() || sweptAt.After(goneAt) || now.Sub(goneAt) >= SweepWaitMax
}

func appendName(names []string, n string) []string {
	for _, x := range names {
		if x == n {
			return names
		}
	}
	names = append(names, n)
	sort.Strings(names)
	return names
}

// normOwner matches cmux's upper-case workspace UUIDs.
func normOwner(o string) string { return strings.ToUpper(strings.TrimSpace(o)) }
