package handlers

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// newBackupTestRouter wires GET /backup against a file-backed store, so
// store.WriteOffsiteState/ReadOffsiteState round-trip through the same
// offsite-state.json file a real daemon would use — the handler must read
// no in-memory state at all.
func newBackupTestRouter(t *testing.T, st *store.Store) *chi.Mux {
	t.Helper()
	svc := &Services{Store: st}
	r := chi.NewRouter()
	r.Get("/backup", Backup(svc, testResponder{}))
	return r
}

func openBackupTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "pigtail.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestBackupHandlerOK(t *testing.T) {
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

	var resp pigtail.BackupResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Offsite == nil {
		t.Fatalf("offsite = nil, want a view: %s", rec.Body.String())
	}
	off := resp.Offsite
	if off.DestDir != "/Volumes/nas/pigtail" {
		t.Errorf("dest_dir = %q, want /Volumes/nas/pigtail", off.DestDir)
	}
	if off.Status != "ok" {
		t.Errorf("status = %q, want ok", off.Status)
	}
	if off.Severity != "ok" {
		t.Errorf("severity = %q, want ok", off.Severity)
	}
	if off.Detail != "ok" {
		t.Errorf("detail = %q, want %q (store.RenderOffsiteDetail's own wording)", off.Detail, "ok")
	}
	if off.LastOKAgeSeconds == nil || *off.LastOKAgeSeconds < 1190 || *off.LastOKAgeSeconds > 1210 {
		t.Errorf("last_ok_age_seconds = %v, want ~1200", off.LastOKAgeSeconds)
	}
	if off.LastTryAgeSeconds == nil || *off.LastTryAgeSeconds < 1190 || *off.LastTryAgeSeconds > 1210 {
		t.Errorf("last_try_age_seconds = %v, want ~1200", off.LastTryAgeSeconds)
	}
	if off.ConsecutiveFailures != 0 {
		t.Errorf("consecutive_failures = %d, want 0", off.ConsecutiveFailures)
	}
	if off.SameDevice {
		t.Errorf("same_device = true, want false")
	}
	if off.LastErr != "" {
		t.Errorf("last_err = %q, want empty", off.LastErr)
	}
}

// The 2026-09-06 outage is what this field exists for: three failures in a
// row must read as alarm even though nothing else in the state (a recent
// last_ok, no stuck attempt) would raise it on its own.
func TestBackupHandlerFailingThreeInARowIsAlarm(t *testing.T) {
	t.Parallel()
	st := openBackupTestStore(t)
	now := time.Now()
	if err := store.WriteOffsiteState(st.Path(), store.OffsiteState{
		DestDir:             "/Volumes/nas/pigtail",
		LastOKAt:            now.Add(-45 * time.Minute),
		LastTryAt:           now.Add(-5 * time.Minute),
		LastErr:             "offsite destination unreachable: /Volumes/nas/pigtail",
		ConsecutiveFailures: 3,
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
	off := resp.Offsite
	if off.Status != "failing" {
		t.Errorf("status = %q, want failing", off.Status)
	}
	if off.Severity != "alarm" {
		t.Errorf("severity = %q, want alarm", off.Severity)
	}
	if off.ConsecutiveFailures != 3 {
		t.Errorf("consecutive_failures = %d, want 3", off.ConsecutiveFailures)
	}
	if off.LastErr != "offsite destination unreachable: /Volumes/nas/pigtail" {
		t.Errorf("last_err = %q", off.LastErr)
	}
	// The exact wording, not just status/severity: this is the only test in
	// this file that exercises a non-trivial branch of RenderOffsiteDetail
	// (the "ok" case's detail is the literal string "ok", which a hand-rolled
	// stand-in would produce just as well). Pinning the FAILING phrase here
	// is what would catch someone swapping the RenderOffsiteDetail call for a
	// hand-written sentence tomorrow.
	const wantDetail = "FAILING — offsite destination unreachable: /Volumes/nas/pigtail"
	if off.Detail != wantDetail {
		t.Errorf("detail = %q, want %q (store.RenderOffsiteDetail's own wording)", off.Detail, wantDetail)
	}
}

// No destination configured must drop the "offsite" key entirely — not a
// null, not an empty object — mirroring formatOffsite's own rule of printing
// nothing at all. Checked on the serialized JSON, not just the Go struct:
// omitempty on a pointer is exactly the kind of thing that silently
// regresses to `"offsite":null` if the field type changes.
func TestBackupHandlerNoDestinationOmitsOffsiteKey(t *testing.T) {
	t.Parallel()
	st := openBackupTestStore(t)
	r := newBackupTestRouter(t, st)
	rec := doJSON(t, r, "GET", "/backup", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if _, present := raw["offsite"]; present {
		t.Fatalf("offsite key must be absent, got: %s", rec.Body.String())
	}
}

// A daemon whose store never opened (unusual, but not impossible in tests
// and during startup races) must still answer 200 rather than panic.
func TestBackupHandlerNilStoreDoesNotPanic(t *testing.T) {
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
