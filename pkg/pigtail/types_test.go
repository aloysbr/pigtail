package pigtail

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestDispatchRequestPlanFields(t *testing.T) {
	pf := true
	req := DispatchRequest{
		ProjectID:    "prj_x",
		Intent:       "do thing",
		PlanContent:  "# plan\nstep 1",
		PlanFilename: "plan.md",
		PlanFirst:    &pf,
		Force:        true,
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var back DispatchRequest
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.PlanContent != "# plan\nstep 1" || back.PlanFilename != "plan.md" {
		t.Fatalf("plan fields lost: %+v", back)
	}
	if back.PlanFirst == nil || *back.PlanFirst != true {
		t.Fatalf("PlanFirst not preserved: %+v", back.PlanFirst)
	}
	if !back.Force {
		t.Fatalf("Force not preserved")
	}
}

func TestNewWorkspaceRequestRoundTrip(t *testing.T) {
	req := NewWorkspaceRequest{ProjectID: "prj_x", Intent: "do thing", BranchOverride: "feat/foo"}
	b, _ := json.Marshal(req)
	var back NewWorkspaceRequest
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.BranchOverride != "feat/foo" {
		t.Fatalf("round trip failed: %+v", back)
	}
}

func TestSubjectStatusConstants(t *testing.T) {
	// Guards against typos and accidental renames of the status vocabulary.
	want := []string{"inbox", "understanding", "designing", "building", "done", "parked", "dropped", "captured"}
	if len(SubjectStatuses) != len(want) {
		t.Fatalf("SubjectStatuses = %q, want %q", SubjectStatuses, want)
	}
	for i := range want {
		if SubjectStatuses[i] != want[i] {
			t.Errorf("SubjectStatuses[%d] = %q, want %q", i, SubjectStatuses[i], want[i])
		}
	}
	consts := []string{
		StatusInbox, StatusUnderstanding, StatusDesigning, StatusBuilding,
		StatusDone, StatusParked, StatusDropped, StatusCaptured,
	}
	for i := range want {
		if consts[i] != want[i] {
			t.Errorf("constant[%d] = %q, want %q", i, consts[i], want[i])
		}
	}
}

func TestValidateSubjectStatus(t *testing.T) {
	for _, s := range SubjectStatuses {
		if err := ValidateSubjectStatus(s); err != nil {
			t.Errorf("ValidateSubjectStatus(%q) = %v, want nil", s, err)
		}
	}
	for _, s := range []string{"researching", ""} {
		err := ValidateSubjectStatus(s)
		want := `unknown status "` + s + `": use one of inbox, understanding, designing, building, done, parked, dropped, captured`
		if err == nil || err.Error() != want {
			t.Errorf("ValidateSubjectStatus(%q) = %v, want %q", s, err, want)
		}
	}
}

func TestIsClosedStatusDropped(t *testing.T) {
	if !IsClosedStatus("dropped") {
		t.Error(`IsClosedStatus("dropped") = false, want true`)
	}
	if IsClosedStatus("designing") {
		t.Error(`IsClosedStatus("designing") = true, want false`)
	}
}

func TestSubjectJSONRoundTrip(t *testing.T) {
	s := Subject{
		ID: "sub_1", ThemeID: "thm_1", Title: "checklist style", Slug: "checklist-style",
		Status: StatusInbox, Impact: 4, Effort: 2, Urgency: 3, Rank: 1,
		Deps: []string{"sub_0"}, VaultPath: "Globex/Projects/rain-gauge/subjects/checklist-style",
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Subject
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Title != s.Title || back.Impact != 4 || len(back.Deps) != 1 || back.Deps[0] != "sub_0" {
		t.Fatalf("round trip mismatch: %+v", back)
	}
}

func TestSatisfiesDepsDropped(t *testing.T) {
	if !SatisfiesDeps("dropped") {
		t.Error(`SatisfiesDeps("dropped") = false, want true`)
	}
	if SatisfiesDeps("parked") {
		t.Error(`SatisfiesDeps("parked") = true, want false`)
	}
}

// TestLegacyStatusConstantsGone: the four pre-existing status constants are
// gone from types.go once the board stopped reading them.
func TestLegacyStatusConstantsGone(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "types.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	gone := map[string]bool{"StatusTriaged": true, "StatusResearching": true, "StatusSpec": true, "StatusReview": true}
	seen := 0
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, sp := range g.Specs {
			for _, n := range sp.(*ast.ValueSpec).Names {
				seen++
				if gone[n.Name] {
					t.Errorf("constant %s still declared in types.go", n.Name)
				}
			}
		}
	}
	if seen == 0 { // positive control: the parser read the constants
		t.Fatal("no constant read from types.go")
	}
}
