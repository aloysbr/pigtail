package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
)

// pigtaild never hands a CMUX_* variable of the pane it was started from to
// the cmux calls it makes: cmux reads CMUX_SURFACE_ID before an explicit
// target (sub_x000000000000041, Task 7). Only the bundled CLI path, which the
// daemon reads itself, stays.

func cmuxEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "CMUX_") {
			out = append(out, kv)
		}
	}
	return out
}

func TestStripCmuxEnv(t *testing.T) {
	t.Setenv("CMUX_SURFACE_ID", "80914656-BF88-4780-9982-CA1665755494")
	t.Setenv("CMUX_AGENT_LAUNCH_ARGV_B64", "Y2xhdWRl")
	t.Setenv(cmux.EnvBundledCLI, "/x/cmux")
	t.Setenv("PATH_KEEP", "1")
	stripCmuxEnv()
	if got := cmuxEnv(); len(got) != 1 || got[0] != cmux.EnvBundledCLI+"=/x/cmux" {
		t.Fatalf("CMUX_* left: %q, want only %s=/x/cmux", got, cmux.EnvBundledCLI)
	}
	if os.Getenv("PATH_KEEP") != "1" {
		t.Fatal("a non-CMUX variable was removed")
	}

	t.Setenv(cmux.EnvBundledCLI, "x") // restored at the end of the test
	if err := os.Unsetenv(cmux.EnvBundledCLI); err != nil {
		t.Fatal(err)
	}
	stripCmuxEnv()
	if v, ok := os.LookupEnv(cmux.EnvBundledCLI); ok {
		t.Fatalf("absent before, set after: %q (empty means « no cmux »)", v)
	}
}

// TestHelperRunDaemon is the child of TestRunStripsCmuxEnv: it starts the real
// run() on a throwaway socket and database.
func TestHelperRunDaemon(t *testing.T) {
	if os.Getenv("PIGTAILD_HELPER") != "1" {
		t.Skip("helper process of TestRunStripsCmuxEnv")
	}
	dir := os.Getenv("PIGTAILD_HELPER_DIR")
	os.Args = []string{"pigtaild", "--socket", filepath.Join(dir, "p.sock"), "--db", filepath.Join(dir, "p.db"),
		"--http=", "--cmux-check-after-relaunch=off", "--cmux-sidebar",
		"--cmux-sidebar-window", "00000000-0000-4000-8000-000000000000"}
	if sock := os.Getenv("PIGTAILD_HELPER_CMUX_SOCKET"); sock != "" {
		os.Args = append(os.Args, "--cmux-socket", sock) // TestRunAimsCmuxSocket
	}
	_ = run()
}

func TestRunStripsCmuxEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a daemon child process")
	}
	dir := t.TempDir()
	fifo := filepath.Join(dir, "probe.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// The fake cmux dumps its environment once (a mkdir lock: a second call
	// never blocks on the FIFO), then answers an empty listing.
	bin := filepath.Join(dir, "cmux")
	script := "#!/bin/sh\nif mkdir \"$PROBE_DIR/once\" 2>/dev/null; then env > \"$PROBE_FIFO\"; fi\nprintf '{\"windows\":[]}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	for _, d := range []string{home, filepath.Join(dir, "config"), filepath.Join(dir, "state"), filepath.Join(dir, "daemon")} {
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
		"PIGTAILD_HELPER_DIR=" + filepath.Join(dir, "daemon"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(dir, "config"),
		"XDG_STATE_HOME=" + filepath.Join(dir, "state"),
		"CMUX_SURFACE_ID=80914656-BF88-4780-9982-CA1665755494", // positive control: the child inherits them
		"CMUX_WORKSPACE_ID=B89B4597-2397-45F6-81D9-95F3B0FA83F8",
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
		b, _ := os.ReadFile(fifo) // blocks until the fake cmux writes
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
	for _, kv := range strings.Split(dump, "\n") {
		if strings.HasPrefix(kv, "CMUX_") && kv != cmux.EnvBundledCLI+"="+bin {
			t.Errorf("cmux ran with %q in its environment", kv)
		}
	}
}

// TestRunCallsStripCmuxEnvFirst: the strip is run()'s first statement, before
// any flag parsing or cmux call.
func TestRunCallsStripCmuxEnvFirst(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "run" || fn.Recv != nil {
			continue
		}
		if len(fn.Body.List) == 0 {
			t.Fatal("run has no body")
		}
		es, ok := fn.Body.List[0].(*ast.ExprStmt)
		if ok {
			if call, ok := es.X.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "stripCmuxEnv" {
					return
				}
			}
		}
		t.Fatal("run's first statement must be stripCmuxEnv()")
	}
	t.Fatal("no func run in main.go")
}
