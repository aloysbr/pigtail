package daemon

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/launch"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func sdkServer(t *testing.T, sdk *SDKConfig) (*Server, http.Handler) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := New(Config{SocketPath: testSocketPath(t), SDK: sdk, LaunchOverrides: func(d *launch.Deps) { d.Logf = t.Logf }}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return srv, srv.Handler()
}

func sdkGet(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := launchServe(t, h, http.MethodGet, path, nil)
	return rec.Code, rec.Body.String()
}

// No -sdk-host/-sdk-node: every SDK route answers 503 SDK_OFF.
func TestSDKUnwiredAnswersOff(t *testing.T) {
	_, h := sdkServer(t, nil)
	if code, body := sdkGet(t, h, "/sdk-sessions"); code != http.StatusServiceUnavailable || !strings.Contains(body, "SDK_OFF") {
		t.Fatalf("unwired = %d %s", code, body)
	}
}

// Host and node without -sdk-launch: reads answer, launch_enabled is false,
// and no session directory is created.
func TestSDKWiredReadOnlyByDefault(t *testing.T) {
	claude := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(claude, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, h := sdkServer(t, &SDKConfig{Host: t.TempDir(), Node: "/bin/node", Claude: claude})
	code, body := sdkGet(t, h, "/sdk-sessions")
	var env struct {
		Data pigtail.SDKSessionList `json:"data"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &env) != nil || env.Data.LaunchEnabled {
		t.Fatalf("list = %d %s", code, body)
	}
	if entries, _ := os.ReadDir(os.Getenv("XDG_STATE_HOME")); len(entries) != 0 {
		t.Fatalf("state dir not empty: %v", entries)
	}
}

// claude is resolved at start; a failure leaves the routes off with the reason.
func TestSDKClaudeNotFoundIsOffWithReason(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, h := sdkServer(t, &SDKConfig{Host: t.TempDir(), Node: "/bin/node"})
	code, body := sdkGet(t, h, "/sdk-sessions")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "SDK_OFF") || !strings.Contains(body, "claude") {
		t.Fatalf("= %d %s", code, body)
	}
}
