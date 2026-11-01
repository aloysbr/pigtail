package daemon

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/lid"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// fakeLidRunner stands in for lid.ExecRunner: no test server ever runs pmset.
type fakeLidRunner struct {
	mu       sync.Mutex
	disabled bool
}

func (f *fakeLidRunner) ReadPM() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := 0
	if f.disabled {
		v = 1
	}
	return fmt.Sprintf(" SleepDisabled\t\t%d\n", v), nil
}

func (f *fakeLidRunner) SetSleepDisabled(on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disabled = on
	return nil
}

func (f *fakeLidRunner) ProbeSet(bool) error { return nil }

func TestLidWiredOnlyWithRunner(t *testing.T) {
	s, err := New(Config{SocketPath: testSocketPath(t)}, openTestStore(t), events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("GET", "/lid", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("no runner: GET /lid = %d, want 501", rec.Code)
	}

	s, err = New(Config{SocketPath: testSocketPath(t), LidRunner: &fakeLidRunner{disabled: true}}, openTestStore(t), events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("GET", "/lid", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"sleep_disabled":true`) {
		t.Fatalf("with runner: GET /lid = %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("POST", "/lid", strings.NewReader(`{"on":true,"hours":1}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /lid = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(lid.StatePath(s.cfg.DBPath)); err != nil {
		t.Fatalf("lid state not next to the database: %v", err)
	}
}

// nextLidChange waits up to 2 s for the next event on sub; the literal
// "lid.changed" (not the constant) keeps the red on the base behavioural.
func nextLidChange(t *testing.T, sub *events.Subscription) pigtail.Event {
	t.Helper()
	select {
	case e := <-sub.C:
		if e.Type != "lid.changed" {
			t.Fatalf("event type = %q, want lid.changed", e.Type)
		}
		if e.Scope != "" {
			t.Fatalf("lid.changed scoped %q, want unscoped", e.Scope)
		}
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no lid.changed event within 2s")
	}
	return pigtail.Event{}
}

func wantLidChange(t *testing.T, e pigtail.Event, on bool, reason string) {
	t.Helper()
	if e.Data["on"] != on || e.Data["reason"] != reason {
		t.Fatalf("lid.changed data = %v, want on=%v reason=%q", e.Data, on, reason)
	}
}

func TestLidChangePublishedOnBus(t *testing.T) {
	bus := events.New()
	runner := &fakeLidRunner{}
	s, err := New(Config{SocketPath: testSocketPath(t), LidRunner: runner}, openTestStore(t), bus, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sub := bus.Subscribe("lid.*")
	defer sub.Close()
	post := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, httptest.NewRequest("POST", "/lid", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("POST /lid %s = %d %s", body, rec.Code, rec.Body.String())
		}
	}

	post(`{"on":true,"hours":1}`)
	wantLidChange(t, nextLidChange(t, sub), true, "on")
	post(`{"on":false}`)
	wantLidChange(t, nextLidChange(t, sub), false, "manual")

	// Auto-off by the tick: sleep restored outside pigtail.
	post(`{"on":true,"hours":1}`)
	wantLidChange(t, nextLidChange(t, sub), true, "on")
	runner.mu.Lock()
	runner.disabled = false
	runner.mu.Unlock()
	s.lid.Tick()
	wantLidChange(t, nextLidChange(t, sub), false, "external")
}

func TestLidChangeReachesEventsStream(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	srv, err := New(Config{SocketPath: testSocketPath(t), LidRunner: &fakeLidRunner{}}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer startDaemon(t, srv)()

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

	// No X-Pigtail-Scope header: a work-like subscriber, as Pigtail.
	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()
	req, _ := http.NewRequestWithContext(reqCtx, "GET", "http://pigtail/events?types=lid.*", nil)
	resp, err := httpc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	lines := make(chan string, 64)
	sc := bufio.NewScanner(resp.Body)
	go func() {
		defer close(lines)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()

	postResp, err := httpc.Post("http://pigtail/lid", "application/json", strings.NewReader(`{"on":true,"hours":1}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = postResp.Body.Close()
	if postResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /lid = %d", postResp.StatusCode)
	}

	timeout := time.After(2 * time.Second)
	gotEvent := false
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("stream ended before lid.changed")
			}
			if line == "event: lid.changed" {
				gotEvent = true
				continue
			}
			if gotEvent && strings.HasPrefix(line, "data:") {
				if !strings.Contains(line, `"on":true`) {
					t.Fatalf("data line = %q, want \"on\":true", line)
				}
				return
			}
		case <-timeout:
			t.Fatal("no lid.changed on /events within 2s")
		}
	}
}
