package handlers

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// fakeOffsiteRunner is a hand-rolled OffsiteRunner stub — RunOffsiteBackup's
// contract is two method calls and three booleans, nothing this package
// needs a real offsiteBackupper (internal/daemon, unreachable from here) to
// exercise.
type fakeOffsiteRunner struct {
	mu sync.Mutex

	offsiteStarted     bool
	fallbackStarted    bool
	fallbackConfigured bool

	primaryCalls  int
	fallbackCalls int
	gotDBPath     string
	gotDestDir    string
	gotFallback   string
}

func (f *fakeOffsiteRunner) RunOffsiteNow(dbPath, destDir string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.primaryCalls++
	f.gotDBPath, f.gotDestDir = dbPath, destDir
	return f.offsiteStarted
}

func (f *fakeOffsiteRunner) RunOffsiteFallbackNow(dbPath, destDir, fallbackDest string) (bool, bool) {
	f.mu.Lock()
	f.fallbackCalls++
	f.gotFallback = fallbackDest
	f.mu.Unlock()
	return f.fallbackStarted, f.fallbackConfigured
}

func newOffsiteRunTestRouter(svc *Services) *chi.Mux {
	r := chi.NewRouter()
	r.Post("/backup/offsite/run", RunOffsiteBackup(svc, testResponder{}))
	return r
}

// The happy path: both routes start, both interface methods are called
// exactly once, with the configured destinations.
func TestRunOffsiteBackupHandlerOK(t *testing.T) {
	t.Parallel()
	st := openBackupTestStore(t)
	fake := &fakeOffsiteRunner{offsiteStarted: true, fallbackStarted: true, fallbackConfigured: true}
	svc := &Services{Store: st, Offsite: fake, OffsiteDestDir: "/Volumes/nas/pigtail", OffsiteFallbackDest: "nas-fallback:/pigtail"}

	rec := doJSON(t, newOffsiteRunTestRouter(svc), "POST", "/backup/offsite/run", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	var out pigtail.BackupRunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OffsiteStarted || !out.FallbackStarted || !out.FallbackConfigured {
		t.Errorf("got %+v, want all three true", out)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.primaryCalls != 1 {
		t.Errorf("RunOffsiteNow called %d times, want 1", fake.primaryCalls)
	}
	if fake.fallbackCalls != 1 {
		t.Errorf("RunOffsiteFallbackNow called %d times, want 1", fake.fallbackCalls)
	}
	if fake.gotDestDir != "/Volumes/nas/pigtail" {
		t.Errorf("RunOffsiteNow destDir = %q, want the configured primary destination", fake.gotDestDir)
	}
	if fake.gotFallback != "nas-fallback:/pigtail" {
		t.Errorf("RunOffsiteFallbackNow fallbackDest = %q, want the configured fallback destination", fake.gotFallback)
	}
	if fake.gotDBPath != st.Path() {
		t.Errorf("RunOffsiteNow dbPath = %q, want %q", fake.gotDBPath, st.Path())
	}
}

// A primary copy already in flight: offsite_started reads false, 200 not
// 409 — the case this handler's whole doc comment argues 409 cannot express.
func TestRunOffsiteBackupHandlerPrimaryAlreadyInProgress(t *testing.T) {
	t.Parallel()
	st := openBackupTestStore(t)
	fake := &fakeOffsiteRunner{offsiteStarted: false, fallbackStarted: true, fallbackConfigured: true}
	svc := &Services{Store: st, Offsite: fake, OffsiteDestDir: "/Volumes/nas/pigtail", OffsiteFallbackDest: "nas-fallback:/pigtail"}

	rec := doJSON(t, newOffsiteRunTestRouter(svc), "POST", "/backup/offsite/run", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (not 409): body=%s", rec.Code, rec.Body.String())
	}
	var out pigtail.BackupRunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.OffsiteStarted {
		t.Error("offsite_started = true, want false: the primary copy was already in flight")
	}
	if !out.FallbackStarted {
		t.Error("fallback_started = false, want true: the fallback route is independent of the primary's own busy slot")
	}
}

// No fallback destination configured: both fallback booleans read false,
// still 200.
func TestRunOffsiteBackupHandlerNoFallbackConfigured(t *testing.T) {
	t.Parallel()
	st := openBackupTestStore(t)
	fake := &fakeOffsiteRunner{offsiteStarted: true, fallbackStarted: false, fallbackConfigured: false}
	svc := &Services{Store: st, Offsite: fake, OffsiteDestDir: "/Volumes/nas/pigtail"}

	rec := doJSON(t, newOffsiteRunTestRouter(svc), "POST", "/backup/offsite/run", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out pigtail.BackupRunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OffsiteStarted {
		t.Error("offsite_started = false, want true")
	}
	if out.FallbackStarted || out.FallbackConfigured {
		t.Errorf("fallback booleans = (%v, %v), want (false, false): no fallback destination is configured",
			out.FallbackStarted, out.FallbackConfigured)
	}
}

// No primary destination configured at all: 400 INVALID_REQUEST, and
// neither interface method is ever called — there is nothing to trigger.
func TestRunOffsiteBackupHandlerNoDestinationIsInvalid(t *testing.T) {
	t.Parallel()
	st := openBackupTestStore(t)
	fake := &fakeOffsiteRunner{}
	svc := &Services{Store: st, Offsite: fake, OffsiteDestDir: ""}

	rec := doJSON(t, newOffsiteRunTestRouter(svc), "POST", "/backup/offsite/run", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: body=%s", rec.Code, rec.Body.String())
	}
	var errBody struct {
		Code    pigtail.ErrorCode `json:"code"`
		Message string            `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if errBody.Code != pigtail.ErrInvalidRequest {
		t.Errorf("code = %q, want %q", errBody.Code, pigtail.ErrInvalidRequest)
	}
	if errBody.Message != "no offsite destination is configured" {
		t.Errorf("message = %q, want %q", errBody.Message, "no offsite destination is configured")
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.primaryCalls != 0 || fake.fallbackCalls != 0 {
		t.Errorf("RunOffsiteNow/RunOffsiteFallbackNow called (%d, %d) times, want (0, 0): "+
			"no destination means nothing to trigger", fake.primaryCalls, fake.fallbackCalls)
	}
}

// envelopeResponder mirrors internal/daemon/server.go's own writeJSON
// exactly — `{"data": ..., "meta": {"request_id": ...}}` — the standard
// response shape docs/wire-protocol.md §2 documents for every endpoint.
// This package cannot import internal/daemon to use serverResponder
// directly (see OffsiteRunner's own doc comment above for that import
// cycle: daemon -> handlers already runs the other way), so this is a
// second, deliberately narrow implementation of the one thing that matters
// for the test below — the envelope's shape — not a reimplementation of
// writeJSON's request-id plumbing.
type envelopeResponder struct{}

func (envelopeResponder) JSON(w http.ResponseWriter, _ *http.Request, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"data": data,
		"meta": map[string]interface{}{"request_id": "req_test"},
	})
}
func (envelopeResponder) Error(w http.ResponseWriter, _ *http.Request, status int, code pigtail.ErrorCode, msg string, details map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{"code": code, "message": msg, "details": details},
		"meta":  map[string]interface{}{"request_id": "req_test"},
	})
}

// The line the whole task exists to protect: none of the three response
// booleans may ever be omitted from the wire form when false. Checked on
// the raw serialized JSON a real client actually receives — through
// envelopeResponder, not testResponder — not the decoded Go struct:
// omitempty on a bool is exactly the kind of thing json.Unmarshal into a Go
// bool would hide (a missing key and an explicit false both decode to the
// zero value), while the client-side Swift
// BackupRunResult (the one type in its backup wire file keeping Swift's synthesized
// Decodable conformance) throws on the missing key instead.
//
// Ruling 5 (final whole-branch review): an earlier version of this test ran
// the handler through testResponder (this file's other tests all do, and
// rightly — they only care about the decoded payload) and inspected the raw
// JSON at the top level. testResponder writes the bare payload with no
// envelope, so that raw JSON was never a document production actually
// sends — every real response nests the payload under "data" (see
// internal/daemon/server.go's writeJSON). The three keys would have been
// found "present" at the top level even if the production envelope nested
// them one level down where this test never looked, silently losing the
// coverage the comment above claims. Routed through envelopeResponder
// instead, so the raw bytes inspected here are shaped the way a real caller
// on the other end of the socket actually receives them.
//
// Mutation: reverting `newEnvelopeOffsiteRunTestRouter` back to
// `newOffsiteRunTestRouter` (testResponder) makes this test pass again
// despite the shape mismatch — it is the router substitution itself that
// closes the gap, not any assertion below it.
func TestRunOffsiteBackupHandlerNeverOmitsAFalseBoolean(t *testing.T) {
	t.Parallel()
	st := openBackupTestStore(t)
	fake := &fakeOffsiteRunner{offsiteStarted: false, fallbackStarted: false, fallbackConfigured: false}
	svc := &Services{Store: st, Offsite: fake, OffsiteDestDir: "/Volumes/nas/pigtail"}

	r := chi.NewRouter()
	r.Post("/backup/offsite/run", RunOffsiteBackup(svc, envelopeResponder{}))

	rec := doJSON(t, r, "POST", "/backup/offsite/run", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Data == nil {
		t.Fatalf("test setup: no \"data\" key in the envelope, body=%s", rec.Body.String())
	}
	for _, key := range []string{"offsite_started", "fallback_started", "fallback_configured"} {
		v, present := envelope.Data[key]
		if !present {
			t.Errorf("key %q absent from the wire form, want present with value false", key)
			continue
		}
		if string(v) != "false" {
			t.Errorf("key %q = %s, want false", key, v)
		}
	}
}

// A nil Store or a nil Offsite (early daemon bring-up, or a test that does
// not wire one in) must not panic — same defensive shape as
// TestBackupHandlerNilStoreDoesNotPanic for GET /backup.
func TestRunOffsiteBackupHandlerNilDependenciesDoNotPanic(t *testing.T) {
	t.Parallel()
	svc := &Services{Store: nil, Offsite: nil, OffsiteDestDir: "/Volumes/nas/pigtail"}
	rec := doJSON(t, newOffsiteRunTestRouter(svc), "POST", "/backup/offsite/run", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out pigtail.BackupRunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.OffsiteStarted || out.FallbackStarted || out.FallbackConfigured {
		t.Errorf("got %+v, want all-false with nothing wired in", out)
	}
}
