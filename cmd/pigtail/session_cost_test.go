package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

var costFixture = pigtail.SessionCost{
	Window: "24h", Samples: 1440,
	Types: []pigtail.SessionCostType{
		{Repo: "pigtail", Status: "busy", Sessions: 7, CPUAvgPct: 38, MemoryAvgGB: 0.4, MemoryPeakGB: 0.9},
		{Repo: "garden", Status: "idle", Sessions: 1, CPUAvgPct: 2, MemoryAvgGB: 0.3, MemoryPeakGB: 0.3},
		{Repo: "globex", Status: "waiting", Sessions: 3, CPUAvgPct: 0, MemoryAvgGB: 0.2, MemoryPeakGB: 0.2},
	},
}

func TestSessionCostPrints(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := renderSessionCostAt(&out, costFixture, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	want := "Coût des sessions — 24 h (1440 échantillons)\n" +
		"pigtail · au travail · 7 sessions · CPU moy 38 % · mémoire moy 0,4 Go · pic 0,9 Go\n" +
		"garden · inactive · 1 session · CPU moy 2 % · mémoire moy 0,3 Go · pic 0,3 Go\n" +
		"globex · en attente · 3 sessions · CPU moy 0 % · mémoire moy 0,2 Go · pic 0,2 Go\n"
	if out.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestSessionCostJSONShape(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := renderSessionCost(&out, costFixture, true); err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Window  string           `json:"window"`
		Samples int              `json:"samples"`
		Types   []map[string]any `json:"types"`
	}
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Window != "24h" || raw.Samples != 1440 || len(raw.Types) != 3 {
		t.Fatalf("json = %s", out.String())
	}
	first := raw.Types[0]
	want := map[string]any{"repo": "pigtail", "status": "busy", "sessions": 7.0,
		"cpu_avg_percent": 38.0, "memory_avg_gb": 0.4, "memory_peak_gb": 0.9}
	if len(first) != len(want) {
		t.Fatalf("keys = %v, want exactly %v", first, want)
	}
	for k, v := range want {
		if first[k] != v {
			t.Errorf("key %q = %v, want %v", k, first[k], v)
		}
	}
}

// The command asks the daemon for the window's samples as the caller's scope
// and aggregates them itself.
func TestSessionCostCommand(t *testing.T) {
	var since, scopeHdr string
	mux := http.NewServeMux()
	mux.HandleFunc("/session-budget/samples", func(w http.ResponseWriter, r *http.Request) {
		since, scopeHdr = r.URL.Query().Get("since"), r.Header.Get(pigtail.HeaderScope)
		writeData(w, http.StatusOK, pigtail.SessionSamples{Samples: []pigtail.SessionSample{{
			TS: time.Now(), Live: 1, Busy: 1,
			Sessions: []pigtail.SessionSampleRow{{SessionID: "a", Repo: "pigtail", Status: "busy", FootprintBytes: 1 << 30, CPUPercent: 10}},
		}}})
	})
	s := &stubDaemon{srv: httptest.NewServer(mux)}
	t.Cleanup(s.srv.Close)
	before := time.Now().Add(-24 * time.Hour)
	if err := runCLI(t, s, sessionCmd, "cost", "--since", "24h"); err != nil {
		t.Fatalf("cost: %v", err)
	}
	at, err := time.Parse(time.RFC3339, since)
	if err != nil || at.Before(before.Add(-time.Minute)) || at.After(before.Add(time.Minute)) {
		t.Fatalf("since sent = %q (%v)", since, err)
	}
	if scopeHdr == "" {
		t.Error("the caller's scope header was not sent (newScopedClient)")
	}
	if err := runCLI(t, s, sessionCmd, "cost", "--since", "soon"); err == nil || !strings.Contains(err.Error(), "since") {
		t.Fatalf("a bad --since was accepted: %v", err)
	}
}

func costOut(t *testing.T, r pigtail.SessionCost, now time.Time) string {
	t.Helper()
	var out bytes.Buffer
	if err := renderSessionCostAt(&out, r, false, now); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestSessionCostEmptyStates(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.Local)
	got := costOut(t, pigtail.SessionCost{Window: "24h", Samples: 5}, now)
	want := "Coût des sessions — 24 h (5 échantillons)\naucune session sur la fenêtre\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
	got = costOut(t, pigtail.SessionCost{Window: "24h"}, now)
	want = "Coût des sessions — 24 h (0 échantillon)\naucun échantillon — mesure éteinte ou historique vide\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestSessionCostSampleAgreement(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.Local)
	typ := []pigtail.SessionCostType{{Repo: "r", Status: "busy", Sessions: 1}}
	for n, w := range map[int]string{1: "(1 échantillon)", 2: "(2 échantillons)"} {
		got := costOut(t, pigtail.SessionCost{Window: "24h", Samples: n, Types: typ}, now)
		if !strings.Contains(got, w) {
			t.Errorf("samples=%d: %q lacks %q", n, got, w)
		}
	}
}

func TestSessionCostWindowLabel(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.Local)
	ms := time.Date(2026, 9, 22, 0, 0, 0, 0, time.Local).UnixMilli()
	cases := map[string]string{
		"24h":                     "24 h",
		"1h":                      "1 h",
		"7d":                      "7 j",
		"90m":                     "90 min",
		"2026-09-22":              "depuis le 22/09",
		"2025-09-22":              "depuis le 22/09/2025",
		strconv.FormatInt(ms, 10): "depuis le 22/09",
	}
	for in, want := range cases {
		if got := sinceLabel(in, now); got != want {
			t.Errorf("sinceLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
