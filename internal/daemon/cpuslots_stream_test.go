package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// TestAcquireCPUSlotFlushesOneNewlineTerminatedLine pins the wire shape the
// Node client depends on, on the side that actually produces it.
//
// clients/node/client.mjs buffers the response until it sees a '\n', then
// parses everything before it. That newline exists only because writeJSON
// (server.go) encodes with json.NewEncoder().Encode, which appends one —
// nothing else states the contract. The Node test drives a hand-written fake
// that emits '\n' explicitly, and the handler unit test uses a testResponder
// that writes the payload unwrapped, so neither of them would notice
// writeJSON switching to json.Marshal + w.Write. The consequence of that
// switch is invisible and expensive: every test file would sit for the
// client's full 30 s timeout before failing open, the suite would stay green,
// and `pigtail test` would just become unusably slow.
//
// So this drives the real router, the real serverResponder and a real Unix
// socket listener — the exact transport the Node client uses — and asserts on
// the first flushed bytes.
func TestAcquireCPUSlotFlushesOneNewlineTerminatedLine(t *testing.T) {
	sock := testSocketPath(t)
	cfg := Config{SocketPath: sock, HTTPAddr: "", CPUSlots: 2}
	s, err := New(cfg, openTestStore(t), events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	srv := &http.Server{Handler: s.router, ReadHeaderTimeout: 10 * time.Second}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		<-done
	})

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://pigtail/cpu/slots",
		strings.NewReader(`{"group":"ws_test:vitest","label":"a.test.ts","weight":1}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pigtail-API", pigtail.APIVersion)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /cpu/slots: %v", err)
	}
	defer resp.Body.Close() // closing the body is the release: the connection is the slot
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// One Read returns exactly what the handler flushed, because the handler
	// then blocks on the request context rather than writing more or closing.
	buf := make([]byte, 4096)
	n, err := resp.Body.Read(buf)
	if err != nil {
		t.Fatalf("read the flushed grant: %v", err)
	}
	first := buf[:n]

	if !bytes.HasSuffix(first, []byte("\n")) {
		t.Fatalf("the flushed grant must end in a newline — clients/node/client.mjs waits for one "+
			"before parsing, and without it every test file waits out its 30s timeout:\n%q", first)
	}
	if bytes.Count(first, []byte("\n")) != 1 {
		t.Errorf("the grant should be exactly one line, got %q", first)
	}

	var env struct {
		Data pigtail.CPUSlotGrant `json:"data"`
	}
	if err := json.Unmarshal(bytes.TrimSuffix(first, []byte("\n")), &env); err != nil {
		t.Fatalf("the flushed line must parse on its own: %v\n%q", err, first)
	}
	if !env.Data.Granted {
		t.Errorf("granted = false, want true:\n%q", first)
	}
}
