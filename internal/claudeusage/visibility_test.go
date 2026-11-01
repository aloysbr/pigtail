package claudeusage

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

var persoIDs = regexp.MustCompile(`\b(ip|jp|mp|p)\b`)

func TestBuildHidesOtherScopes(t *testing.T) {
	cells, files := researchCells(t)
	files[fileOf(cells[2])] = files[fileOf(cells[2])][:100] // jp unreadable: must stay silent from globex
	cells[6].Account = "p"                                  // letter conflict involving a perso cell: silent too
	r := Build(snapshots(cells, files), "globex", now0)
	for label, want := range map[string]string{"i": "it", "j": "jt", "m": "mt", "t": "t"} {
		a := account(t, r, label)
		var ids []string
		for _, c := range a.Cells {
			ids = append(ids, c.ID)
			if c.Scope != "gx" {
				t.Errorf("%s: cell %s of scope %q shown to globex", label, c.ID, c.Scope)
			}
		}
		if got := strings.Join(ids, ","); got != want {
			t.Errorf("%s cells = %q, want %q", label, got, want)
		}
	}
	if j := account(t, r, "j"); j.Reading == nil || j.Reading.Cell != "jt" || j.Reading.SevenDay.Percent != 73 {
		t.Fatalf("j reading = %+v — jp is unreadable, so jt's 73 %%, jt named (visible)", j.Reading)
	}
	if m := account(t, r, "m"); m.Reading.Cell != "mt" {
		t.Fatalf("m reading cell = %q, want mt (visible)", m.Reading.Cell)
	}
	if len(r.Unreadable) != 0 || r.CellsRead != 7 {
		t.Fatalf("Unreadable %+v CellsRead %d, want none shown and 7 read", r.Unreadable, r.CellsRead)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "perso") || persoIDs.Match(b) {
		t.Fatalf("globex view leaks another scope:\n%s", b)
	}
}

// The reading may come from a cell the caller cannot see: % kept, cell omitted.
func TestBuildReadingFromHiddenCellKeepsPercent(t *testing.T) {
	cells, files := researchCells(t)
	j := account(t, Build(snapshots(cells, files), "globex", now0), "j")
	if j.Reading.Cell != "" || j.Reading.SevenDay.Percent != 91 {
		t.Fatalf("j reading = %+v, want jp's 91 %% without naming jp", j.Reading)
	}
}

// No visible cell, no letter: a neutral label, never a cell id.
func TestBuildNeutralLabelWithoutVisibleCell(t *testing.T) {
	c := cellAt("ip", "perso", "")
	files := map[string][]byte{fileOf(c): maxCell(t, cellFx{UUID: uuidI, Fetched: now0, P7: 10, R7: "2026-10-11T08:00:00+00:00", P5: 0, R5: "2026-10-05T14:00:00+00:00"})}
	r := Build(snapshots([]CellInput{c}, files), "globex", now0)
	a := account(t, r, "other-22222222")
	if len(a.Cells) != 0 || a.Reading.SevenDay.Percent != 10 {
		t.Fatalf("a = %+v", a)
	}
}

// No visible cell but letters: the FIRST letter by cell id labels the account
// (anyLetter branch), no cell is listed, the % are kept, and the hidden
// cells' disagreement is not reported.
func TestBuildLetterOfHiddenOnlyCell(t *testing.T) {
	c, c2 := cellAt("ip", "perso", "i"), cellAt("p", "perso", "z")
	fx := cellFx{UUID: uuidI, Fetched: now0, P7: 10, R7: "2026-10-11T08:00:00+00:00", P5: 0, R5: "2026-10-05T14:00:00+00:00"}
	files := map[string][]byte{fileOf(c): maxCell(t, fx), fileOf(c2): maxCell(t, fx)}
	r := Build(snapshots([]CellInput{c, c2}, files), "globex", now0)
	a := account(t, r, "i") // a "last letter found" rule would label it z
	if len(a.Cells) != 0 || a.LabelFromCells || a.Reading == nil || a.Reading.SevenDay.Percent != 10 {
		t.Fatalf("a = %+v, want label i, no cell, 10 %% kept", a)
	}
	for _, w := range r.Warnings {
		if strings.Contains(w, "disagree") {
			t.Fatalf("letter conflict of hidden cells reported from globex: %q", w)
		}
	}
}

// A visible cell's letter wins over a hidden cell read first, and the
// disagreement is not reported: it would name the hidden cell.
func TestBuildHiddenCellLetterNeverWins(t *testing.T) {
	ci, ct := cellAt("ip", "perso", "x"), cellAt("it", "gx", "i")
	fx := cellFx{UUID: uuidI, Fetched: now0, P7: 10, R7: "2026-10-11T08:00:00+00:00", P5: 0, R5: "2026-10-05T14:00:00+00:00"}
	files := map[string][]byte{fileOf(ci): maxCell(t, fx), fileOf(ct): maxCell(t, fx)}
	r := Build(snapshots([]CellInput{ci, ct}, files), "globex", now0)
	if len(r.Accounts) != 1 || r.Accounts[0].Label != "i" {
		t.Fatalf("accounts = %+v, want one account labelled i", r.Accounts)
	}
	for _, w := range r.Warnings {
		if strings.Contains(w, "disagree") {
			t.Fatalf("letter conflict reported from globex: %q", w)
		}
	}
}

// A visible cell without a letter: the label comes from the visible cell ids,
// never from a hidden cell's letter.
func TestBuildHiddenLetterNotUsedWhenVisibleUnlettered(t *testing.T) {
	ci, ct := cellAt("ip", "perso", "i"), cellAt("it", "gx", "")
	fx := cellFx{UUID: uuidI, Fetched: now0, P7: 10, R7: "2026-10-11T08:00:00+00:00", P5: 0, R5: "2026-10-05T14:00:00+00:00"}
	files := map[string][]byte{fileOf(ci): maxCell(t, fx), fileOf(ct): maxCell(t, fx)}
	r := Build(snapshots([]CellInput{ci, ct}, files), "globex", now0)
	if len(r.Accounts) != 1 || r.Accounts[0].Label != "it" || !r.Accounts[0].LabelFromCells {
		t.Fatalf("accounts = %+v, want one account labelled it, from cells", r.Accounts)
	}
	for _, w := range r.Warnings {
		if strings.Contains(w, "disagree") {
			t.Fatalf("letter conflict reported from globex: %q", w)
		}
	}
}

func TestBuildPersoSeesEverything(t *testing.T) {
	cells, files := researchCells(t)
	r := Build(snapshots(cells, files), "perso", now0)
	if got := len(account(t, r, "m").Cells); got != 3 || account(t, r, "j").Reading.Cell != "jp" {
		t.Fatalf("perso view must keep every cell: m has %d, j reading cell %q", got, account(t, r, "j").Reading.Cell)
	}
}
