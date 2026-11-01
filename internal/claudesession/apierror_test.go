// internal/claudesession/apierror_test.go
package claudesession

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Shape measured 2026-10-05 on a usage limit (2026-09-23):
// top-level error/isApiErrorMessage/apiErrorStatus, quotaLimits.resetsAt in epoch seconds.
const limitLine = `{"type":"assistant","timestamp":"2026-09-23T10:10:18.832Z","error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429,"quotaLimits":{"status":"rejected","resetsAt":1790159400,"rateLimitType":"five_hour","overageDisabledReason":"org_spend_cap_reached"},"message":{"role":"assistant","content":[{"type":"text","text":"You've hit your limit"}]}}`

func writeAPITranscript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLastAPIErrorReadsTheLatestErrorEntry(t *testing.T) {
	old := strings.Replace(strings.Replace(limitLine, "1790159400", "1700000000", 1), "2026-09-23T10:10:18.832Z", "2026-09-01T08:00:00Z", 1)
	p := writeAPITranscript(t, old, `{"type":"user","message":{"role":"user","content":"go"}}`, limitLine, `{"type":"system","subtype":"x"}`)
	e, ok := LastAPIError(p)
	if !ok || e.Error != "rate_limit" || e.Status != 429 || !e.ResetsAt.Equal(time.Unix(1790159400, 0)) {
		t.Fatalf("got %+v ok=%v", e, ok)
	}
	if want, _ := time.Parse(time.RFC3339Nano, "2026-09-23T10:10:18.832Z"); !e.At.Equal(want) {
		t.Fatalf("At = %v", e.At)
	}
}

func TestLastAPIErrorWithoutQuota(t *testing.T) {
	p := writeAPITranscript(t, `{"type":"assistant","timestamp":"2026-10-01T09:00:00Z","error":"server_error","isApiErrorMessage":true,"apiErrorStatus":500}`)
	e, ok := LastAPIError(p)
	if !ok || e.Error != "server_error" || !e.ResetsAt.IsZero() {
		t.Fatalf("got %+v ok=%v", e, ok)
	}
}

func TestLastAPIErrorNone(t *testing.T) {
	for _, p := range []string{writeAPITranscript(t, `{"type":"user"}`), filepath.Join(t.TempDir(), "missing.jsonl"), ""} {
		if _, ok := LastAPIError(p); ok {
			t.Fatalf("%q: found an error", p)
		}
	}
}

// Only the tail is read: a transcript of hundreds of MB costs the hook nothing.
func TestLastAPIErrorReadsOnlyTheTail(t *testing.T) {
	big := strings.Repeat(`{"type":"user","message":{"content":"`+strings.Repeat("x", 1000)+`"}}`+"\n", 2000)
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(limitLine+"\n"+big), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := LastAPIError(p); ok {
		t.Fatal("an error 2 MB before the end must be out of the tail")
	}
}

func apiTextLine(text string) string {
	return `{"type":"assistant","timestamp":"2026-10-30T09:00:00Z","error":"server_error","isApiErrorMessage":true,"message":{"role":"assistant","content":[{"type":"text","text":` + jsonString(text) + `}]}}`
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestLastAPIErrorTextAndClass(t *testing.T) {
	cases := []struct {
		text  string
		class string
	}{
		{"API Error: Can't reach the API server (ENOTFOUND)", ClassNetwork},
		{"API Error: your Mac went to sleep mid-response", ClassNetwork},
		{"API Error: Connection lost mid-response", ClassNetwork},
		{"API Error: 529 Overloaded", ClassOther},
		{"No response from API", ClassOther},
	}
	for _, c := range cases {
		e, ok := LastAPIError(writeAPITranscript(t, apiTextLine(c.text)))
		if !ok || e.Text != c.text || e.Class() != c.class {
			t.Errorf("%q: got Text=%q Class=%q ok=%v, want class %q", c.text, e.Text, e.Class(), ok, c.class)
		}
	}
	// A string content, and several text blocks joined by a newline.
	e, _ := LastAPIError(writeAPITranscript(t, `{"isApiErrorMessage":true,"error":"server_error","message":{"content":"plain ENOTFOUND"}}`))
	if e.Text != "plain ENOTFOUND" || e.Class() != ClassNetwork {
		t.Errorf("string content: %+v", e)
	}
	e, _ = LastAPIError(writeAPITranscript(t, `{"isApiErrorMessage":true,"error":"server_error","message":{"content":[{"type":"text","text":"a"},{"type":"tool_use"},{"type":"text","text":"b"}]}}`))
	if e.Text != "a\nb" {
		t.Errorf("blocks: %q", e.Text)
	}
	// No message: empty text, class other.
	e, ok := LastAPIError(writeAPITranscript(t, `{"isApiErrorMessage":true,"error":"server_error"}`))
	if !ok || e.Text != "" || e.Class() != ClassOther {
		t.Errorf("no message: %+v ok=%v", e, ok)
	}
}

func TestAPIErrorNetworkIsCaseInsensitive(t *testing.T) {
	if !(APIError{Text: "econnreset by peer"}).Network() {
		t.Fatal("ECONNRESET lowercase not recognised")
	}
}
