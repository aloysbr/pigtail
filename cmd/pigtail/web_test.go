package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// startWebDaemon serves a fake POST /web/login-code on a Unix socket.
func startWebDaemon(t *testing.T, answer pigtail.WebLoginCode) *int {
	t.Helper()
	testutil.SerialOnly(t) // swaps flagHTTP
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("POST /web/login-code", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": answer})
	})
	dir := testutil.MkdirTemp(t, "web-") // short: the 104-byte socket path cap
	sock := filepath.Join(dir, "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	prevHTTP, prevSocket := flagHTTP, flagSocket
	flagHTTP, flagSocket = "", sock
	t.Cleanup(func() {
		flagHTTP, flagSocket = prevHTTP, prevSocket
		_ = srv.Close()
		_ = os.RemoveAll(dir)
	})
	return &calls
}

func runWeb(t *testing.T) (string, string, error) {
	t.Helper()
	cmd := webCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(nil)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true // as under the root (silenceUsageOnRunE)
	err := cmd.Execute()
	return out.String(), errb.String(), err
}

func TestWebPrintsLoginURL(t *testing.T) {
	calls := startWebDaemon(t, pigtail.WebLoginCode{Code: "c0de-XYZ_123", HTTPAddr: "127.0.0.1:7811"})
	out, _, err := runWeb(t)
	if err != nil {
		t.Fatal(err)
	}
	if want := "http://127.0.0.1:7811/web/login?code=c0de-XYZ_123\n"; out != want {
		t.Fatalf("stdout = %q, want exactly %q", out, want)
	}
	if *calls != 1 {
		t.Fatalf("login-code called %d times, want 1", *calls)
	}
}

func TestWebNeverReferencesOpenBrowser(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("web.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("web.go is empty: the check would prove nothing")
	}
	if strings.Contains(string(b), "openBrowser") {
		t.Fatal("web.go references openBrowser: the web command never opens a tab")
	}
}

func TestWebRefusesHTTPClient(t *testing.T) {
	calls := startWebDaemon(t, pigtail.WebLoginCode{Code: "x", HTTPAddr: "127.0.0.1:7811"})
	flagHTTP = "127.0.0.1:7811"
	out, _, err := runWeb(t)
	if err == nil || !strings.Contains(err.Error(), "socket") {
		t.Fatalf("with --http: err = %v, want a refusal naming the socket", err)
	}
	if out != "" || *calls != 0 {
		t.Fatalf("with --http: stdout %q, %d daemon calls; want none", out, *calls)
	}
}
