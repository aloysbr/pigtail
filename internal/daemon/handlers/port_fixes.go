package handlers

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/aloysbr/pigtail/internal/daemon/rammon"
	"github.com/aloysbr/pigtail/internal/ports"
)

// resyncChecker is the allocator's Reserve predicate, asked without
// reserving (ports.Allocator.ResyncBlockers).
type resyncChecker interface {
	ResyncBlockers(kind string, port int, self string, selfRegistered int) ([]ports.Blocker, error)
}

// serverAt keys a running dev server by (workspace, port).
type serverAt struct {
	ws   string
	port int
}

// resyncTable asks the allocator, in one ResyncBlockersAll, every question
// the planner may ask for this request — one per env_diverges conflict — and
// answers portFixes from that table (EnvSources and every .env.local read once
// per request, outside the allocator's lock, however many conflicts). A failed batch or a
// question not asked answers an error: the planner then gives the port up
// (a logged fallback), never guesses a resync. a == nil (no allocator wired,
// handler tests only) → nil: nothing to ask.
func resyncTable(a *ports.Allocator, conflicts []rammon.PortConflict, logf func(string, ...any)) resyncChecker {
	if a == nil {
		return nil
	}
	var qs []ports.ResyncQuery
	for _, c := range conflicts {
		if c.Reason == rammon.ConflictEnvDiverges {
			qs = append(qs, ports.ResyncQuery{Kind: c.Kind, Port: c.Port, Self: c.Path, SelfRegistered: c.RegisteredPort})
		}
	}
	if len(qs) == 0 {
		return blockerTable{}
	}
	t, err := a.ResyncBlockersAll(qs)
	if err != nil {
		logf("servers: resync blockers: %v", err)
		return blockerTable{err: err}
	}
	return blockerTable{answers: t}
}

// blockerTable is a resyncChecker answering from one ResyncBlockersAll.
type blockerTable struct {
	answers map[ports.ResyncQuery][]ports.Blocker
	err     error
}

func (t blockerTable) ResyncBlockers(kind string, port int, self string, selfRegistered int) ([]ports.Blocker, error) {
	if t.err != nil {
		return nil, t.err
	}
	bs, ok := t.answers[ports.ResyncQuery{Kind: kind, Port: port, Self: self, SelfRegistered: selfRegistered}]
	if !ok {
		return nil, fmt.Errorf("resync of %s :%d for %s not asked", kind, port, self)
	}
	return bs, nil
}

// repair is the ordered repair of one workspace: stop its servers that sit
// on a port it gives up (in a sub-shell, so the user's shell stays where it
// is), move other workspaces off the ports it keeps, reassign the kinds it
// gives up, then resync the rest. resync covers every kind at once
// (PATCH …/ports refuses the whole request on one blocked kind), hence one
// repair per workspace rather than one per conflict.
type repair struct {
	path     string
	stops    map[string]bool
	before   []string
	reassign map[string]bool
	resync   bool
}

func (r *repair) command(id string) string {
	var steps []string
	if len(r.stops) > 0 {
		group := []string{"cd " + quoteWord(r.path)}
		for _, s := range sortedSet(r.stops) {
			group = append(group, "pigtail stop --server "+quoteWord(s))
		}
		steps = append(steps, "("+strings.Join(group, " && ")+")")
	}
	steps = append(steps, r.before...)
	if len(r.reassign) > 0 {
		cmd := "pigtail ports reassign " + quoteWord(id)
		for _, k := range sortedSet(r.reassign) {
			cmd += " --kind " + quoteWord(k)
		}
		steps = append(steps, cmd)
	}
	if r.resync {
		steps = append(steps, "pigtail ports resync "+quoteWord(id))
	}
	return strings.Join(steps, " && ")
}

// giveUp reassigns kind, stopping first the server of ws that runs on port:
// reassign refuses to move a port under a running server.
func (r *repair) giveUp(ws, kind string, port int, running map[serverAt]string) {
	r.reassign[kind] = true
	if srv, ok := running[serverAt{ws, port}]; ok {
		r.stops[srv] = true
	}
}

// moveFirst is the step that moves E's kind off a port before the resync.
func moveFirst(e rammon.PortConflict) string {
	return "pigtail ports reassign " + quoteWord(e.WorkspaceID) + " --kind " + quoteWord(e.Kind)
}

// portFixes returns, index-aligned with conflicts, the one command that
// repairs each — a command that runs as shown in the observed state
// (PATCH …/ports' own refusals, asked through chk). Rules, per env_diverges
// conflict c of workspace W on port P:
//   - W also has a foreign_listener on P → W gives P up (reassign W);
//   - nothing blocks the resync (an allocation in flight is a race, ignored) → resync W;
//   - the only blocker is a live workspace E (its registration of c's kind
//     and/or its .env.local) whose own entry on P, of c's kind, says the
//     listener is W's → reassign E first, then resync W; E's entries on P
//     join W's repair (same command);
//   - anything else (dead row, static server, E running its own server, E's
//     entry on another kind, two registrants, an unreadable registry) → W
//     gives P up.
//
// Kinds of the project without a declared server are not looked at (D1):
// no conflict names them, and no pigtail stop could free one of their ports.
//
// foreign_listener conflicts not joined to another repair give their port up.
// chk == nil (no allocator wired) asks nothing: an unblocked resync is assumed.
func portFixes(conflicts []rammon.PortConflict, running map[serverAt]string, chk resyncChecker, logf func(string, ...any)) []string {
	foreignAt := map[serverAt]bool{}
	for _, c := range conflicts {
		if c.Reason == rammon.ConflictForeignListener {
			foreignAt[serverAt{c.WorkspaceID, c.Port}] = true
		}
	}
	// Pass 1: decide each env_diverges on its own, and which conflicts join
	// another workspace's repair. No workspace both orders and is ordered:
	// ordering W needs W's listener on P, being ordered needs a foreign one.
	const (
		actResync = iota + 1
		actGiveUp
		actOrdered
	)
	decide := func(c rammon.PortConflict) (act, e int) {
		blockers, err := resyncBlockers(chk, c)
		switch {
		case err != nil:
			logf("servers: port fix for %s %s :%d: %v — proposing a fresh port", c.WorkspaceID, c.Kind, c.Port, err)
			return actGiveUp, 0
		case len(blockers) == 0:
			return actResync, 0
		}
		if e, ok := soleLiveBlocker(blockers, c, conflicts); ok {
			return actOrdered, e
		}
		return actGiveUp, 0
	}
	act := make([]int, len(conflicts))
	orderedE := make([]int, len(conflicts))
	joins := make([]string, len(conflicts)) // workspace whose repair conflict i joins; "" = its own
	join := func(w string, e rammon.PortConflict) {
		for j, d := range conflicts {
			if d.WorkspaceID == e.WorkspaceID && d.Port == e.Port {
				joins[j] = w
			}
		}
	}
	for i, c := range conflicts {
		if c.Reason != rammon.ConflictEnvDiverges {
			continue
		}
		if foreignAt[serverAt{c.WorkspaceID, c.Port}] {
			act[i] = actGiveUp
			continue
		}
		act[i], orderedE[i] = decide(c)
		if act[i] == actOrdered {
			join(c.WorkspaceID, conflicts[orderedE[i]])
		}
	}
	// Pass 2: build each workspace's repair from what did not join another.
	repairs := map[string]*repair{}
	repairOf := func(ws, path string) *repair {
		r, ok := repairs[ws]
		if !ok {
			r = &repair{path: path, stops: map[string]bool{}, reassign: map[string]bool{}}
			repairs[ws] = r
		}
		return r
	}
	for i, c := range conflicts {
		if joins[i] != "" {
			continue
		}
		r := repairOf(c.WorkspaceID, c.Path)
		switch {
		case c.Reason == rammon.ConflictForeignListener:
			r.reassign[c.Kind] = true // not its listener on its port: nothing of its own to stop there
		case act[i] == actResync:
			r.resync = true
		case act[i] == actOrdered:
			r.before = append(r.before, moveFirst(conflicts[orderedE[i]]))
			r.resync = true
		default:
			r.giveUp(c.WorkspaceID, c.Kind, c.Port, running)
		}
	}
	out := make([]string, len(conflicts))
	for i, c := range conflicts {
		id := c.WorkspaceID
		if joins[i] != "" {
			id = joins[i]
		}
		out[i] = repairs[id].command(id)
	}
	return out
}

// resyncBlockers asks chk about c, dropping allocations in flight (a race,
// out of the fix's contract).
func resyncBlockers(chk resyncChecker, c rammon.PortConflict) ([]ports.Blocker, error) {
	if chk == nil {
		return nil, nil
	}
	all, err := chk.ResyncBlockers(c.Kind, c.Port, c.Path, c.RegisteredPort)
	if err != nil {
		return nil, err
	}
	var out []ports.Blocker
	for _, b := range all {
		if b.Reason != ports.BlockPending {
			out = append(out, b)
		}
	}
	return out, nil
}

// soleLiveBlocker finds the conflict j of a live workspace E such that E is
// every blocker of c's resync (registered as E, or E's .env.local) and E's
// own entry on c.Port, of c's kind, says the listener there is c's
// workspace's.
func soleLiveBlocker(blockers []ports.Blocker, c rammon.PortConflict, conflicts []rammon.PortConflict) (int, bool) {
	for j, e := range conflicts {
		if e.Reason != rammon.ConflictForeignListener || e.Port != c.Port ||
			e.WorkspaceID == c.WorkspaceID || e.HolderWorkspaceID != c.WorkspaceID {
			continue
		}
		if blockedOnlyBy(blockers, c, e) {
			return j, true
		}
	}
	return 0, false
}

// blockedOnlyBy: every blocker of c is E — its registration, which must be
// of c's kind on E's entry too (reassigning E's other kind would leave c's
// kind registered), or its .env.local.
func blockedOnlyBy(blockers []ports.Blocker, c, e rammon.PortConflict) bool {
	for _, b := range blockers {
		switch b.Reason {
		case ports.BlockRegistered:
			if b.Static || b.WorkspaceID == "" || b.WorkspaceID != e.WorkspaceID || e.Kind != c.Kind {
				return false
			}
		case ports.BlockEnvLocal:
			if e.Path == "" || filepath.Clean(b.Dir) != filepath.Clean(e.Path) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_./@%+=:,-]+$`)

// quoteWord makes w one shell word: as is when plain, else single-quoted
// (empty included). Every word put in a fix goes through it — path, server,
// kind, workspace id.
func quoteWord(w string) string {
	if plainWord.MatchString(w) {
		return w
	}
	return "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
