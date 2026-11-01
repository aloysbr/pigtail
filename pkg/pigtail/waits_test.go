package pigtail

import "testing"

// TestNormalizeEnvelope: an absent kind or schema takes the wait's type and
// version 1; a value already set is never overwritten (D28 c).
func TestNormalizeEnvelope(t *testing.T) {
	var d WaitDetails
	d.Normalize("arbitrage")
	if d.Kind != "arbitrage" || d.Schema != WaitDetailsSchema {
		t.Fatalf("details = %q/%d", d.Kind, d.Schema)
	}
	var a WaitAnswer
	a.Normalize("review")
	if a.Kind != "review" || a.Schema != WaitAnswerSchema {
		t.Fatalf("answer = %q/%d", a.Kind, a.Schema)
	}
	d = WaitDetails{Kind: "question", Schema: 2}
	d.Normalize("arbitrage")
	if d.Kind != "question" || d.Schema != 2 {
		t.Fatalf("details overwritten: %q/%d", d.Kind, d.Schema)
	}
	a = WaitAnswer{Kind: "question", Schema: 3}
	a.Normalize("review")
	if a.Kind != "question" || a.Schema != 3 {
		t.Fatalf("answer overwritten: %q/%d", a.Kind, a.Schema)
	}
}
