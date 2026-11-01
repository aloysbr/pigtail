package main

import (
	"context"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/testutil"
)

// -cmux-socket aims every cmux call of the daemon at one cmux instance (a
// second one, cmux NIGHTLY, for a live pass): absolute, ≤ 103 bytes (a macOS
// Unix socket path), refused at config() otherwise.
func TestCmuxSocketFlagRejects(t *testing.T) {
	long := "/" + strings.Repeat("a", 103) // 104 bytes
	for _, tc := range []struct {
		val, wantErr string
	}{
		{"rel.sock", "absolute"},
		{long, "103"},
		{"", ""},
		{"/tmp/cmux-nightly.sock", ""},
	} {
		fs := flag.NewFlagSet("pigtaild", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		f := registerFlags(fs)
		if err := fs.Parse([]string{"-cmux-socket", tc.val}); err != nil {
			t.Fatalf("%q: parse: %v", tc.val, err)
		}
		_, err := f.config()
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%q: unexpected error %v", tc.val, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%q: error %v, want one containing %q", tc.val, err, tc.wantErr)
		}
	}
}

// TestRunAimsCmuxSocket: started with -cmux-socket, the real run() hands the
// cmux calls exactly CMUX_SOCKET_PATH=<value>, never the caller's pane ids.
// Its directory is a short one under /tmp: a daemon socket under t.TempDir()
// can pass the 103-byte macOS limit.
func TestRunAimsCmuxSocket(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a daemon child process")
	}
	dir := testutil.MkdirTemp(t, "pa-")
	aimed := filepath.Join(dir, "n.sock")
	fifo := filepath.Join(dir, "probe.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "cmux")
	script := "#!/bin/sh\nif mkdir \"$PROBE_DIR/once\" 2>/dev/null; then env > \"$PROBE_FIFO\"; fi\nprintf '{\"windows\":[]}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	for _, d := range []string{home, filepath.Join(dir, "config"), filepath.Join(dir, "state"), filepath.Join(dir, "d")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperRunDaemon$")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"PIGTAILD_HELPER=1",
		"PIGTAILD_HELPER_DIR=" + filepath.Join(dir, "d"),
		"PIGTAILD_HELPER_CMUX_SOCKET=" + aimed,
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(dir, "config"),
		"XDG_STATE_HOME=" + filepath.Join(dir, "state"),
		"CMUX_SURFACE_ID=80914656-BF88-4780-9982-CA1665755494",
		"CMUX_WORKSPACE_ID=B89B4597-2397-45F6-81D9-95F3B0FA83F8",
		"CMUX_SOCKET_PATH=" + filepath.Join(dir, "main.sock"), // the caller's pane: replaced by the aim
		cmux.EnvBundledCLI + "=" + bin,
		"PROBE_FIFO=" + fifo,
		"PROBE_DIR=" + dir,
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	got := make(chan []byte, 1)
	go func() {
		b, _ := os.ReadFile(fifo)
		got <- b
	}()
	var dump string
	select {
	case b := <-got:
		dump = string(b)
	case <-time.After(20 * time.Second):
		t.Fatal("the daemon never called cmux within 20 s")
	}
	if !strings.Contains(dump, "PROBE_FIFO=") {
		t.Fatalf("the probe read nothing useful: %q", dump)
	}
	aims := 0
	for _, kv := range strings.Split(dump, "\n") {
		switch {
		case kv == cmux.EnvSocketPath+"="+aimed:
			aims++
		case kv == cmux.EnvBundledCLI+"="+bin: // the sidebar keeps it, the wake strips it
		case strings.HasPrefix(kv, "CMUX_"):
			t.Errorf("cmux ran with %q in its environment", kv)
		}
	}
	if aims != 1 {
		t.Fatalf("want exactly one %s=%s, got %d in %q", cmux.EnvSocketPath, aimed, aims, dump)
	}
}
