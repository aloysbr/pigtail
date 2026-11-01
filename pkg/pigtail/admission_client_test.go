package pigtail

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/testutil"
)

// unixServer serves h on a real Unix socket, the transport pigtail uses.
func unixServer(t *testing.T, h http.Handler) *Client {
	t.Helper()
	// Short, not t.TempDir(): Unix socket paths are capped at 104 bytes on
	// darwin, and /var/folders/… overshoots it.
	dir := testutil.MkdirTemp(t, "adm-")
	sock := filepath.Join(dir, "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: h}}
	srv.Start()
	t.Cleanup(srv.Close)
	return NewSocketClient(sock)
}

func writeLine(w http.ResponseWriter, u AdmissionUpdate) {
	_ = json.NewEncoder(w).Encode(map[string]any{"data": u})
	w.(http.Flusher).Flush()
}

func TestAcquireAdmissionQueuedThenGranted(t *testing.T) {
	var released atomic.Bool
	c := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req AdmissionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		_, _ = io.Copy(io.Discard, r.Body)
		if req.Group != "wt" || req.Class != "tsc" {
			http.Error(w, "bad request", 400)
			return
		}
		writeLine(w, AdmissionUpdate{State: AdmissionQueued, Position: 1, Reason: "memory"})
		writeLine(w, AdmissionUpdate{State: AdmissionGranted, LeaseID: "adm-1", WaitedMS: 5})
		<-r.Context().Done()
		released.Store(true)
	}))

	var queued []AdmissionUpdate
	lease, err := c.AcquireAdmission(context.Background(), AdmissionRequest{Group: "wt", Class: "tsc"},
		func(u AdmissionUpdate) { queued = append(queued, u) })
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0].Reason != "memory" || lease.Grant.LeaseID != "adm-1" {
		t.Fatalf("queued=%+v grant=%+v", queued, lease.Grant)
	}
	lease.Release()
	lease.Release() // idempotent
	deadline := time.Now().Add(2 * time.Second)
	for !released.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the server never saw the lease connection close")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAcquireAdmissionWaitBudgetRunsOut(t *testing.T) {
	c := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		writeLine(w, AdmissionUpdate{State: AdmissionQueued, Position: 1})
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.AcquireAdmission(ctx, AdmissionRequest{Group: "wt"}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the caller's deadline", err)
	}
}

func TestAcquireAdmissionDisabledIsAnAPIError(t *testing.T) {
	c := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": ErrAdmissionDisabled, "message": "off"}})
	}))
	_, err := c.AcquireAdmission(context.Background(), AdmissionRequest{Group: "wt"}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != ErrAdmissionDisabled {
		t.Fatalf("err = %v, want ADMISSION_DISABLED", err)
	}
}

func TestAcquireAdmissionNoDaemon(t *testing.T) {
	c := NewSocketClient(filepath.Join(t.TempDir(), "absent.sock"))
	start := time.Now()
	_, err := c.AcquireAdmission(context.Background(), AdmissionRequest{Group: "wt"}, nil)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a connection error", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("an absent daemon must fail fast")
	}
}

func TestAcquireAdmissionUnresponsiveDaemon(t *testing.T) {
	prev := admissionFirstLineTimeout
	admissionFirstLineTimeout = 50 * time.Millisecond
	defer func() { admissionFirstLineTimeout = prev }()
	c := unixServer(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// Drained so the server notices the client leaving (see
		// handlers.decodeLeaseBody); then accept and never answer.
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	_, err := c.AcquireAdmission(context.Background(), AdmissionRequest{Group: "wt"}, nil)
	if !errors.Is(err, ErrAdmissionUnresponsive) {
		t.Fatalf("err = %v, want ErrAdmissionUnresponsive", err)
	}
}

// An older daemon has no exit route: Finish must still hand the lease back,
// promptly, whatever the daemon answers.
func TestLeaseFinishIgnoresAnOldDaemon(t *testing.T) {
	released := make(chan struct{})
	c := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/exit") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"data":{"state":"granted","lease_id":"adm-1"}}` + "\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done() // the connection is the lease
		close(released)
	}))

	lease, err := c.AcquireAdmission(context.Background(), AdmissionRequest{Group: "g", Class: "tsc"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	lease.Finish(3)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Finish took %s against a daemon without the route", d)
	}
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("Finish did not close the lease")
	}
}
