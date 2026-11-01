package claudeusage

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/scope"
)

// ranked sets each cell's rank from a declaration order (the launcher config).
func ranked(cells []CellInput, order ...string) []CellInput {
	idx := map[string]int{}
	for i, id := range order {
		idx[id] = i
	}
	out := append([]CellInput(nil), cells...)
	for i := range out {
		if r, ok := idx[out[i].ID]; ok {
			out[i].Rank = &r
		}
	}
	return out
}

func labels(r Report) string {
	var ls []string
	for _, a := range r.Accounts {
		ls = append(ls, a.Label)
	}
	return strings.Join(ls, " ")
}

// an account is listed at its first declared cell — the launcher config order
// mt, mp, it, ip, jt, jp, t, p gives m, i, j, t in every view.
func TestBuildOrdersAccountsByRank(t *testing.T) {
	cells, files := researchCells(t)
	cells = ranked(cells, "mt", "mp", "it", "ip", "jt", "jp", "t", "p")
	for _, sc := range []scope.Scope{"perso", "globex"} {
		if got := labels(Build(snapshots(cells, files), sc, now0)); got != "m i j t" {
			t.Errorf("%s view: accounts = %q, want \"m i j t\"", sc, got)
		}
	}
}

// Unranked accounts go after the ranked ones, by label; equal ranks by label.
func TestBuildRankMixedAndTies(t *testing.T) {
	cells, files := researchCells(t)
	cells = ranked(cells, "t") // only t ranked (0): t first, then i j m by label
	if got := labels(Build(snapshots(cells, files), "perso", now0)); got != "t i j m" {
		t.Errorf("mixed: accounts = %q, want \"t i j m\"", got)
	}
	cells, _ = researchCells(t)
	one := 1
	for i := range cells {
		if cells[i].ID == "jt" || cells[i].ID == "it" {
			cells[i].Rank = &one // i and j tie at 1: label decides
		}
	}
	if got := labels(Build(snapshots(cells, files), "perso", now0)); got != "i j m t" {
		t.Errorf("tie: accounts = %q, want \"i j m t\"", got)
	}
}

// from globex, the smallest rank may sit on a hidden perso cell. The
// order still uses it (same order in every view), and nothing shows the rank
// or the hidden cell.
func TestBuildRankFromHiddenCellKeepsOrderSilently(t *testing.T) {
	cells, files := researchCells(t)
	// mp first (hidden from globex), mt last: visible ranks alone would give i j t m.
	cells = ranked(cells, "mp", "it", "jt", "t", "ip", "jp", "mt", "p")
	perso := labels(Build(snapshots(cells, files), "perso", now0))
	r := Build(snapshots(cells, files), "globex", now0)
	if got := labels(r); got != "m i j t" || perso != "m i j t" {
		t.Fatalf("globex %q perso %q, want \"m i j t\" in both", got, perso)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "rank") || persoIDs.Match(b) {
		t.Fatalf("globex JSON shows a rank or a hidden cell:\n%s", b)
	}
}
