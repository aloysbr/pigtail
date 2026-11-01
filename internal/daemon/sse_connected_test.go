package daemon

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
)

// TestEventsStreamSendsBytesAtOnce (D29): with nothing to replay, the first
// bytes of /events must arrive at once, not with the first ": ping" 15 s
// later — URLSession.bytes(for:) only returns with the first data, so
// Pigtail would log "connected" and re-read GET /lid late.
func TestEventsStreamSendsBytesAtOnce(t *testing.T) {
	srv, err := New(Config{SocketPath: testSocketPath(t)}, openTestStore(t), events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.router)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/events?types=lid.*")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	first := make(chan string, 1)
	r := bufio.NewReader(resp.Body)
	go func() {
		line, _ := r.ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first:
		if line != ": connected\n" {
			t.Fatalf("first line = %q, want %q", line, ": connected\n")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no bytes on /events within 2s: the client waits for the first ping")
	}
}
