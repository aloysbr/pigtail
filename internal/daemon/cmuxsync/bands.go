package cmuxsync

import (
	"slices"
	"sort"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Maturity bands (D2, D11, D14–D17 of sub_x000000000000014). In a banded
// folder (an area folder or « Inbox », never « Standby » nor a focus, D14):
// the pinned tabs keep cmux's own order (D3, D17), then bands 0..3, then the
// tabs bound to a closed subject (D15). A tab with no subject keeps its slot,
// even placed by hand above a band (D15, D20). Inside a band the current
// order holds; a tab that enters (pigtail's add, a band change, an unpin)
// goes to the bottom of its band (D16).

// Bands, in folder order.
const (
	BandBuilding = iota
	BandDesigning
	BandUnderstanding
	BandInbox
)

// BandOf is the band of a subject status; ok is false for a closed status
// (done, parked, dropped, captured) and any other word.
func BandOf(status string) (band int, ok bool) {
	switch status {
	case pigtail.StatusBuilding:
		return BandBuilding, true
	case pigtail.StatusDesigning:
		return BandDesigning, true
	case pigtail.StatusUnderstanding:
		return BandUnderstanding, true
	case pigtail.StatusInbox:
		return BandInbox, true
	}
	return 0, false
}

// blockMembers is the wanted order of folder's members ms (anchor excluded).
// A Banded folder: pinned and unbound tabs keep their slot (an
// unbound one that enters goes to the bottom of the folder, as tailOrder
// did, unsorted only); the bound tabs fill the other slots, bands 0..3 then
// the bound tabs with no band, each group in its current order with its
// entering tabs last — sorted (D11): stable by Last then gravity instead. A
// folder not Banded keeps the former rule: unsorted, entering tabs at the
// bottom; sorted, pinned in their slot and the others by Last then gravity;
// Unsorted (Standby): the current order, sorted; unsorted, entering tabs last.
func blockMembers(in ArrangeInput, folder string, ms []string, sorted bool) []string {
	if in.Unsorted[folder] {
		if sorted {
			return ms
		}
		return enterLast(in, ms) // NoSort: an entering tab goes to the bottom, no band
	}
	if !in.Banded[folder] {
		if sorted {
			return fillSlots(ms, func(m string) bool { return in.Pinned[m] }, sortByGravity(in, ms))
		}
		return enterLast(in, ms)
	}
	var tail []string // unsorted: an unbound tab that enters, to the bottom of the folder
	if !sorted {
		rest := ms[:0:0]
		for _, m := range ms {
			if in.Enter[m] && !in.Bound[m] && !in.Pinned[m] {
				tail = append(tail, m)
			} else {
				rest = append(rest, m)
			}
		}
		ms = rest
	}
	// Hold (D40) holds free workspaces only, never a folder member: a young
	// tab born in a folder takes its band like the others.
	fixed := func(m string) bool { return in.Pinned[m] || !in.Bound[m] }
	groups := make([][]string, BandInbox+2) // bands 0..3, then bound with no band
	for _, m := range ms {
		if fixed(m) {
			continue
		}
		i := BandInbox + 1
		if b, ok := in.Band[m]; ok && b >= BandBuilding && b <= BandInbox {
			i = b
		}
		groups[i] = append(groups[i], m)
	}
	var moving []string
	for _, gr := range groups {
		if sorted {
			moving = append(moving, sortByGravity(in, gr)...)
		} else {
			moving = append(moving, enterLast(in, gr)...)
		}
	}
	return append(fillSlots(ms, fixed, moving), tail...)
}

// fillSlots keeps each fixed member in its slot and puts moving, in order, in
// the other slots.
func fillSlots(ms []string, fixed func(string) bool, moving []string) []string {
	out := slices.Clone(ms)
	k := 0
	for i, m := range ms {
		if !fixed(m) {
			out[i] = moving[k]
			k++
		}
	}
	return out
}

// sortByGravity is the non-pinned members of ms, stable by Last (off-topic
// after the others) then gravity rank.
func sortByGravity(in ArrangeInput, ms []string) []string {
	rank := func(ws string) int {
		if r, ok := in.Rank[ws]; ok {
			return r
		}
		return int(^uint(0) >> 1)
	}
	free := slices.DeleteFunc(slices.Clone(ms), func(m string) bool { return in.Pinned[m] })
	sort.SliceStable(free, func(i, j int) bool {
		if in.Last[free[i]] != in.Last[free[j]] {
			return !in.Last[free[i]]
		}
		return rank(free[i]) < rank(free[j])
	})
	return free
}

// enterLast is ms with its entering members (never a pinned one, D13) last,
// their relative order kept.
func enterLast(in ArrangeInput, ms []string) []string {
	entering := func(m string) bool { return in.Enter[m] && !in.Pinned[m] }
	out := slices.DeleteFunc(slices.Clone(ms), entering)
	for _, m := range ms {
		if entering(m) {
			out = append(out, m)
		}
	}
	return out
}

// noSortPlaces is the arrangement without the sort (D5-A of
// sub_x000000000000030, D39 of sub_x000000000000014): for each own folder,
// its members (display order, anchor excluded) take blockMembers' unsorted
// order, reached by moving one tab at a time — never a whole-window reorder,
// which cmux would render with the folder holding a pinned member lifted
// (cmux probe P26 a). The tabs kept are the longest subsequence of the current
// order already in the wanted order that holds every fixed tab (pinned; in a
// banded folder, unbound and not entering) and the first member when it
// stays first; ties keep the most tabs that do not enter, then the earliest.
// Each other tab, in wanted order, goes right after its predecessor in the
// wanted order (already in place, P26 b); the first one, after the last
// pinned tab of the folder, else after the anchor — never above a pinned one
// (P24 c2, P26 c). nil when every folder is in order.
func noSortPlaces(in ArrangeInput, own map[string]*cmux.Group) []FolderOp {
	var ops []FolderOp
	for _, f := range in.Folders {
		gr := own[f]
		if gr == nil {
			continue
		}
		var ms []string
		for _, ws := range in.Order {
			if ws != gr.AnchorID && slices.Contains(gr.Members, ws) {
				ms = append(ms, ws)
			}
		}
		want := blockMembers(in, f, ms, false)
		if slices.Equal(ms, want) {
			continue
		}
		banded := in.Banded[f] && !in.Unsorted[f]
		must := func(i int) bool {
			m := ms[i]
			return in.Pinned[m] || (banded && !in.Bound[m] && !in.Enter[m]) || (i == 0 && want[0] == m)
		}
		kept := keptInOrder(ms, want, must, func(m string) bool { return in.Enter[m] })
		head := gr.AnchorID
		for _, m := range ms {
			if in.Pinned[m] {
				head = m // the last pinned tab of the block (cmux holds them on top, P24 a, e)
			}
		}
		for i, m := range want {
			if kept[m] {
				continue
			}
			ref := head
			if i > 0 {
				ref = want[i-1]
			}
			ops = append(ops, FolderOp{Kind: FolderPlace, Workspace: m, Ref: ref})
		}
	}
	return ops
}

// keptInOrder is the set of ms kept in place: the longest subsequence of ms
// whose order agrees with want and that holds every must(i); ties keep the
// most members that do not enter, then the earliest in ms. ms and want hold
// the same members.
func keptInOrder(ms, want []string, must func(int) bool, enter func(string) bool) map[string]bool {
	pos := make(map[string]int, len(want))
	for i, m := range want {
		pos[m] = i
	}
	type chain struct {
		idx    []int // indices in ms
		n, out int   // length, members that do not enter
	}
	better := func(a, b chain) bool { // a strictly better than b
		if a.n != b.n {
			return a.n > b.n
		}
		if a.out != b.out {
			return a.out > b.out
		}
		for k := range a.idx {
			if a.idx[k] != b.idx[k] {
				return a.idx[k] < b.idx[k]
			}
		}
		return false
	}
	mustBetween := func(lo, hi int) bool { // a must index strictly between lo and hi
		for k := lo + 1; k < hi; k++ {
			if must(k) {
				return true
			}
		}
		return false
	}
	best := make([]*chain, len(ms)) // best valid chain ending at k
	var top *chain
	for k := range ms {
		var c *chain
		if !mustBetween(-1, k) {
			c = &chain{idx: []int{k}, n: 1}
		}
		for j := 0; j < k; j++ {
			if best[j] == nil || pos[ms[j]] > pos[ms[k]] || mustBetween(j, k) {
				continue
			}
			ext := chain{idx: append(slices.Clone(best[j].idx), k), n: best[j].n + 1, out: best[j].out}
			if c == nil || better(ext, *c) {
				c = &ext
			}
		}
		if c != nil && !enter(ms[k]) {
			c.out++
		}
		best[k] = c
		if c != nil && !mustBetween(k, len(ms)) && (top == nil || better(*c, *top)) {
			top = c
		}
	}
	kept := map[string]bool{}
	if top != nil {
		for _, i := range top.idx {
			kept[ms[i]] = true
		}
	}
	return kept
}
