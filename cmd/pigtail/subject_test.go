package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestApplySubjectFieldThemeReassignment(t *testing.T) {
	t.Parallel()
	sub := &pigtail.Subject{ID: "sub_1", ThemeID: "thm_old"}

	// `theme=` is the friendly alias that reassigns the subject's theme.
	if err := applySubjectField(sub, "theme", "thm_new"); err != nil {
		t.Fatalf("theme=: %v", err)
	}
	if sub.ThemeID != "thm_new" {
		t.Fatalf("theme not reassigned: %q", sub.ThemeID)
	}

	// `theme_id=` remains a synonym.
	if err := applySubjectField(sub, "theme_id", "thm_other"); err != nil {
		t.Fatalf("theme_id=: %v", err)
	}
	if sub.ThemeID != "thm_other" {
		t.Fatalf("theme_id not applied: %q", sub.ThemeID)
	}
}

func TestApplySubjectFieldUnknownKey(t *testing.T) {
	t.Parallel()
	sub := &pigtail.Subject{ID: "sub_1"}
	if err := applySubjectField(sub, "bogus", "v"); err == nil {
		t.Fatalf("expected error for unknown key")
	}
}

// TestSubjectShowJSONCarriesBlockedBy (D30): show --json lifts the daemon's
// blocked_by to the top level (never under subject, [] stays []); an older
// daemon without the key gives no key and no panic; list --json never has it.
func TestSubjectShowJSONCarriesBlockedBy(t *testing.T) {
	for _, tc := range []struct {
		name, subject string
		want          bool
	}{
		{"present", `{"id":"sub_1","theme_id":"thm_1","title":"t","slug":"t","status":"inbox","source":"local","blocked_by":[],"created_at":"2026-10-22T00:00:00Z","updated_at":"2026-10-22T00:00:00Z"}`, true},
		{"absent", `{"id":"sub_1","theme_id":"thm_1","title":"t","slug":"t","status":"inbox","source":"local","created_at":"2026-10-22T00:00:00Z","updated_at":"2026-10-22T00:00:00Z"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withCells(t, func(_, perso string) string { return perso })
			raw := func(w http.ResponseWriter, body string) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"data":`+body+`,"meta":{}}`)
			}
			mux := http.NewServeMux()
			mux.HandleFunc("/subjects/sub_1", func(w http.ResponseWriter, _ *http.Request) { raw(w, tc.subject) })
			mux.HandleFunc("/subjects/sub_1/phases", func(w http.ResponseWriter, _ *http.Request) { raw(w, `[]`) })
			// GET /subjects never carries blocked_by (TestSubjectBlockedBy):
			// the CLI must not add it either.
			mux.HandleFunc("/subjects", func(w http.ResponseWriter, _ *http.Request) {
				raw(w, `[{"id":"sub_1","theme_id":"thm_1","title":"t","slug":"t","status":"inbox","source":"local","created_at":"2026-10-22T00:00:00Z","updated_at":"2026-10-22T00:00:00Z"}]`)
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			s := &stubDaemon{srv: srv}

			out, err := runSubject(t, s, "show", "sub_1", "--json")
			if err != nil {
				t.Fatalf("show --json: %v", err)
			}
			var top map[string]json.RawMessage
			if err := json.Unmarshal([]byte(out), &top); err != nil {
				t.Fatalf("decode %q: %v", out, err)
			}
			v, ok := top["blocked_by"]
			if ok != tc.want {
				t.Fatalf("top-level blocked_by present = %v, want %v: %s", ok, tc.want, out)
			}
			if tc.want && string(v) != "[]" {
				t.Fatalf("blocked_by = %s, want []", v)
			}
			var sub map[string]json.RawMessage
			_ = json.Unmarshal(top["subject"], &sub)
			if _, dup := sub["blocked_by"]; dup {
				t.Fatalf("blocked_by also under subject: %s", out)
			}

			list, err := runSubject(t, s, "list", "--json")
			if err != nil {
				t.Fatalf("list --json: %v", err)
			}
			if strings.Contains(list, "blocked_by") {
				t.Fatalf("list --json carries blocked_by: %s", list)
			}
		})
	}
}

const wantUnknownResearching = `unknown status "researching": use one of inbox, understanding, designing, building, done, parked, dropped, captured`

// statusStub serves one subject and counts every write it receives.
func statusStub(t *testing.T) (*stubDaemon, *int) {
	t.Helper()
	writes := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/subjects/sub_1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes++
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"id":"sub_1","theme_id":"thm_1","title":"t","slug":"t","status":"inbox","source":"local","created_at":"2026-10-22T00:00:00Z","updated_at":"2026-10-22T00:00:00Z"},"meta":{}}`)
	})
	mux.HandleFunc("/subjects", func(w http.ResponseWriter, r *http.Request) {
		writes++ // a list reaching the daemon counts too: the filter must stop first
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[],"meta":{}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &stubDaemon{srv: srv}, &writes
}

// TestSubjectSetRefusesUnknownStatus (D4): an unknown status is refused by
// the CLI with the exact message, before any write reaches the daemon.
func TestSubjectSetRefusesUnknownStatus(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, writes := statusStub(t)
	_, err := runSubject(t, s, "set", "sub_1", "status=researching")
	if err == nil || err.Error() != wantUnknownResearching {
		t.Fatalf("set status=researching: %v, want %q", err, wantUnknownResearching)
	}
	if *writes != 0 {
		t.Fatalf("daemon received %d write(s), want none", *writes)
	}
}

// TestSubjectListRefusesUnknownStatusFilter (D4, Trou 5): --status takes the
// same vocabulary, same message.
func TestSubjectListRefusesUnknownStatusFilter(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, calls := statusStub(t)
	_, err := runSubject(t, s, "list", "--status", "researching")
	if err == nil || err.Error() != wantUnknownResearching {
		t.Fatalf("list --status researching: %v, want %q", err, wantUnknownResearching)
	}
	if *calls != 0 {
		t.Fatalf("daemon received %d list call(s), want none", *calls)
	}
}
