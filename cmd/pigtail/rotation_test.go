package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/testutil"
)

func TestRotationWarnings(t *testing.T) {
	t.Parallel()
	ch := claudeusage.Choice{Warnings: []string{"m's reading is 42m old — no fresh account under the margin"}}
	if got := rotationWarnings(ch, nil, "fresh session stays on ip"); len(got) != 1 || got[0] != "rotation: m's reading is 42m old — no fresh account under the margin" {
		t.Fatalf("pick warnings = %q", got)
	}
	err := fmt.Errorf("%w in scope perso (cell jp: not logged in)", claudeusage.ErrNoCandidate)
	got := rotationWarnings(claudeusage.Choice{}, err, "caller's cell ip")
	if len(got) != 1 || got[0] != "rotation: no candidate account in scope perso (cell jp: not logged in) — caller's cell ip" {
		t.Fatalf("error warnings = %q", got)
	}
	got = rotationWarnings(claudeusage.Choice{}, errRotationOff, "caller's cell ip")
	if len(got) != 1 || !strings.HasPrefix(got[0], "rotation: off") {
		t.Fatalf("off warnings = %q", got)
	}
}

func TestRotationOffPickCell(t *testing.T) {
	t.Setenv("PIGTAIL_ROTATION", "off")
	_, _, err := pickCell(&claudeprofile.Table{Cells: map[string]claudeprofile.Cell{}}, "perso", nil)
	if !errors.Is(err, errRotationOff) {
		t.Fatalf("err = %v, want errRotationOff", err)
	}
}

// staleChoice is a pick of m on a 42 min old reading, in the given scope.
func staleChoice(sc string) claudeusage.Choice {
	age := 42 * time.Minute
	return claudeusage.Choice{
		Candidate: claudeusage.Candidate{Label: "m", AccountUUID: "11111111-1111-4111-8111-111111111111",
			Cell: claudeusage.CellInput{ID: "mx", Scope: sc}, Group: claudeusage.GroupStale, Age: &age},
		Warnings: []string{"m's reading is 42m old — no fresh account under the margin"},
	}
}

func withBackground(t *testing.T, st *claudeusage.BackgroundState, err error) {
	t.Helper()
	testutil.SerialOnly(t) // swaps rotationBackground
	prevBG, prevNow := rotationBackground, rotationNow
	rotationBackground = func() (*claudeusage.BackgroundState, error) { return st, err }
	rotationNow = func() time.Time { return pickNowCLI }
	t.Cleanup(func() { rotationBackground, rotationNow = prevBG, prevNow })
}

var pickNowCLI = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

func failedM(cellScope string) *claudeusage.BackgroundState {
	at := pickNowCLI.Add(-12 * time.Minute)
	return &claudeusage.BackgroundState{At: at, ResultsAt: &at, Results: []claudeusage.BackgroundResult{
		{Label: "m", AccountUUID: "11111111-1111-4111-8111-111111111111", Cell: "mp", CellScope: cellScope,
			Status: claudeusage.RefreshFailed, Error: "timed out after 60s"},
	}}
}

// A12: the stale warning names the account's background refresh failure,
// with the same line as pigtail usage.
func TestRotationStaleWarningNamesBackgroundFailure(t *testing.T) {
	withBackground(t, failedM("perso"), nil)
	got := rotationWarnings(staleChoice("perso"), nil, "")
	want := "rotation: m's reading is 42m old — no fresh account under the margin; background refresh failed: m (cell mp) — timed out after 60s, 12m ago"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("warnings = %q, want %q", got, want)
	}
	// another account's failure is not cited
	other := failedM("perso")
	other.Results[0].AccountUUID = "22222222-2222-4222-8222-222222222222"
	withBackground(t, other, nil)
	if got := rotationWarnings(staleChoice("perso"), nil, ""); len(got) != 1 || strings.Contains(got[0], "background") {
		t.Fatalf("warnings = %q", got)
	}
	// an unreadable state is said, never read as "no failure"
	withBackground(t, nil, errors.New("parse usage-refresh.json: unexpected end of JSON input"))
	got = rotationWarnings(staleChoice("perso"), nil, "")
	if len(got) != 2 || got[1] != "rotation: background refresh state unreadable — parse usage-refresh.json: unexpected end of JSON input" {
		t.Fatalf("warnings = %q", got)
	}
}

// D19: a failure on a cell of another scope, picked for a non-master scope,
// names the account only; the same result picked in the master scope names
// the cell.
func TestRotationStaleWarningHiddenCell(t *testing.T) {
	withBackground(t, failedM("perso"), nil)
	got := rotationWarnings(staleChoice("globex"), nil, "")
	want := "rotation: m's reading is 42m old — no fresh account under the margin; background refresh failed: m — timed out after 60s, 12m ago"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("work warnings = %q, want %q", got, want)
	}
	if strings.Contains(got[0], "mp") {
		t.Fatalf("hidden cell named: %q", got[0])
	}
	got = rotationWarnings(staleChoice("perso"), nil, "")
	if len(got) != 1 || !strings.Contains(got[0], "background refresh failed: m (cell mp) — ") {
		t.Fatalf("master warnings = %q", got)
	}
}
