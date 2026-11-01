package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func handbacksDaemon(t *testing.T, gotSince *string) *stubDaemon {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/session-handbacks", func(w http.ResponseWriter, r *http.Request) {
		*gotSince = r.URL.Query().Get("since")
		writeData(w, http.StatusOK, pigtail.SessionHandbacksResponse{Skills: []pigtail.SkillHandbacks{{Skill: "plan-skill", Count: 2}}})
	})
	s := &stubDaemon{srv: httptest.NewServer(mux)} // same shape as clean_test.go:56
	t.Cleanup(s.srv.Close)
	return s
}

func TestSessionHandbacksCommand(t *testing.T) {
	var since string
	s := handbacksDaemon(t, &since)
	before := time.Now().AddDate(0, 0, -7) // calendar days, not 168 h (DST)
	if err := runCLI(t, s, sessionCmd, "handbacks", "--since", "7d", "--json"); err != nil {
		t.Fatalf("handbacks: %v", err)
	}
	at, err := time.Parse(time.RFC3339, since)
	if err != nil || at.Before(before.Add(-time.Minute)) || at.After(before.Add(time.Minute)) {
		t.Fatalf("since sent = %q (%v)", since, err)
	}
	if err := runCLI(t, s, sessionCmd, "handbacks", "--since", "soon"); err == nil {
		t.Fatal("a bad --since was accepted")
	}
}

// The JSON the weekly review reads (M4): since, skills[{skill,count,last_at}].
func TestRenderHandbacks(t *testing.T) {
	t.Parallel()
	last := time.Date(2026, 10, 5, 9, 3, 0, 0, time.UTC)
	r := pigtail.SessionHandbacksResponse{Since: last.Add(-7 * 24 * time.Hour), Skills: []pigtail.SkillHandbacks{
		{Skill: "", Count: 1, LastAt: last}, {Skill: "plan-skill", Count: 2, LastAt: last},
	}}
	var js bytes.Buffer
	if err := renderHandbacks(&js, r, true); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(js.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	sk, _ := raw["skills"].([]any)
	first, _ := sk[0].(map[string]any)
	if _, ok := raw["since"]; !ok || len(sk) != 2 || first["skill"] != "" || first["count"] != float64(1) || first["last_at"] == nil {
		t.Fatalf("json = %s", js.String())
	}
	var txt bytes.Buffer
	if err := renderHandbacks(&txt, r, false); err != nil {
		t.Fatal(err)
	}
	if out := txt.String(); !strings.Contains(out, "plan-skill") || !strings.Contains(out, "(none)") || strings.Index(out, "plan-skill") > strings.Index(out, "(none)") {
		t.Fatalf("text (sorted by count desc) = %q", out)
	}
}
