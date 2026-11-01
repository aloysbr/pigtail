package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func handbacksOf(t *testing.T, f *relayFixture, c caller, query string) pigtail.SessionHandbacksResponse {
	t.Helper()
	rec := doAs(t, f.r, c, "GET", "/session-handbacks"+query, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET handbacks = %d %s", rec.Code, rec.Body.String())
	}
	var out pigtail.SessionHandbacksResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// D11/D12 over HTTP: a skill, then a Stop of an undeclared turn, counts under
// that skill, in the session's scope; a work caller does not see perso turns.
func TestSessionTurnsCountedBySkill(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s-work", Name: "t1", Scope: "gx"})
	f.peers(t, pigtail.PeerSession{SessionID: "s-perso", Name: "p1", Scope: "perso"})
	start := f.clock().Add(-time.Minute)
	if rec := doAs(t, f.r, work, "POST", "/peers/s-work/skill", pigtail.SessionSkillRequest{Skill: "my-plan"}); rec.Code != http.StatusNoContent {
		t.Fatalf("POST skill = %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range []struct {
		who  caller
		sess string
	}{{work, "s-work"}, {perso, "s-perso"}} {
		if rec := doAs(t, f.r, c.who, "POST", "/peers/"+c.sess+"/turn", pigtail.SessionTurnRequest{TurnID: "p1", TurnStart: start, Event: "stop"}); rec.Code != http.StatusNoContent {
			t.Fatalf("POST turn %s = %d %s", c.sess, rec.Code, rec.Body.String())
		}
	}
	got := handbacksOf(t, f, work, "")
	if len(got.Skills) != 1 || got.Skills[0].Skill != "my-plan" || got.Skills[0].Count != 1 {
		t.Fatalf("work view = %+v", got)
	}
	if want := f.clock().Add(-7 * 24 * time.Hour); !got.Since.Equal(want) {
		t.Fatalf("default since = %v, want %v", got.Since, want)
	}
	if got := handbacksOf(t, f, perso, ""); len(got.Skills) != 2 {
		t.Fatalf("perso view = %+v", got)
	}
	if got := handbacksOf(t, f, perso, "?since="+f.clock().Add(time.Hour).Format(time.RFC3339)); len(got.Skills) != 0 {
		t.Fatalf("since in the future = %+v", got)
	}
}

func TestSessionTurnAndSkillRefusals(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Name: "a", Scope: "gx"})
	now := f.clock()
	for name, c := range map[string]struct {
		path string
		body any
		want int
	}{
		"bad skill":     {"/peers/s1/skill", pigtail.SessionSkillRequest{Skill: "bad name; rm"}, http.StatusBadRequest},
		"empty skill":   {"/peers/s1/skill", pigtail.SessionSkillRequest{}, http.StatusBadRequest},
		"bad event":     {"/peers/s1/turn", pigtail.SessionTurnRequest{TurnID: "p", TurnStart: now, Event: "end"}, http.StatusBadRequest},
		"no turn":       {"/peers/s1/turn", pigtail.SessionTurnRequest{TurnStart: now, Event: "stop"}, http.StatusBadRequest},
		"slash turn":    {"/peers/s1/turn", pigtail.SessionTurnRequest{TurnID: "a/b", TurnStart: now, Event: "stop"}, http.StatusBadRequest},
		"long turn":     {"/peers/s1/turn", pigtail.SessionTurnRequest{TurnID: strings.Repeat("x", 129), TurnStart: now, Event: "stop"}, http.StatusBadRequest},
		"no start":      {"/peers/s1/turn", pigtail.SessionTurnRequest{TurnID: "p", Event: "stop"}, http.StatusBadRequest},
		"unknown sess.": {"/peers/s-unknown/turn", pigtail.SessionTurnRequest{TurnID: "p", TurnStart: now, Event: "stop"}, http.StatusNotFound},
	} {
		if rec := doAs(t, f.r, work, "POST", c.path, c.body); rec.Code != c.want {
			t.Errorf("%s = %d %s, want %d", name, rec.Code, rec.Body.String(), c.want)
		}
	}
	if rec := doAs(t, f.r, work, "GET", "/session-handbacks?since=soon", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad since = %d", rec.Code)
	}
}

// sub_x000000000000041 over HTTP: the /standby turn is not a missed
// hand-back; after the Standby ends, a turn counts again.
func TestStandbyTurnNotAHandback(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s-perso", Name: "p1", Scope: "perso"})
	start := f.clock().Add(-time.Minute)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/peers/s-perso/skill", pigtail.SessionSkillRequest{Skill: "standby"}},
		{"PUT", "/peers/s-perso/standby", nil},
		{"POST", "/peers/s-perso/turn", pigtail.SessionTurnRequest{TurnID: "p1", TurnStart: start, Event: "stop"}},
	} {
		if rec := doAs(t, f.r, perso, c.method, c.path, c.body); rec.Code/100 != 2 {
			t.Fatalf("%s %s = %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	if got := handbacksOf(t, f, perso, ""); len(got.Skills) != 0 {
		t.Fatalf("in Standby: %+v, want no hand-back", got)
	}
	if rec := doAs(t, f.r, perso, "DELETE", "/peers/s-perso/standby", nil); rec.Code != http.StatusOK {
		t.Fatalf("DELETE standby = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doAs(t, f.r, perso, "POST", "/peers/s-perso/turn", pigtail.SessionTurnRequest{TurnID: "p2", TurnStart: f.clock(), Event: "stop"}); rec.Code != http.StatusNoContent {
		t.Fatalf("POST turn = %d %s", rec.Code, rec.Body.String())
	}
	got := handbacksOf(t, f, perso, "")
	if len(got.Skills) != 1 || got.Skills[0].Skill != "standby" || got.Skills[0].Count != 1 {
		t.Fatalf("out of Standby: %+v, want one standby turn", got)
	}
}

// A 🔄 line in the /standby answer infers no working wait: a declared wait
// would hold the compaction. Out of Standby the wait is inferred (control).
func TestStandbyTurnInfersNoWorking(t *testing.T) {
	t.Parallel()
	for _, standby := range []bool{true, false} {
		f := newRelayFixture(t)
		f.peers(t, pigtail.PeerSession{SessionID: "s-perso", Name: "p1", Scope: "perso"})
		if standby {
			if rec := doAs(t, f.r, perso, "PUT", "/peers/s-perso/standby", nil); rec.Code != http.StatusOK {
				t.Fatalf("PUT standby = %d %s", rec.Code, rec.Body.String())
			}
		}
		req := pigtail.SessionTurnRequest{TurnID: "p1", TurnStart: f.clock().Add(-time.Minute), Event: "stop", Working: true, Reason: "x"}
		if rec := doAs(t, f.r, perso, "POST", "/peers/s-perso/turn", req); rec.Code != http.StatusNoContent {
			t.Fatalf("POST turn = %d %s", rec.Code, rec.Body.String())
		}
		ws, err := f.st.SessionWaits("s-perso")
		if err != nil {
			t.Fatal(err)
		}
		_, statErr := os.Stat(filepath.Join(f.waitMk, "s-perso"))
		if standby && (len(ws) != 0 || statErr == nil) {
			t.Fatalf("in Standby: waits %+v, marker %v", ws, statErr)
		}
		if !standby && (len(ws) != 1 || ws[0].Type != "working" || statErr != nil) {
			t.Fatalf("out of Standby: waits %+v, marker %v; want the working wait", ws, statErr)
		}
	}
}
