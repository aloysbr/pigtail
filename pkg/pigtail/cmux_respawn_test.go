package pigtail

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestEndCmuxRespawnReadsTheNotice(t *testing.T) {
	var got CmuxRespawnRequest
	c := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": CmuxRespawnNotice{Journal: "ok", Wait: "ok", Notice: "skipped: session abcd1234 is gone", Pill: "ok"}})
	}))
	n, err := c.EndCmuxRespawn(context.Background(), "WS", CmuxRespawnRequest{State: CmuxRespawnAbandoned, RunID: "r1", SessionID: "s1", Detail: "d"})
	if err != nil || n == nil || n.Notice != "skipped: session abcd1234 is gone" || got.RunID != "r1" || got.SessionID != "s1" {
		t.Fatalf("notice = %+v, %v; sent %+v", n, err, got)
	}
	if p := n.Problems(); p != "notice skipped: session abcd1234 is gone" {
		t.Fatalf("Problems = %q", p)
	}
}

// A daemon older than the notice answers 204: no report, no error.
func TestEndCmuxRespawnOlderDaemon(t *testing.T) {
	c := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	n, err := c.EndCmuxRespawn(context.Background(), "WS", CmuxRespawnRequest{State: CmuxRespawnFailed, RunID: "r1"})
	if err != nil || n != nil {
		t.Fatalf("notice = %+v, %v; want nil, nil", n, err)
	}
}

func TestCmuxRespawnNoticeProblems(t *testing.T) {
	if p := (CmuxRespawnNotice{Journal: "ok", Wait: "duplicate", Notice: "ok"}).Problems(); p != "" {
		t.Fatalf("all fine: Problems = %q", p)
	}
	if p := (CmuxRespawnNotice{Journal: "failed: disk", Wait: "ok", Notice: "skipped: x"}).Problems(); p != "journal failed: disk; notice skipped: x" {
		t.Fatalf("Problems = %q", p)
	}
	// An abandoned respawn poses no wait: none is fine for the wait field only.
	if p := (CmuxRespawnNotice{Journal: "ok", Wait: "none", Notice: "ok"}).Problems(); p != "" {
		t.Fatalf("wait none: Problems = %q", p)
	}
	if p := (CmuxRespawnNotice{Journal: "none", Wait: "ok", Notice: "ok"}).Problems(); p != "journal none" {
		t.Fatalf("journal none: Problems = %q", p)
	}
	// The pill is not the notice: a daemon without the sidebar loop told the
	// session all the same (CA8b).
	n := CmuxRespawnNotice{Journal: "ok", Wait: "ok", Notice: "ok", Pill: "skipped: this daemon runs no cmux sidebar loop"}
	if n.Problems() != "" || n.PillProblem() != "skipped: this daemon runs no cmux sidebar loop" {
		t.Fatalf("Problems = %q, PillProblem = %q", n.Problems(), n.PillProblem())
	}
	if p := (CmuxRespawnNotice{Pill: "ok"}).PillProblem(); p != "" {
		t.Fatalf("pill ok: PillProblem = %q", p)
	}
}
