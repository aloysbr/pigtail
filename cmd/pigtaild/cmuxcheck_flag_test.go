package main

import (
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/cmuxwatch"
)

func TestCmuxCheckModeFlag(t *testing.T) {
	cases := []struct {
		args []string
		want string
		bad  bool
	}{
		{nil, cmuxwatch.ModePilot, false}, // D3 (2026-10-09): the pilot by default
		{[]string{"-cmux-check-after-relaunch=fix"}, cmuxwatch.ModeFix, false},
		{[]string{"-cmux-check-after-relaunch=report"}, cmuxwatch.ModeReport, false},
		{[]string{"-cmux-check-after-relaunch=off"}, cmuxwatch.ModeOff, false},
		{[]string{"-cmux-check-after-relaunch=yes"}, "", true},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("pigtaild", flag.ContinueOnError)
		get := cmuxCheckModeFlag(fs)
		if err := fs.Parse(c.args); err != nil {
			t.Fatal(err)
		}
		got, err := get()
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("%v: mode %q, err %v; want %q (refused: %v)", c.args, got, err, c.want, c.bad)
		}
	}
}

// D8, T-a: --cmux-check-ctrl-c reaches the daemon's check (default rescue);
// an unknown value refuses to start pigtaild.
func TestCmuxCheckCtrlCFlag(t *testing.T) {
	cases := []struct {
		args []string
		want string
		bad  bool
	}{
		{nil, "rescue", false},
		{[]string{"-cmux-check-ctrl-c=unread"}, "unread", false},
		{[]string{"-cmux-check-ctrl-c=always"}, "", true},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("pigtaild", flag.ContinueOnError)
		get := cmuxCheckCtrlCFlag(fs)
		if err := fs.Parse(c.args); err != nil {
			t.Fatal(err)
		}
		got, err := get()
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("%v: ctrl-c %q, err %v; want %q (refused: %v)", c.args, got, err, c.want, c.bad)
		}
	}
}

// Wiring guard, behaviour half: the flags registered by registerFlags reach
// daemon.Config through config() — the same path run() and -check-args take.
func TestRunWiresCmuxCheckMode(t *testing.T) {
	fs := flag.NewFlagSet("pigtaild", flag.ContinueOnError)
	f := registerFlags(fs)
	if err := fs.Parse([]string{"--cmux-check-after-relaunch=off", "--cmux-check-ctrl-c=unread"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := f.config()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CmuxCheckMode != "off" || cfg.CmuxCheckCtrlC != "unread" {
		t.Fatalf("config: CmuxCheckMode %q, CmuxCheckCtrlC %q; want off, unread", cfg.CmuxCheckMode, cfg.CmuxCheckCtrlC)
	}
}

// Wiring guard, AST half: run() registers the flags on the real command line
// (registerFlags(flag.CommandLine)) and then builds its configuration from
// them (.config()) — breaks if main.go drops either half or swaps them.
func TestRunRegistersFlagsThenConfig(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var run *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "run" && fd.Recv == nil {
			run = fd
		}
	}
	if run == nil {
		t.Fatal("no func run in main.go")
	}
	registered, configured := token.NoPos, token.NoPos
	ast.Inspect(run.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			if fn.Name == "registerFlags" && len(call.Args) == 1 {
				if sel, ok := call.Args[0].(*ast.SelectorExpr); ok && sel.Sel.Name == "CommandLine" && registered == token.NoPos {
					registered = call.Pos()
				}
			}
		case *ast.SelectorExpr:
			if fn.Sel.Name == "config" && len(call.Args) == 0 && configured == token.NoPos {
				configured = call.Pos()
			}
		}
		return true
	})
	if registered == token.NoPos || configured == token.NoPos || configured < registered {
		t.Fatalf("run(): registerFlags(flag.CommandLine) at %v, .config() at %v; want both, in that order", registered, configured)
	}
}

func TestCmuxInputProbeFlag(t *testing.T) {
	cases := []struct {
		args []string
		want bool
		bad  bool
	}{
		{nil, true, false},
		{[]string{"-cmux-input-probe=off"}, false, false},
		{[]string{"-cmux-input-probe=on"}, true, false},
		{[]string{"-cmux-input-probe=maybe"}, false, true},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("pigtaild", flag.ContinueOnError)
		get := cmuxInputProbeFlag(fs)
		if err := fs.Parse(c.args); err != nil {
			t.Fatal(err)
		}
		got, err := get()
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("%v: probe %v, err %v; want %v (refused: %v)", c.args, got, err, c.want, c.bad)
		}
	}
	fs := flag.NewFlagSet("pigtaild", flag.ContinueOnError)
	f := registerFlags(fs)
	if err := fs.Parse([]string{"--cmux-input-probe=off"}); err != nil {
		t.Fatal(err)
	}
	if cfg, err := f.config(); err != nil || cfg.CmuxInputProbe {
		t.Fatalf("config: CmuxInputProbe %v, err %v; want off", cfg.CmuxInputProbe, err)
	}
	fs = flag.NewFlagSet("pigtaild", flag.ContinueOnError)
	f = registerFlags(fs)
	_ = fs.Parse([]string{"--cmux-input-probe=nope"})
	if _, err := f.config(); err == nil {
		t.Fatal("an unknown --cmux-input-probe must refuse to start")
	}
}

func TestCmuxInputProbeFlagHelpNamesQuery(t *testing.T) {
	fs := flag.NewFlagSet("pigtaild", flag.ContinueOnError)
	cmuxInputProbeFlag(fs)
	u := fs.Lookup("cmux-input-probe").Usage
	if !strings.Contains(u, "(a terminal status query on the session's own tty, never a keystroke)") || strings.Contains(u, "focus events") {
		t.Fatalf("usage %q", u)
	}
}
