package cpuslots

import "testing"

func TestSharesSingleGroupTakesTheWholeBudget(t *testing.T) {
	got := shares([]GroupDemand{{Group: "a", Waiting: 20}}, 9)
	if got["a"] != 9 {
		t.Fatalf("shares(a demanding 20, budget 9)[a] = %d, want 9", got["a"])
	}
}

// The spec's convergence example (§3): budget 9, A holds 9, B queues 9.
// Both want more than half, so the budget splits evenly and the remainder
// goes to the oldest group — A, which is first in the slice.
func TestSharesEvenSplitRemainderToTheOldest(t *testing.T) {
	got := shares([]GroupDemand{
		{Group: "a", Held: 9},
		{Group: "b", Waiting: 9},
	}, 9)
	if got["a"] != 5 || got["b"] != 4 {
		t.Fatalf("shares = a:%d b:%d, want a:5 b:4", got["a"], got["b"])
	}
}

// A group asking for less than its share frees the difference for the others:
// max-min fairness, not a flat division.
func TestSharesSurplusOfAModestGroupIsRedistributed(t *testing.T) {
	got := shares([]GroupDemand{
		{Group: "small", Held: 2},
		{Group: "big", Waiting: 9},
	}, 9)
	if got["small"] != 2 || got["big"] != 7 {
		t.Fatalf("shares = small:%d big:%d, want small:2 big:7", got["small"], got["big"])
	}
}

// Three groups, one modest: the modest one is capped at its demand and the
// two greedy ones split what is left, remainder to the older of the two.
func TestSharesThreeGroupsOneModest(t *testing.T) {
	got := shares([]GroupDemand{
		{Group: "a", Held: 1},
		{Group: "b", Waiting: 8},
		{Group: "c", Waiting: 8},
	}, 9)
	if got["a"] != 1 || got["b"] != 4 || got["c"] != 4 {
		t.Fatalf("shares = a:%d b:%d c:%d, want a:1 b:4 c:4", got["a"], got["b"], got["c"])
	}
}

// More groups than slots: the oldest ones get the single slots there are, the
// rest get zero — and nobody gets a fractional promise.
func TestSharesMoreGroupsThanSlots(t *testing.T) {
	got := shares([]GroupDemand{
		{Group: "a", Waiting: 1},
		{Group: "b", Waiting: 1},
		{Group: "c", Waiting: 1},
	}, 2)
	if got["a"] != 1 || got["b"] != 1 || got["c"] != 0 {
		t.Fatalf("shares = a:%d b:%d c:%d, want a:1 b:1 c:0", got["a"], got["b"], got["c"])
	}
}

// A group with no held and no waiting slot is not active and gets no entry —
// counting it would shrink everybody else's share for nothing.
func TestSharesIgnoresInactiveGroups(t *testing.T) {
	got := shares([]GroupDemand{
		{Group: "idle"},
		{Group: "busy", Waiting: 4},
	}, 4)
	if _, ok := got["idle"]; ok {
		t.Fatalf("inactive group got an entry: %v", got)
	}
	if got["busy"] != 4 {
		t.Fatalf("shares[busy] = %d, want 4", got["busy"])
	}
}

func TestSharesDisabledBudget(t *testing.T) {
	if got := shares([]GroupDemand{{Group: "a", Waiting: 3}}, 0); len(got) != 0 {
		t.Fatalf("budget 0 must hand out nothing, got %v", got)
	}
	if got := shares(nil, 9); len(got) != 0 {
		t.Fatalf("no groups must hand out nothing, got %v", got)
	}
}

func TestNextToServeEmptyQueue(t *testing.T) {
	if got := nextToServe(nil, map[string]int{}, map[string]int{}); got != -1 {
		t.Fatalf("nextToServe(empty) = %d, want -1", got)
	}
}

// The oldest waiter wins when its group is under its share.
func TestNextToServeOldestUnderShare(t *testing.T) {
	queue := []Waiter{{Group: "a"}, {Group: "b"}}
	held := map[string]int{"a": 0, "b": 0}
	share := map[string]int{"a": 4, "b": 5}
	if got := nextToServe(queue, held, share); got != 0 {
		t.Fatalf("nextToServe = %d, want 0 (a is oldest and under its share)", got)
	}
}

// A group at or above its share is skipped, and the next waiter under its
// own share is served instead. This is the redistribution of the design's
// convergence table: A holds 9 of a 9-slot budget with a share of 5, so its
// queued files wait while B's are served.
func TestNextToServeSkipsGroupAtItsShare(t *testing.T) {
	queue := []Waiter{{Group: "a"}, {Group: "a"}, {Group: "b"}}
	held := map[string]int{"a": 5, "b": 0}
	share := map[string]int{"a": 5, "b": 4}
	if got := nextToServe(queue, held, share); got != 2 {
		t.Fatalf("nextToServe = %d, want 2 (a is at its share, b is not)", got)
	}
}

// A free slot must never go unused: when every group is at or above its
// share, the oldest waiter is served whatever its group.
func TestNextToServeNeverWastesAFreeSlot(t *testing.T) {
	queue := []Waiter{{Group: "a"}, {Group: "b"}}
	held := map[string]int{"a": 5, "b": 4}
	share := map[string]int{"a": 5, "b": 4}
	if got := nextToServe(queue, held, share); got != 0 {
		t.Fatalf("nextToServe = %d, want 0 (fallback to the oldest waiter)", got)
	}
}

// Starvation freedom: a waiter that keeps being skipped becomes the oldest of
// its group, and repeated serving drives its group to its share, after which
// the FIFO reaches it. Two rounds are enough to show the queue draining in
// order rather than one group monopolising it.
func TestNextToServeIsStarvationFree(t *testing.T) {
	queue := []Waiter{{Group: "a"}, {Group: "b"}, {Group: "a"}}
	held := map[string]int{"a": 2, "b": 0}
	share := map[string]int{"a": 2, "b": 2}

	i := nextToServe(queue, held, share)
	if i != 1 {
		t.Fatalf("first pick = %d, want 1 (b is under its share)", i)
	}
	queue = append(queue[:i], queue[i+1:]...)
	held["b"]++

	i = nextToServe(queue, held, share)
	if i != 0 {
		t.Fatalf("second pick = %d, want 0 (b under share, oldest of what is left)", i)
	}
	queue = append(queue[:i], queue[i+1:]...)
	held["b"]++

	// Both groups are now at their share: the last waiter is served anyway.
	if i = nextToServe(queue, held, share); i != 0 {
		t.Fatalf("third pick = %d, want 0 (free slot must not be wasted)", i)
	}
	if queue[0].Group != "a" {
		t.Fatalf("last waiter group = %q, want a", queue[0].Group)
	}
}
