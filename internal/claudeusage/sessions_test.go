package claudeusage

import (
	"strconv"
	"testing"
)

// researchSessions is the session table of 2026-10-05 morning, as the daemon
// returns it to a perso caller: i 1/2, j 1/5, m 1/8, t 0/9.
func researchSessions() []SessionRef {
	var ss []SessionRef
	add := func(cell string, n, busy int) {
		for k := 0; k < n; k++ {
			ss = append(ss, SessionRef{ID: cell + "-" + strconv.Itoa(k), CellID: cell, Open: true, Busy: k < busy})
		}
	}
	add("ip", 1, 1)
	add("it", 1, 0)
	add("jp", 3, 1)
	add("jt", 2, 0)
	add("mp", 3, 0)
	add("mt", 4, 1)
	add("p", 1, 0)
	add("t", 9, 0)
	return ss
}

func counts(t *testing.T, a Account) string {
	t.Helper()
	if a.SessionsBusy == nil || a.SessionsOpen == nil {
		return "null"
	}
	return strconv.Itoa(*a.SessionsBusy) + "/" + strconv.Itoa(*a.SessionsOpen)
}

func TestAttachSessionsCountsPerAccount(t *testing.T) {
	cells, files := researchCells(t)
	r := Build(snapshots(cells, files), "perso", now0)
	r.AttachSessions(researchSessions(), true)
	for label, want := range map[string]string{"i": "1/2", "j": "1/5", "m": "1/8", "t": "0/9"} {
		if got := counts(t, account(t, r, label)); got != want {
			t.Errorf("%s: sessions = %s, want %s", label, got, want)
		}
	}
	if !r.SessionsAvailable || !r.SessionsComplete {
		t.Fatalf("perso view: available %v complete %v, want true true", r.SessionsAvailable, r.SessionsComplete)
	}
}

// A session present twice (same id: a copy left in another account) counts
// once, on the cell of its first row — its own pane's cell.
func TestAttachSessionsDuplicateCountedOnce(t *testing.T) {
	cells, files := researchCells(t)
	r := Build(snapshots(cells, files), "perso", now0)
	r.AttachSessions([]SessionRef{
		{ID: "0f072977", CellID: "jp", Open: true, Busy: true},
		{ID: "0f072977", CellID: "mp", Open: true, Busy: true},
	}, true)
	if got := counts(t, account(t, r, "j")); got != "1/1" {
		t.Errorf("j = %s, want 1/1", got)
	}
	if got := counts(t, account(t, r, "m")); got != "0/0" {
		t.Errorf("m = %s, want 0/0 (the duplicate is not counted twice)", got)
	}
}

func TestAttachSessionsSkipsClosedAndUnknownCells(t *testing.T) {
	cells, files := researchCells(t)
	r := Build(snapshots(cells, files), "perso", now0)
	r.AttachSessions([]SessionRef{
		{ID: "a", CellID: "jp", Open: false, Busy: true}, // ended or dead
		{ID: "b", CellID: "work", Open: true},            // a cell no account owns
		{ID: "c", CellID: "", Open: true},                // cell unknown to the snapshot
		{ID: "d", CellID: "jp", Open: true},
	}, true)
	if got := counts(t, account(t, r, "j")); got != "0/1" {
		t.Errorf("j = %s, want 0/1", got)
	}
}

func TestAttachSessionsUnavailable(t *testing.T) {
	cells, files := researchCells(t)
	r := Build(snapshots(cells, files), "perso", now0)
	r.AttachSessions(nil, false)
	if r.SessionsAvailable || r.SessionsComplete {
		t.Fatalf("available %v complete %v, want false false", r.SessionsAvailable, r.SessionsComplete)
	}
	for _, a := range r.Accounts {
		if got := counts(t, a); got != "null" {
			t.Errorf("%s = %s, want null (unknown, never 0/0)", a.Label, got)
		}
	}
}

// Not perso: the daemon returned only what the caller may see — say so.
func TestAttachSessionsPartialOutsidePerso(t *testing.T) {
	cells, files := researchCells(t)
	r := Build(snapshots(cells, files), "globex", now0)
	r.AttachSessions(researchSessions(), true)
	if !r.SessionsAvailable || r.SessionsComplete {
		t.Fatalf("non-perso view: available %v complete %v, want true false", r.SessionsAvailable, r.SessionsComplete)
	}
}
