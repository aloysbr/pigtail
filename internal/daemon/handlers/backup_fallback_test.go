package handlers

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// A configured fallback that has exercised successfully must surface as its
// own "fallback" object on GET /backup, independent of (but alongside) the
// primary "offsite" verdict — the mapping backup.go's Backup handler adds on
// top of store.ClassifyOffsiteFallback.
func TestBackupHandlerFallbackHealthy(t *testing.T) {
	t.Parallel()
	st := openBackupTestStore(t)
	now := time.Now()
	if err := store.WriteOffsiteState(st.Path(), store.OffsiteState{
		DestDir:           "/Volumes/nas/pigtail",
		LastOKAt:          now.Add(-20 * time.Minute),
		LastTryAt:         now.Add(-20 * time.Minute),
		FallbackDest:      "nas-fallback:/srv/pigtail",
		FallbackLastOKAt:  now.Add(-30 * time.Minute),
		FallbackLastTryAt: now.Add(-30 * time.Minute),
		FallbackVia:       "127.0.0.1",
	}); err != nil {
		t.Fatalf("seed offsite state: %v", err)
	}

	r := newBackupTestRouter(t, st)
	rec := doJSON(t, r, "GET", "/backup", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var resp pigtail.BackupResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Offsite == nil {
		t.Fatalf("offsite = nil, want a view: %s", rec.Body.String())
	}
	fb := resp.Offsite.Fallback
	if fb == nil {
		t.Fatalf("fallback = nil, want a view: %s", rec.Body.String())
	}
	if fb.Dest != "nas-fallback:/srv/pigtail" {
		t.Errorf("dest = %q, want nas-fallback:/srv/pigtail", fb.Dest)
	}
	if fb.Status != "ok" {
		t.Errorf("status = %q, want ok", fb.Status)
	}
	if fb.Severity != "ok" {
		t.Errorf("severity = %q, want ok", fb.Severity)
	}
	const wantDetail = "ok — exercised 30m ago via 127.0.0.1"
	if fb.Detail != wantDetail {
		t.Errorf("detail = %q, want %q (store.RenderOffsiteFallbackDetail's own wording)", fb.Detail, wantDetail)
	}
	if fb.Via != "127.0.0.1" {
		t.Errorf("via = %q, want 127.0.0.1 (the healthy reading on a userspace-networking NAS)", fb.Via)
	}
	if fb.LastOKAgeSeconds == nil || *fb.LastOKAgeSeconds < 1790 || *fb.LastOKAgeSeconds > 1810 {
		t.Errorf("last_ok_age_seconds = %v, want ~1800", fb.LastOKAgeSeconds)
	}
	if fb.LastTryAgeSeconds == nil || *fb.LastTryAgeSeconds < 1790 || *fb.LastTryAgeSeconds > 1810 {
		t.Errorf("last_try_age_seconds = %v, want ~1800", fb.LastTryAgeSeconds)
	}
	if fb.ConsecutiveFailures != 0 {
		t.Errorf("consecutive_failures = %d, want 0", fb.ConsecutiveFailures)
	}
	if fb.LastErr != "" {
		t.Errorf("last_err = %q, want empty", fb.LastErr)
	}
}

// Two consecutive fallback failures must read as "warn" even while the
// primary route classifies as "ok" — the whole reason ClassifyOffsiteFallback
// exists is to answer a question the primary's own severity cannot: the
// two verdicts are independent facts, and this is the case where they must
// visibly disagree rather than the fallback silently inheriting the
// primary's "ok".
func TestBackupHandlerFallbackTwoFailuresWarnsWhilePrimaryOK(t *testing.T) {
	t.Parallel()
	st := openBackupTestStore(t)
	now := time.Now()
	if err := store.WriteOffsiteState(st.Path(), store.OffsiteState{
		DestDir:                     "/Volumes/nas/pigtail",
		LastOKAt:                    now.Add(-20 * time.Minute),
		LastTryAt:                   now.Add(-20 * time.Minute),
		FallbackDest:                "nas-fallback:/srv/pigtail",
		FallbackLastOKAt:            now.Add(-3 * time.Hour),
		FallbackLastTryAt:           now.Add(-30 * time.Minute),
		FallbackLastErr:             "dial tcp: i/o timeout",
		FallbackConsecutiveFailures: 2,
	}); err != nil {
		t.Fatalf("seed offsite state: %v", err)
	}

	r := newBackupTestRouter(t, st)
	rec := doJSON(t, r, "GET", "/backup", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var resp pigtail.BackupResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Offsite == nil {
		t.Fatalf("offsite = nil, want a view: %s", rec.Body.String())
	}
	if resp.Offsite.Severity != "ok" {
		t.Fatalf("offsite.severity = %q, want ok (the primary route itself is healthy)", resp.Offsite.Severity)
	}
	fb := resp.Offsite.Fallback
	if fb == nil {
		t.Fatalf("fallback = nil, want a view: %s", rec.Body.String())
	}
	if fb.Status != "failing" {
		t.Errorf("status = %q, want failing", fb.Status)
	}
	if fb.Severity != "warn" {
		t.Errorf("severity = %q, want warn (2 consecutive failures, primary is ok so nothing mutes it)", fb.Severity)
	}
	if fb.ConsecutiveFailures != 2 {
		t.Errorf("consecutive_failures = %d, want 2", fb.ConsecutiveFailures)
	}
	if fb.LastErr != "dial tcp: i/o timeout" {
		t.Errorf("last_err = %q, want %q", fb.LastErr, "dial tcp: i/o timeout")
	}
	const wantDetail = "FALLBACK FAILING — 2 in a row: dial tcp: i/o timeout"
	if fb.Detail != wantDetail {
		t.Errorf("detail = %q, want %q (store.RenderOffsiteFallbackDetail's own wording)", fb.Detail, wantDetail)
	}
}

// No fallback destination configured must drop the "fallback" key entirely
// from the serialized JSON — not a null, not an empty object — mirroring the
// same rule GET /backup's "offsite" key already follows. Checked on the raw
// bytes, not the Go struct: a nil pointer in Go proves nothing about what
// omitempty actually produced on the wire.
func TestBackupHandlerNoFallbackConfiguredOmitsFallbackKeyFromRawJSON(t *testing.T) {
	t.Parallel()
	st := openBackupTestStore(t)
	now := time.Now()
	if err := store.WriteOffsiteState(st.Path(), store.OffsiteState{
		DestDir:   "/Volumes/nas/pigtail",
		LastOKAt:  now.Add(-20 * time.Minute),
		LastTryAt: now.Add(-20 * time.Minute),
	}); err != nil {
		t.Fatalf("seed offsite state: %v", err)
	}

	r := newBackupTestRouter(t, st)
	rec := doJSON(t, r, "GET", "/backup", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	offsiteRaw, present := raw["offsite"]
	if !present {
		t.Fatalf("offsite key absent, want present (a primary destination is configured): %s", rec.Body.String())
	}
	var offsite map[string]json.RawMessage
	if err := json.Unmarshal(offsiteRaw, &offsite); err != nil {
		t.Fatalf("decode raw offsite: %v", err)
	}
	if _, present := offsite["fallback"]; present {
		t.Fatalf("fallback key must be absent when no fallback destination is configured, got: %s", rec.Body.String())
	}
}

// A daemon whose store never opened must still answer 200 rather than
// panic once the fallback classification is wired in — the same guarantee
// backup_test.go's TestBackupHandlerNilStoreDoesNotPanic pins for the
// primary route, exercised again here because the new fallback mapping in
// Backup is exactly the kind of added code path that could reintroduce a
// nil-store panic if it stopped checking svc.Store first.
func TestBackupHandlerNilStoreDoesNotPanicWithFallbackMapping(t *testing.T) {
	t.Parallel()
	svc := &Services{Store: nil}
	r := chi.NewRouter()
	r.Get("/backup", Backup(svc, testResponder{}))

	rec := doJSON(t, r, "GET", "/backup", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if _, present := raw["offsite"]; present {
		t.Fatalf("offsite key must be absent with a nil store, got: %s", rec.Body.String())
	}
}
