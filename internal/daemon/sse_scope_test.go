package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// streamAs replays the bus to a subscriber sending header (""= none) and
// returns what it received before the stream is cut.
func streamAs(t *testing.T, srv *Server, header string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest("GET", "/events", nil).WithContext(ctx)
	if header != "" {
		req.Header.Set(pigtail.HeaderScope, header)
	}
	rec := httptest.NewRecorder()
	srv.handleEvents(rec, req)
	return rec
}

// The SSE stream follows the work/perso rule of GET /messages: a work (or
// headerless) subscriber gets work conversations only; perso gets all. A
// subject-bearing event with no scope is dropped for work — fail-closed.
func TestSSEFiltersMessageEventsByScope(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	bus := events.New()
	srv, err := New(Config{SocketPath: testSocketPath(t)}, st, bus, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	bus.Publish(pigtail.Event{Type: pigtail.EventWorkspaceCreated, Data: map[string]interface{}{"workspace_id": "ws_open"}})
	bus.Publish(pigtail.Event{Type: pigtail.EventMessageCreated, Scope: "gx", Data: map[string]interface{}{"id": "msg_work"}})
	bus.Publish(pigtail.Event{Type: pigtail.EventMessageCreated, Scope: "perso", Data: map[string]interface{}{"id": "msg_perso"}})
	bus.Publish(pigtail.Event{Type: pigtail.EventMessageAcked, Data: map[string]interface{}{"id": "msg_untagged"}})

	for _, h := range []string{"", "gx"} {
		body := streamAs(t, srv, h).Body.String()
		for _, want := range []string{"ws_open", "msg_work"} {
			if !strings.Contains(body, want) {
				t.Errorf("subscriber %q lost %s:\n%s", h, want, body)
			}
		}
		for _, leak := range []string{"msg_perso", "msg_untagged"} {
			if strings.Contains(body, leak) {
				t.Errorf("subscriber %q received %s:\n%s", h, leak, body)
			}
		}
	}
	body := streamAs(t, srv, "perso").Body.String()
	for _, want := range []string{"ws_open", "msg_work", "msg_perso", "msg_untagged"} {
		if !strings.Contains(body, want) {
			t.Errorf("perso subscriber lost %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, `"Scope"`) || strings.Contains(body, `"scope":"perso"`) {
		t.Errorf("the scope tag is daemon-side, never on the wire:\n%s", body)
	}
	if rec := streamAs(t, srv, "everything"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid scope = %d, want 400", rec.Code)
	}
}

// sse.go:125 — eventVisible lets everything through only for caller ==
// scope.Scope("perso"); root falls to the untagged rule and loses every untagged
// message.* / cmux.* event (subjectBearingEvents), which the master must get.
func TestSSEUntaggedSubjectEventsReachTheRenamedMaster(t *testing.T) {
	scopetest.RenamedMaster(t)
	st, _ := store.Open(":memory:")
	defer st.Close()
	bus := events.New()
	srv, err := New(Config{SocketPath: testSocketPath(t)}, st, bus, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	bus.Publish(pigtail.Event{Type: pigtail.EventMessageAcked, Data: map[string]interface{}{"id": "msg_untagged"}})
	bus.Publish(pigtail.Event{Type: pigtail.EventCmuxSidebarChanged, Data: map[string]interface{}{"workspace_id": "ws_untagged"}})
	body := streamAs(t, srv, "root").Body.String()
	for _, want := range []string{"msg_untagged", "ws_untagged"} {
		if !strings.Contains(body, want) {
			t.Errorf("root subscriber lost %s:\n%s", want, body)
		}
	}
	for _, h := range []string{"globex", "acme"} { // guard: untagged stays master-only
		if body := streamAs(t, srv, h).Body.String(); strings.Contains(body, "msg_untagged") || strings.Contains(body, "ws_untagged") {
			t.Errorf("subscriber %s received an untagged subject event:\n%s", h, body)
		}
	}
}

// A tagged event follows CanRead, which reads the table: under a renamed
// master, root and the event's own scope get it, another scope does not.
func TestSSETaggedEventFollowsTheTable(t *testing.T) {
	scopetest.RenamedMaster(t)
	st, _ := store.Open(":memory:")
	defer st.Close()
	bus := events.New()
	srv, err := New(Config{SocketPath: testSocketPath(t)}, st, bus, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	bus.Publish(pigtail.Event{Type: pigtail.EventMessageCreated, Scope: "acme", Data: map[string]interface{}{"id": "msg_acme"}})
	for h, want := range map[string]bool{"root": true, "acme": true, "globex": false} {
		if got := strings.Contains(streamAs(t, srv, h).Body.String(), "msg_acme"); got != want {
			t.Errorf("subscriber %s got the acme event = %v, want %v", h, got, want)
		}
	}
}

// wait.* events carry a session id: a perso session's never reach a work
// subscriber, and an untagged one reaches the master only.
func TestSSEFiltersWaitEventsByScope(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	bus := events.New()
	srv, err := New(Config{SocketPath: testSocketPath(t)}, st, bus, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	bus.Publish(pigtail.Event{Type: pigtail.EventWaitOpened, Scope: "globex", Data: map[string]interface{}{"wait_id": "wait_work"}})
	bus.Publish(pigtail.Event{Type: pigtail.EventWaitOpened, Scope: "perso", Data: map[string]interface{}{"wait_id": "wait_perso"}})
	bus.Publish(pigtail.Event{Type: pigtail.EventWaitClosed, Data: map[string]interface{}{"wait_id": "wait_untagged"}})
	body := streamAs(t, srv, "gx").Body.String()
	if !strings.Contains(body, "wait_work") {
		t.Errorf("work subscriber lost wait_work:\n%s", body)
	}
	for _, leak := range []string{"wait_perso", "wait_untagged"} {
		if strings.Contains(body, leak) {
			t.Errorf("work subscriber received %s:\n%s", leak, body)
		}
	}
	body = streamAs(t, srv, "perso").Body.String()
	for _, want := range []string{"wait_work", "wait_perso", "wait_untagged"} {
		if !strings.Contains(body, want) {
			t.Errorf("master lost %s:\n%s", want, body)
		}
	}
}

// subject.output_added carries a subject id: a perso one never reaches a work
// subscriber, and an untagged one reaches the master only (fail-closed).
func TestSSESubjectOutputEventScoped(t *testing.T) {
	work := scope.Default() // globex: a non-master scope
	e := pigtail.Event{Type: pigtail.EventSubjectOutputAdded, Scope: "perso"}
	if eventVisible(work, e) {
		t.Error("perso subject.output_added visible to work")
	}
	e.Scope = ""
	if eventVisible(work, e) {
		t.Error("untagged subject.output_added visible to work")
	}
	if !eventVisible(scope.Master(), e) {
		t.Error("untagged subject.output_added hidden from the master")
	}
}

// session.launch_changed carries a subject: tagged with the subject's scope,
// it reaches that scope's subscribers (and the master's); untagged, the
// master's only (sub_x000000000000015).
func TestSSEFiltersLaunchEventsByScope(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	bus := events.New()
	srv, err := New(Config{SocketPath: testSocketPath(t)}, st, bus, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	bus.Publish(pigtail.Event{Type: pigtail.EventSessionLaunchChanged, Scope: "perso", Data: map[string]interface{}{"launch_id": "ln_perso"}})
	bus.Publish(pigtail.Event{Type: pigtail.EventSessionLaunchChanged, Scope: "gx", Data: map[string]interface{}{"launch_id": "ln_work"}})
	bus.Publish(pigtail.Event{Type: pigtail.EventSessionLaunchChanged, Data: map[string]interface{}{"launch_id": "ln_untagged"}})

	body := streamAs(t, srv, "gx").Body.String()
	if !strings.Contains(body, "ln_work") {
		t.Errorf("work subscriber lost its launch:\n%s", body)
	}
	for _, leak := range []string{"ln_perso", "ln_untagged"} {
		if strings.Contains(body, leak) {
			t.Errorf("work subscriber received %s:\n%s", leak, body)
		}
	}
	body = streamAs(t, srv, "perso").Body.String()
	for _, want := range []string{"ln_perso", "ln_work", "ln_untagged"} {
		if !strings.Contains(body, want) {
			t.Errorf("master subscriber lost %s:\n%s", want, body)
		}
	}
}

// staffing.decision carries subjects of every scope and is published
// untagged: it reaches the master's subscribers only (sub_x000000000000004).
func TestStaffingEventMasterOnly(t *testing.T) {
	e := pigtail.Event{Type: pigtail.EventStaffingDecision, Data: map[string]interface{}{"outcome": "idle"}}
	if eventVisible(scope.Default(), e) {
		t.Error("untagged staffing.decision visible to a non-master subscriber")
	}
	if !eventVisible(scope.Master(), e) {
		t.Error("staffing.decision hidden from the master")
	}
}
