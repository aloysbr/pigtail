package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func runCapture(t *testing.T, s *stubDaemon, args ...string) (string, error) {
	t.Helper()
	return runCobra(t, s, captureCmd, args...)
}

// The help exposes --theme like `subject`, and says a capture is a backlog
// idea, not a screenshot.
func TestCaptureCLIHelp(t *testing.T) {
	cmd := captureCmd()
	if !strings.Contains(strings.ToLower(cmd.Long), "not a screenshot") {
		t.Fatalf("Long = %q, must say a capture is not a screenshot", cmd.Long)
	}
	for _, name := range []string{"add", "list"} {
		sub, _, err := cmd.Find([]string{name})
		if err != nil || sub.Flags().Lookup("theme") == nil {
			t.Fatalf("capture %s lacks --theme: %v", name, err)
		}
	}
	for _, name := range []string{"add", "promote", "delete"} {
		sub, _, err := cmd.Find([]string{name})
		if err != nil || sub.Flags().Lookup("cross-scope") == nil {
			t.Fatalf("capture %s lacks --cross-scope: %v", name, err)
		}
	}
	if sub, _, _ := cmd.Find([]string{"add"}); sub.Flags().Lookup("from-subject") == nil {
		t.Fatal("capture add lacks --from-subject")
	}
	if sub, _, _ := cmd.Find([]string{"promote"}); sub.Flags().Lookup("title") == nil || sub.Flags().Lookup("short-title") == nil {
		t.Fatal("capture promote lacks --title/--short-title")
	}
	root := newRootCmd()
	if c, _, err := root.Find([]string{"capture", "add"}); err != nil || c.Name() != "add" {
		t.Fatalf("capture is not registered on the root: %v", err)
	}
}

func TestCaptureCLIRoundTrip(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)

	out, err := runCapture(t, s, "add", "--theme", "thm_p", "--text", "an idea", "--json")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	var cp pigtail.Capture
	if err := json.Unmarshal([]byte(out), &cp); err != nil || !strings.HasPrefix(cp.ID, "cap_") {
		t.Fatalf("add output = %q (%v)", out, err)
	}
	if out, err = runCapture(t, s, "list", "--theme", "thm_p", "--open"); err != nil || !strings.Contains(out, cp.ID) || !strings.Contains(out, "an idea") {
		t.Fatalf("list = %q, %v", out, err)
	}
	if _, err := runCapture(t, s, "add", "--theme", "thm_p", "--text", "x", "--from-subject", "sub_p"); err != nil {
		t.Fatalf("add --from-subject: %v", err)
	}
	if sub, _ := st.GetSubject("sub_p"); sub.Status != pigtail.StatusCaptured {
		t.Fatalf("sub_p status = %q, want captured", sub.Status)
	}
	if out, err = runCapture(t, s, "promote", cp.ID, "--title", "Promoted one"); err != nil || !strings.Contains(out, "promoted 1 capture") {
		t.Fatalf("promote = %q, %v", out, err)
	}
	// a work theme from the perso master: --cross-scope is required
	_, err = runCapture(t, s, "add", "--theme", "thm_t", "--text", "x")
	wantScopeMismatch(t, "capture add on a work theme", err)
	if _, err := runCapture(t, s, "add", "--theme", "thm_t", "--text", "x", "--cross-scope"); err != nil {
		t.Fatalf("add --cross-scope: %v", err)
	}
}
