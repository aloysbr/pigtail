package daemon

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
)

// TestEventsStreamEndsAtShutdown: an open /events subscriber (Pigtail keeps
// one for its whole life) must not hold Run for Shutdown's 5 s cap, and must
// see daemon.shutting_down before a clean end of stream.
func TestEventsStreamEndsAtShutdown(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	srv, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Not startDaemon: its stop waits up to daemonStopTimeout; the duration
	// of Run's return is what this test measures.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", srv.cfg.SocketPath); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	httpc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", srv.cfg.SocketPath)
		},
	}}

	// On the base the connection outlives Run: the cleanup closes it.
	reqCtx, reqCancel := context.WithCancel(context.Background())
	t.Cleanup(reqCancel)
	req, _ := http.NewRequestWithContext(reqCtx, "GET", "http://pigtail/events", nil)
	resp, err := httpc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	type end struct {
		err   error
		types []string
	}
	ended := make(chan end, 1)
	sc := bufio.NewScanner(resp.Body)
	go func() {
		var types []string
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "event: ") {
				types = append(types, strings.TrimPrefix(line, "event: "))
			}
		}
		ended <- end{sc.Err(), types}
	}()

	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return within 10s")
	}
	if took := time.Since(start); took >= 2*time.Second {
		t.Errorf("Run returned after %.1fs, want < 2s: an /events subscriber held Shutdown", took.Seconds())
	}

	select {
	case e := <-ended:
		if e.err != nil {
			t.Fatalf("stream ended with %v, want a clean end", e.err)
		}
		found := false
		for _, typ := range e.types {
			if typ == "daemon.shutting_down" {
				found = true
			}
		}
		if !found {
			t.Fatalf("events before the end = %v, want daemon.shutting_down", e.types)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not end within 2s")
	}
}
