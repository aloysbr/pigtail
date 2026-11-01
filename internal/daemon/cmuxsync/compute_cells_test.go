package cmuxsync

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func cellSess(id, cell string, started time.Duration) Session {
	s := sess(id, wsA, "S1", "busy", true)
	s.CellID, s.StartedAt = cell, t0.Add(started)
	return s
}

func boundInput(ss ...Session) Input {
	b := pigtail.SubjectCmuxBinding{SubjectID: "sub1", CmuxWorkspaceID: wsA}
	sv := SubjectView{Subject: pigtail.Subject{ID: "sub1", Scope: "perso", Status: "building", Rank: 2}, ThemeName: "pigtail"}
	return Input{Now: t0, Workspaces: []Workspace{live(wsA)}, Sessions: ss, Bindings: []pigtail.SubjectCmuxBinding{b}, Subjects: map[string]SubjectView{"sub1": sv}}
}

func TestBoundDescriptionEndsWithCells(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ss   []Session
		want string
	}{
		{"D1 one cell", []Session{cellSess("a", "mp", 0)}, "pigtail · building · #2 · mp"},
		{"D1 two cells, arrival order", []Session{cellSess("b", "jt", time.Minute), cellSess("a", "mp", 0)}, "pigtail · building · #2 · mp+jt"},
		{"D1 one cell twice", []Session{cellSess("a", "mp", 0), cellSess("b", "mp", time.Minute)}, "pigtail · building · #2 · mp"},
		{"D7 no cell", []Session{cellSess("a", "", 0)}, "pigtail · building · #2"},
		{"dead session not counted", []Session{cellSess("a", "mp", 0), func() Session { s := cellSess("b", "jt", 0); s.Alive = false; return s }()}, "pigtail · building · #2 · mp"},
		{"ended session not counted", []Session{cellSess("a", "mp", 0), func() Session { s := cellSess("b", "jt", 0); s.Ended = true; return s }()}, "pigtail · building · #2 · mp"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Compute(boundInput(c.ss...)).Desired[wsA].Description; got != c.want {
				t.Fatalf("description = %q, want %q", got, c.want)
			}
		})
	}
}

// Stable between two passes, whatever order the registry lists the sessions in.
func TestCellOrderStable(t *testing.T) {
	t.Parallel()
	a, b := cellSess("a", "mp", 0), cellSess("b", "jt", time.Minute)
	one := Compute(boundInput(a, b)).Desired[wsA].Description
	two := Compute(boundInput(b, a)).Desired[wsA].Description
	if one != two || one != "pigtail · building · #2 · mp+jt" {
		t.Fatalf("%q then %q", one, two)
	}
}

func TestBoundDescriptionOwnedByUserUntouched(t *testing.T) {
	t.Parallel()
	in := boundInput(cellSess("a", "mp", 0))
	in.Bindings[0].DescriptionOwner = pigtail.CmuxOwnerUser
	if got := Compute(in).Desired[wsA].Description; got != "" {
		t.Fatalf("description = %q, want untouched", got)
	}
}

func unboundInput(desc string, ss ...Session) Input {
	w := live(wsA)
	w.Description = desc
	return Input{Now: t0, Workspaces: []Workspace{w}, Sessions: ss, KnownCells: map[string]bool{"mp": true, "jt": true, "mt": true}}
}

func TestUnboundDescriptionIsTheCell(t *testing.T) {
	t.Parallel()
	d := Compute(unboundInput("", cellSess("a", "mt", 0))).Desired[wsA]
	if d.Description != "mt" || d.ClearDescription {
		t.Fatalf("%+v", d)
	}
	if d := Compute(unboundInput("", cellSess("a", "", 0))).Desired[wsA]; d.Description != "" || d.ClearDescription {
		t.Fatalf("D7: a bare claude adds nothing: %+v", d)
	}
}

// Review Focus 4: a description written by hand on an unbound workspace wins.
func TestUnboundDescriptionHandWrittenWins(t *testing.T) {
	t.Parallel()
	d := Compute(unboundInput("mes notes", cellSess("a", "mt", 0))).Desired[wsA]
	if d.Description != "" || d.ClearDescription {
		t.Fatalf("hand-written description touched: %+v", d)
	}
}

// Review Focus 4: after a daemon restart (CellText empty) "mp+jt" is still
// recognised as pigtail's, updated, and cleared when no cell is left.
func TestUnboundCellTextRecognisedAfterRestart(t *testing.T) {
	t.Parallel()
	if d := Compute(unboundInput("mp+jt", cellSess("a", "mp", 0))).Desired[wsA]; d.Description != "mp" {
		t.Fatalf("not updated: %+v", d)
	}
	if d := Compute(unboundInput("mp+jt")).Desired[wsA]; !d.ClearDescription {
		t.Fatalf("not cleared once no session is left: %+v", d)
	}
	if d := Compute(unboundInput("mp+zz", cellSess("a", "mp", 0))).Desired[wsA]; d.Description != "" || d.ClearDescription {
		t.Fatalf("an unknown id is not a cell text: %+v", d)
	}
}

func TestDiffClearsAPosedCellDescription(t *testing.T) {
	t.Parallel()
	desired := map[string]Desired{wsA: {WorkspaceID: wsA, ClearDescription: true}}
	ops := Diff(desired, map[string]Applied{wsA: {Description: "mt"}})
	if len(ops) != 1 || ops[0].Kind != OpClearDescription {
		t.Fatalf("ops = %+v", ops)
	}
	if ops := Diff(desired, map[string]Applied{}); len(ops) != 0 {
		t.Fatalf("nothing posed, nothing to clear: %+v", ops)
	}
}
