package cmuxsync

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestOffTopicPill(t *testing.T) {
	t.Parallel()
	const (
		decl  = "AAAAAAAA-0000-0000-0000-0000000000F1"
		both  = "AAAAAAAA-0000-0000-0000-0000000000F2"
		plain = "AAAAAAAA-0000-0000-0000-0000000000F3"
	)
	ws := []Workspace{{ID: decl}, {ID: both}, {ID: plain}}
	in := Input{Now: time.Now(), Workspaces: ws,
		OffTopic: map[string]bool{decl: true, both: true, "AAAAAAAA-0000-0000-0000-0000000000F9": true},
		Bindings: []pigtail.SubjectCmuxBinding{{CmuxWorkspaceID: both, SubjectID: "sub_x"}}}
	res := Compute(in)
	if !res.Desired[decl].OffTopic || res.Desired[both].OffTopic || res.Desired[plain].OffTopic {
		t.Fatalf("pills: decl=%v both=%v plain=%v", res.Desired[decl].OffTopic, res.Desired[both].OffTopic, res.Desired[plain].OffTopic)
	}
	if _, ok := res.Desired["AAAAAAAA-0000-0000-0000-0000000000F9"]; ok {
		t.Fatal("pill on a closed workspace")
	}
	e, ok := res.Desired[decl].Statuses()[KeyOffTopic]
	// D10–D14 pinned by literal value, not by the constants themselves.
	if !ok || e.Value != "Off-topic" || e.Icon != "circle.slash" || e.Color != "#8E8E93" || e.Priority != 50 {
		t.Fatalf("entry = %+v, want Off-topic / circle.slash / #8E8E93 / 50 (D10–D13)", e)
	}
	if e.Priority >= serversPriority || e.Priority >= maintenancePriority || e.Priority >= attentionPriority || e.Priority >= 70 {
		t.Fatalf("priority %d not under servers, inbox (70), maintenance, attention (D13)", e.Priority)
	}
	if !Owns(KeyOffTopic) {
		t.Fatal("key not owned: Apply would refuse it")
	}
	applied := map[string]Applied{}
	for _, op := range Diff(res.Desired, applied) {
		Record(applied, op)
	}
	lifted := Compute(Input{Now: time.Now(), Workspaces: ws})
	clears := 0
	for _, op := range Diff(lifted.Desired, applied) {
		if op.Kind == OpClearStatus && op.Status.Key == KeyOffTopic {
			clears++
		}
	}
	if clears != 1 {
		t.Fatalf("clears = %d", clears)
	}
}
