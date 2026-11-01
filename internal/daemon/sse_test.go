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
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestSSEStream(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	bus := events.New()
	srv, err := New(Config{SocketPath: testSocketPath(t)}, st, bus, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer startDaemon(t, srv)()

	// wait for socket
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

	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()
	req, _ := http.NewRequestWithContext(reqCtx, "GET", "http://pigtail/events", nil)
	resp, err := httpc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// The subscription exists once the headers are here: the handler
	// subscribes (sse.go, bus.Subscribe) before its first Flush sends them.
	bus.Publish(pigtail.Event{Type: pigtail.EventWorkspaceCreated})

	sc := bufio.NewScanner(resp.Body)
	gotType := ""
	deadline2 := time.After(2 * time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "event: workspace.created") {
				gotType = "workspace.created"
				return
			}
		}
	}()
	select {
	case <-done:
	case <-deadline2:
		t.Fatal("timeout waiting for SSE event")
	}
	if gotType != "workspace.created" {
		t.Fatalf("expected workspace.created, got %q", gotType)
	}
}
