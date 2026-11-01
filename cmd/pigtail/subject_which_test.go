package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestSubjectWhich(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	t.Setenv(cmux.EnvWorkspaceID, attachWS)
	if _, err := runSubject(t, s, "which"); err == nil || !strings.Contains(err.Error(), "no subject bound to cmux workspace "+attachWS) {
		t.Fatalf("unbound = %v", err)
	}
	if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: attachWS, SubjectID: "sub_p", Source: "attach", BoundAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	out, err := runSubject(t, s, "which", "--json")
	if err != nil || !strings.Contains(out, `"subject_id":"sub_p"`) || !strings.Contains(out, `"scope":"perso"`) {
		t.Fatalf("bound --json = %q %v", out, err)
	}
	if out, err := runSubject(t, s, "which"); err != nil || !strings.HasPrefix(out, "sub_p\t") {
		t.Fatalf("bound = %q %v", out, err)
	}
	// A work cell learns a link exists (D25: bound-elsewhere), never whose.
	withCells(t, func(work, _ string) string { return work })
	if out, err := runSubject(t, s, "which"); err == nil || out != "" ||
		err.Error() != "no subject bound to cmux workspace "+attachWS+" (bound to a subject this scope cannot read)" {
		t.Fatalf("work cell on a perso binding = %q %v", out, err)
	}
	out, err = runSubject(t, s, "which", "--json")
	if err == nil || !strings.Contains(out, `"state":"bound-elsewhere"`) || strings.Contains(out, "sub_p") || strings.Contains(out, "perso") {
		t.Fatalf("work cell on a perso binding --json = %q %v", out, err)
	}
}

func TestSubjectWhichStates(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	t.Setenv(cmux.EnvWorkspaceID, attachWS)
	// declare below looks up the calling Claude session: stub it, as TestSubjectOffTopicCLI does,
	// so the test never reads the real process tree or ~/.claude*/sessions.
	savedClaude, savedSession := spawnCallerClaude, spawnCallerSession
	t.Cleanup(func() { spawnCallerClaude, spawnCallerSession = savedClaude, savedSession })
	spawnCallerClaude = func() (claudesession.Proc, bool, error) { return claudesession.Proc{PID: 4242}, true, nil }
	spawnCallerSession = func(string, int) (string, bool) { return "sess-which", true }

	out, err := runSubject(t, s, "which", "--json")
	if err == nil || !strings.Contains(out, `"state":"unbound"`) || !strings.Contains(err.Error(), "no subject bound to cmux workspace "+attachWS) {
		t.Fatalf("unbound --json = %q %v", out, err)
	}
	if strings.Contains(err.Error(), "(") {
		t.Fatalf("unbound message changed: %v", err)
	}
	if _, err := runSubject(t, s, "off-topic", "declare", "--reason", "question", "--space", "Inbox"); err != nil {
		t.Fatal(err)
	}
	out, err = runSubject(t, s, "which", "--json")
	if err == nil || !strings.Contains(out, `"state":"off-topic"`) || !strings.Contains(err.Error(), "no subject bound to cmux workspace "+attachWS+" (declared off-topic)") {
		t.Fatalf("off-topic = %q %v", out, err)
	}
	// S8: the caller reads its own (perso) row, so --json carries the reason and the declaration time.
	if !strings.Contains(out, `"reason":"question"`) || !strings.Contains(out, `"declared_at":`) {
		t.Fatalf("off-topic --json lacks reason/declared_at = %q", out)
	}
	if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: attachWS, SubjectID: "sub_p", Source: "attach", BoundAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	out, err = runSubject(t, s, "which", "--json")
	if err != nil || !strings.Contains(out, `"state":"bound"`) || !strings.Contains(out, `"subject_id":"sub_p"`) {
		t.Fatalf("bound = %q %v", out, err)
	}
}

func TestWhichStateUnknownOnOldDaemon(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r) // what chi answers for a route it does not serve: "404 page not found\n", text/plain
	}))
	defer srv.Close()
	c := pigtail.NewHTTPClient(strings.TrimPrefix(srv.URL, "http://"), "")
	st := whichState(context.Background(), c, attachWS)
	b, err := json.Marshal(st)
	if err != nil || !strings.Contains(string(b), `"state":"unknown"`) || !strings.Contains(string(b), `"workspace_id":"`+attachWS+`"`) {
		t.Fatalf("json = %s %v", b, err)
	}
	msg := "no subject bound to cmux workspace " + attachWS + whichSuffix(st)
	if !strings.Contains(msg, " (state unknown: ") || !strings.Contains(msg, "404") || strings.Contains(msg, "\n") {
		t.Fatalf("message = %q, want one line with (state unknown: … 404 …)", msg)
	}
}

// TestSubjectWhichBoundAfterLookup404 (D24-7): a link posed between the lookup (404) and the state read prints
// "state":"bound" with exit 1; the caller retries, the shell hook reads bound as silence.
func TestSubjectWhichBoundAfterLookup404(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/state") {
			envelopeResponder{}.JSON(w, r, http.StatusOK, pigtail.CmuxWorkspaceState{WorkspaceID: attachWS, State: pigtail.WorkspaceStateBound})
			return
		}
		envelopeResponder{}.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no subject bound to this cmux workspace", nil)
	}))
	defer srv.Close()
	t.Setenv(cmux.EnvWorkspaceID, attachWS)
	out, err := runSubject(t, &stubDaemon{srv: srv}, "which", "--json")
	if err == nil || !strings.Contains(out, `"state":"bound"`) || !strings.Contains(err.Error(), "no subject bound to cmux workspace "+attachWS) {
		t.Fatalf("which after a lookup 404 then a bound state = %q %v, want \"state\":\"bound\" and exit 1", out, err)
	}
}
