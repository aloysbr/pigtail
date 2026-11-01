// Package cpuslots arbitrates a global budget of CPU slots between every
// CPU-intensive consumer on the machine — vitest runs, above all — so that N
// concurrent runs share one machine instead of each sizing its worker pool as
// if it owned the box.
//
// The daemon arbitrates and nothing else: it spawns no process and kills none
// (docs/architecture.md §10). A slot is held for exactly as long as the
// client's HTTP connection stays open; the kernel closing that socket is the
// release, so there is no heartbeat, no TTL and no PID to watch.
package cpuslots

// GroupDemand is one active group's demand, as the decision functions see it.
// A group is active when Held+Waiting > 0.
type GroupDemand struct {
	Group   string
	Held    int
	Waiting int
}

// demand is what the group would use if the machine were free.
func (g GroupDemand) demand() int { return g.Held + g.Waiting }

// Waiter is one queued request, as the decision functions see it. The queue's
// slice order carries the FIFO, so nothing else is needed here.
type Waiter struct {
	Group string
}

// shares computes each active group's fair share of budget by max-min
// fairness: a group asking for less than an equal split keeps only what it
// asks for, and its surplus is redistributed to the groups that want more.
// The pass repeats until nobody is under-served, then whatever remains is
// split evenly among the still-hungry groups.
//
// groups MUST be ordered oldest-first: an integer budget rarely divides
// evenly, and the remaining units go to the earliest entries. Ordering by age
// is what makes that tie-break stable rather than arbitrary — and it is the
// rule §3 of the design states ("le reste étant distribué aux groupes les plus
// anciens").
//
// Pure: no I/O, no mutable state, no clock.
func shares(groups []GroupDemand, budget int) map[string]int {
	out := map[string]int{}
	if budget <= 0 {
		return out
	}
	pending := make([]GroupDemand, 0, len(groups))
	for _, g := range groups {
		if g.demand() > 0 {
			pending = append(pending, g)
		}
	}
	if len(pending) == 0 {
		return out
	}

	remaining := budget
	for len(pending) > 0 {
		fair := remaining / len(pending)
		if fair == 0 {
			break // fewer slots than groups: settled by the even split below
		}
		hungry := pending[:0]
		satisfied := false
		for _, g := range pending {
			if g.demand() <= fair {
				out[g.Group] = g.demand()
				remaining -= g.demand()
				satisfied = true
				continue
			}
			hungry = append(hungry, g)
		}
		pending = hungry
		if !satisfied {
			break // everyone still wants more than an equal split
		}
	}

	if len(pending) == 0 {
		return out
	}
	base := remaining / len(pending)
	extra := remaining % len(pending)
	for i, g := range pending {
		q := base
		if i < extra {
			q++ // the remainder goes to the oldest groups
		}
		out[g.Group] = q
	}
	return out
}

// nextToServe picks the waiter to hand the next free slot to. queue MUST be
// ordered oldest-first.
//
// It walks the FIFO and serves the first waiter whose group is still below
// its share. When no group is below its share it serves the oldest waiter
// regardless: a free slot left unused would be worse for everyone than a
// group briefly over its share, and the share is recomputed on the very next
// acquire or release anyway.
//
// Returns the index into queue, or -1 when the queue is empty.
// Pure: no I/O, no mutable state, no clock.
func nextToServe(queue []Waiter, held, share map[string]int) int {
	if len(queue) == 0 {
		return -1
	}
	for i, w := range queue {
		if held[w.Group] < share[w.Group] {
			return i
		}
	}
	return 0
}
