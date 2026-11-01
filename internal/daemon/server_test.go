package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
)

func TestHealth(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv, err := New(Config{
		SocketPath: testSocketPath(t),
		HTTPAddr:   "", // socket only
	}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer startDaemon(t, srv)()

	// wait for socket
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := net.Dial("unix", srv.cfg.SocketPath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	httpc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", srv.cfg.SocketPath)
		},
	}}
	resp, err := httpc.Get("http://pigtail/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var env struct {
		Data struct {
			OK            bool   `json:"ok"`
			DaemonVersion string `json:"daemon_version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v body=%s", err, string(body))
	}
	if !env.Data.OK {
		t.Fatalf("ok=false in body=%s", string(body))
	}
}

// TestWatchdogWiring proves the runaway watchdog is constructed by default
// and suppressed by Config.WatchdogDisabled.
func TestWatchdogWiring(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	on, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if on.watchdog == nil {
		t.Error("watchdog: nil with default config, want constructed")
	}

	off, err := New(Config{SocketPath: testSocketPath(t), WatchdogDisabled: true}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if off.watchdog != nil {
		t.Error("watchdog: constructed despite WatchdogDisabled")
	}
}

func TestIsPublicPath(t *testing.T) {
	// The web front's browser paths are public too: their handlers check the
	// Host and the cookie (web.go). Its login-code route is not.
	public := []string{"/board", "/board/", "/board/static/app.css", "/", "/web/login", "/assets/app-abc.js"}
	for _, p := range public {
		if !isPublicPath(p) {
			t.Errorf("isPublicPath(%q) = false, want true", p)
		}
	}
	private := []string{"/subjects", "/subjects/next", "/workspaces", "/dispatch", "/boardroom",
		"/web/login-code", "/cockpit/login", "/web", "/web/login/x", "/webx/login", "/assets", "/assetsx/a.js", "//"}
	for _, p := range private {
		if isPublicPath(p) {
			t.Errorf("isPublicPath(%q) = true, want false", p)
		}
	}
}

func TestDaemonWiresCwdAttribution(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	srv, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !srv.ramMonitor.AttributesByCwd() {
		t.Fatal("the daemon's RAM monitor must attribute listeners by cwd")
	}
}
