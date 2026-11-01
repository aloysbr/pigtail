package staticsrv

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// freePort grabs a free TCP port by binding :0 and releasing it.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen :0: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func tempDirWithFile(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	return dir
}

// getOnce GETs a URL, retrying briefly while the goroutine spins up.
func getOnce(t *testing.T, url string) (int, string) {
	t.Helper()
	var lastErr error
	for i := 0; i < 50; i++ {
		resp, err := http.Get(url) //nolint:gosec // loopback test URL
		if err != nil {
			lastErr = err
			time.Sleep(10 * time.Millisecond)
			continue
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	t.Fatalf("GET %s never succeeded: %v", url, lastErr)
	return 0, ""
}

func TestStartServeStop(t *testing.T) {
	dir := tempDirWithFile(t, "hello.txt", "hi there")
	port := freePort(t)
	m := New()
	ss := pigtail.StaticServer{Name: "demo", Dir: dir, Port: port}

	if err := m.Start(ss); err != nil {
		t.Fatalf("Start: %v", err)
	}

	url := "http://127.0.0.1:" + itoa(port) + "/hello.txt"
	code, body := getOnce(t, url)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body != "hi there" {
		t.Fatalf("body = %q, want %q", body, "hi there")
	}

	if p, ok := m.Running("demo"); !ok || p != port {
		t.Fatalf("Running = (%d,%v), want (%d,true)", p, ok, port)
	}
	if names := m.List(); len(names) != 1 || names[0] != "demo" {
		t.Fatalf("List = %v, want [demo]", names)
	}

	if err := m.Stop("demo"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, ok := m.Running("demo"); ok {
		t.Fatalf("still running after Stop")
	}

	// Port must be free again: we should be able to bind it.
	ln, err := net.Listen("tcp", "127.0.0.1:"+itoa(port))
	if err != nil {
		t.Fatalf("port %d not free after Stop: %v", port, err)
	}
	_ = ln.Close()
}

func TestDoubleStart(t *testing.T) {
	dir := tempDirWithFile(t, "x.txt", "x")
	port := freePort(t)
	m := New()
	ss := pigtail.StaticServer{Name: "dup", Dir: dir, Port: port}
	if err := m.Start(ss); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer m.StopAll()
	if err := m.Start(ss); err == nil {
		t.Fatalf("second Start should fail (already running)")
	}
}

func TestMissingDir(t *testing.T) {
	m := New()
	ss := pigtail.StaticServer{Name: "nope", Dir: filepath.Join(t.TempDir(), "does-not-exist"), Port: freePort(t)}
	if err := m.Start(ss); err == nil {
		t.Fatalf("Start should fail for missing dir")
	}
}

func TestStartFileNotDir(t *testing.T) {
	dir := tempDirWithFile(t, "afile", "data")
	m := New()
	ss := pigtail.StaticServer{Name: "file", Dir: filepath.Join(dir, "afile"), Port: freePort(t)}
	if err := m.Start(ss); err == nil {
		t.Fatalf("Start should fail when dir is a file")
	}
}

func TestStopNotRunning(t *testing.T) {
	m := New()
	if err := m.Stop("ghost"); err == nil {
		t.Fatalf("Stop of unknown server should error")
	}
}

func TestStopAll(t *testing.T) {
	m := New()
	for _, n := range []string{"a", "b"} {
		dir := tempDirWithFile(t, "i.txt", n)
		if err := m.Start(pigtail.StaticServer{Name: n, Dir: dir, Port: freePort(t)}); err != nil {
			t.Fatalf("Start %s: %v", n, err)
		}
	}
	if len(m.List()) != 2 {
		t.Fatalf("expected 2 running")
	}
	m.StopAll()
	if len(m.List()) != 0 {
		t.Fatalf("StopAll left %v", m.List())
	}
}

// itoa avoids importing strconv across many call sites in tests.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
