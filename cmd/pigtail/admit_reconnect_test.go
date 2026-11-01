package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// scriptedDaemon serves POST /admission/leases with h(n, w), n the 1-based
// request count, on a Unix socket this process's client points at.
func scriptedDaemon(t *testing.T, h func(n int, w http.ResponseWriter)) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	dir := testutil.MkdirTemp(t, "recon-")
	ln, err := net.Listen("unix", filepath.Join(dir, "p.sock"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		h(int(hits.Add(1)), w)
	})}
	go func() { _ = srv.Serve(ln) }()
	pointClientAt(t, filepath.Join(dir, "p.sock"))
	t.Cleanup(func() { _ = srv.Close(); _ = os.RemoveAll(dir) })
	return &hits
}

func line(w http.ResponseWriter, u pigtail.AdmissionUpdate) {
	_ = json.NewEncoder(w).Encode(map[string]any{"data": u})
	w.(http.Flusher).Flush()
}

// cut ends the stream the way a dying daemon does: no line, no body.
func cut(w http.ResponseWriter) {
	c, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		_ = c.Close()
	}
}

func fastReconnect(t *testing.T, every, window time.Duration) {
	t.Helper()
	pe, pw := admitReconnectEvery, admitReconnectWindow
	admitReconnectEvery, admitReconnectWindow = every, window
	t.Cleanup(func() { admitReconnectEvery, admitReconnectWindow = pe, pw })
}

func TestAdmitReconnectsAfterStreamBreakWhileQueued(t *testing.T) {
	fastReconnect(t, 10*time.Millisecond, 5*time.Second)
	scriptedDaemon(t, func(n int, w http.ResponseWriter) {
		line(w, pigtail.AdmissionUpdate{State: pigtail.AdmissionQueued, Position: 1, Queued: 1})
		if n == 1 {
			return // the stream ends: the daemon restarted
		}
		line(w, pigtail.AdmissionUpdate{State: pigtail.AdmissionGranted, LeaseID: "adm-9"})
		time.Sleep(time.Second)
	})
	var stderr bytes.Buffer
	lease, abort := admit(&stderr, t.TempDir(), admitOptions{class: "go-test", maxWait: 10 * time.Second, onTimeout: "fail"})
	if abort || lease == nil {
		t.Fatalf("lease=%v abort=%v stderr=%q", lease, abort, stderr.String())
	}
	defer lease.Release()
	if strings.Contains(stderr.String(), "unadmitted") {
		t.Fatalf("must not run unadmitted: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "daemon gone while queued — reconnecting") {
		t.Fatalf("no reconnect notice: %q", stderr.String())
	}
}

func TestAdmitRunsUnadmittedAfterReconnectWindow(t *testing.T) {
	fastReconnect(t, 10*time.Millisecond, 150*time.Millisecond)
	scriptedDaemon(t, func(n int, w http.ResponseWriter) {
		if n == 1 {
			line(w, pigtail.AdmissionUpdate{State: pigtail.AdmissionQueued, Position: 1, Queued: 1})
			return
		}
		cut(w)
	})
	var stderr bytes.Buffer
	lease, abort := admit(&stderr, t.TempDir(), admitOptions{class: "go-test", maxWait: 10 * time.Second, onTimeout: "fail"})
	if abort || lease != nil {
		t.Fatalf("lease=%v abort=%v, want to run unadmitted", lease, abort)
	}
	if !strings.Contains(stderr.String(), "daemon still gone after") || !strings.Contains(stderr.String(), "running unadmitted") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestAdmitReconnectRespectsMaxWait(t *testing.T) {
	fastReconnect(t, 10*time.Millisecond, 10*time.Second)
	scriptedDaemon(t, func(n int, w http.ResponseWriter) {
		if n == 1 {
			line(w, pigtail.AdmissionUpdate{State: pigtail.AdmissionQueued, Position: 1, Queued: 1})
			return
		}
		cut(w)
	})
	var stderr bytes.Buffer
	lease, abort := admit(&stderr, t.TempDir(), admitOptions{class: "go-test", maxWait: 200 * time.Millisecond, onTimeout: "fail"})
	if !abort || lease != nil {
		t.Fatalf("lease=%v abort=%v, want exit 75", lease, abort)
	}
}

func TestAdmitReconnectStopsOnAdmissionDisabled(t *testing.T) {
	fastReconnect(t, 10*time.Millisecond, 5*time.Second)
	scriptedDaemon(t, func(n int, w http.ResponseWriter) {
		if n == 1 {
			line(w, pigtail.AdmissionUpdate{State: pigtail.AdmissionQueued, Position: 1, Queued: 1})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": pigtail.ErrAdmissionDisabled, "message": "off"}})
	})
	var stderr bytes.Buffer
	lease, abort := admit(&stderr, t.TempDir(), admitOptions{class: "go-test", maxWait: 10 * time.Second, onTimeout: "fail"})
	if abort || lease != nil {
		t.Fatalf("lease=%v abort=%v, want to run unadmitted", lease, abort)
	}
	if strings.Contains(stderr.String(), "unadmitted") || strings.Contains(stderr.String(), "still gone") {
		t.Fatalf("a disabled admission is silent, got %q", stderr.String())
	}
}

func TestAdmitReconnectWindowDoesNotRestart(t *testing.T) {
	fastReconnect(t, 10*time.Millisecond, 300*time.Millisecond)
	scriptedDaemon(t, func(n int, w http.ResponseWriter) {
		switch n {
		case 1:
			line(w, pigtail.AdmissionUpdate{State: pigtail.AdmissionQueued, Position: 1, Queued: 1})
		case 2: // the reconnection queues, then the daemon goes again
			line(w, pigtail.AdmissionUpdate{State: pigtail.AdmissionQueued, Position: 1, Queued: 1})
			time.Sleep(100 * time.Millisecond)
		default:
			cut(w)
		}
	})
	var stderr bytes.Buffer
	lease, abort := admit(&stderr, t.TempDir(), admitOptions{class: "go-test", maxWait: 10 * time.Second, onTimeout: "fail"})
	if abort || lease != nil {
		t.Fatalf("lease=%v abort=%v", lease, abort)
	}
	if n := strings.Count(stderr.String(), "reconnecting"); n != 1 {
		t.Fatalf("the notice (and the window) starts once, got %d: %q", n, stderr.String())
	}
	if n := strings.Count(stderr.String(), "daemon still gone after"); n != 1 {
		t.Fatalf("the window ends once, got %d: %q", n, stderr.String())
	}
}

func TestAdmitReconnectStopsOnDaemonAnswer(t *testing.T) {
	fastReconnect(t, 10*time.Millisecond, 5*time.Second)
	hits := scriptedDaemon(t, func(n int, w http.ResponseWriter) {
		if n == 1 {
			line(w, pigtail.AdmissionUpdate{State: pigtail.AdmissionQueued, Position: 1, Queued: 1})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "internal", "message": "boom"}})
	})
	var stderr bytes.Buffer
	lease, abort := admit(&stderr, t.TempDir(), admitOptions{class: "go-test", maxWait: 10 * time.Second, onTimeout: "fail"})
	if abort || lease != nil {
		t.Fatalf("lease=%v abort=%v, want to run unadmitted", lease, abort)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("daemon hit %d times, want 2: an answer from the daemon is not retried", n)
	}
	if !strings.Contains(stderr.String(), "admission unavailable") || strings.Contains(stderr.String(), "still gone") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestIsTransportErr(t *testing.T) {
	yes := []error{pigtail.ErrAdmissionUnresponsive, io.EOF, fmt.Errorf("admission stream: %w", io.ErrUnexpectedEOF),
		&url.Error{Op: "Post", Err: syscall.ECONNREFUSED}, fmt.Errorf("x: %w", syscall.ECONNRESET)}
	no := []error{nil, &pigtail.APIError{Code: "internal"}, fmt.Errorf("w: %w", &pigtail.APIError{Code: "x"}),
		errors.New("pigtail: HTTP 502: bad gateway")}
	for _, e := range yes {
		if !isTransportErr(e) {
			t.Errorf("%v must be retried", e)
		}
	}
	for _, e := range no {
		if isTransportErr(e) {
			t.Errorf("%v must not be retried", e)
		}
	}
}
