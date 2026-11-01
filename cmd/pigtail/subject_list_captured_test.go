package main

import (
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// TestSubjectListHidesCaptured: `subject list` hides captured subjects by
// default (done and parked stay listed); --status captured and --all show them.
func TestSubjectListHidesCaptured(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	now := time.UnixMilli(1).UTC()
	for id, status := range map[string]string{"sub_cap": pigtail.StatusCaptured, "sub_done": pigtail.StatusDone, "sub_park": pigtail.StatusParked} {
		if err := st.CreateSubject(&pigtail.Subject{ID: id, ThemeID: "thm_p", Title: id, Slug: id, Status: status, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	list := func(args ...string) string {
		t.Helper()
		out, err := runSubject(t, s, append([]string{"list"}, args...)...)
		if err != nil {
			t.Fatalf("subject list %v: %v", args, err)
		}
		return out
	}
	for _, args := range [][]string{{}, {"--json"}} {
		out := list(args...)
		if strings.Contains(out, "sub_cap") {
			t.Errorf("default list %v shows a captured subject: %s", args, out)
		}
		if !strings.Contains(out, "sub_done") || !strings.Contains(out, "sub_park") {
			t.Errorf("default list %v must keep done and parked: %s", args, out)
		}
	}
	for _, args := range [][]string{{"--status", "captured"}, {"--all"}, {"--all", "--json"}} {
		if out := list(args...); !strings.Contains(out, "sub_cap") {
			t.Errorf("list %v must show the captured subject: %s", args, out)
		}
	}
}
