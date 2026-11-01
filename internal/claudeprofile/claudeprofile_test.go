package claudeprofile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTable(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

const sample = `cells:
  mt: { config_dir: ~/.claude-w1, scope: gx }
  t:  { config_dir: ~/.claude-globex, scope: gx }
  p:  { config_dir: ~/.claude-perso, scope: perso }
`

func TestCellResolvesAndExpandsHome(t *testing.T) {
	tbl, err := Load(writeTable(t, sample))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	c, ok := tbl.Cell("mt")
	if !ok {
		t.Fatal("cell mt not found")
	}
	if c.ID != "mt" || c.Scope != "globex" { // read canonical: gx is an alias
		t.Fatalf("got %+v", c)
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, ".claude-w1"); c.ConfigDir != want {
		t.Fatalf("ConfigDir = %q, want %q", c.ConfigDir, want)
	}
}

func TestUnknownCell(t *testing.T) {
	tbl, _ := Load(writeTable(t, sample))
	if _, ok := tbl.Cell("nope"); ok {
		t.Fatal("expected unknown cell to be absent")
	}
}

func TestByConfigDir(t *testing.T) {
	tbl, _ := Load(writeTable(t, sample))
	home, _ := os.UserHomeDir()
	c, ok := tbl.ByConfigDir(filepath.Join(home, ".claude-globex"))
	if !ok || c.ID != "t" {
		t.Fatalf("got %+v ok=%v, want cell t", c, ok)
	}
}

// TestConfigDirsIsOrderedByCellIDAndStable pins the contract callers depend
// on: claudesession.Find resolves an account by taking the FIRST config dir
// hosting a transcript, so an order derived from Go's randomized map
// iteration would report a different account for the same session between two
// consecutive runs. Repeating the call is the assertion that matters — a
// single call cannot distinguish "sorted" from "lucky".
func TestConfigDirsIsOrderedByCellIDAndStable(t *testing.T) {
	tbl, err := Load(writeTable(t, sample))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	home, _ := os.UserHomeDir()
	want := []string{
		filepath.Join(home, ".claude-w1"),     // mt
		filepath.Join(home, ".claude-perso"),  // p
		filepath.Join(home, ".claude-globex"), // t
	}
	for i := 0; i < 50; i++ {
		got := tbl.ConfigDirs()
		if len(got) != len(want) {
			t.Fatalf("ConfigDirs = %v, want %v", got, want)
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("run %d: ConfigDirs = %v, want %v (sorted by cell id)", i, got, want)
			}
		}
	}
}

func TestLoadMissingFileNamesIt(t *testing.T) {
	_, err := Load(t.TempDir())
	if err == nil {
		t.Fatal("expected an error for a missing table")
	}
	if got := err.Error(); !strings.Contains(got, Filename) {
		t.Fatalf("error %q should name %q", got, Filename)
	}
}

func TestScopeOf(t *testing.T) {
	tbl, _ := Load(writeTable(t, sample))
	home, _ := os.UserHomeDir()
	if sc, ok := tbl.ScopeOf(filepath.Join(home, ".claude-perso")); !ok || sc != "perso" {
		t.Fatalf("ScopeOf(perso dir) = %q, %v", sc, ok)
	}
	// A trailing slash or a ~ spelling names the same dir.
	if sc, ok := tbl.ScopeOf(filepath.Join(home, ".claude-globex") + "/"); !ok || sc != "globex" {
		t.Fatalf("ScopeOf(gx dir/) = %q, %v", sc, ok)
	}
	if sc, ok := tbl.ScopeOf("~/.claude-w1"); !ok || sc != "globex" {
		t.Fatalf("ScopeOf(~/.claude-w1) = %q, %v", sc, ok)
	}
	if _, ok := tbl.ScopeOf(filepath.Join(home, ".claude")); ok {
		t.Fatal("the bare ~/.claude is not a cell")
	}
	var nilTable *Table
	if _, ok := nilTable.ScopeOf("/x"); ok {
		t.Fatal("nil table has no scope")
	}
}

func TestCellAccountIsOptional(t *testing.T) {
	tbl, err := Load(writeTable(t, `cells:
  mp: { config_dir: ~/.claude-h1, scope: perso, account: m }
  p:  { config_dir: ~/.claude-perso, scope: perso }
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c, _ := tbl.Cell("mp"); c.Account != "m" {
		t.Fatalf("mp.Account = %q, want m", c.Account)
	}
	if c, _ := tbl.Cell("p"); c.Account != "" {
		t.Fatalf("p.Account = %q, want empty (key absent: older the launcher tooling)", c.Account)
	}
}

// rank is the cell's declaration index in the launcher config (the launcher tooling);
// optional, nil when the table predates it — never read as 0.
func TestCellRankIsOptional(t *testing.T) {
	tbl, err := Load(writeTable(t, `cells:
  mt: { config_dir: ~/.claude-w1, scope: gx, account: m, rank: 0 }
  it: { config_dir: ~/.claude-w2, scope: gx, account: i, rank: 2 }
  p:  { config_dir: ~/.claude-perso, scope: perso, account: m }
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for id, want := range map[string]int{"mt": 0, "it": 2} {
		if c, _ := tbl.Cell(id); c.Rank == nil || *c.Rank != want {
			t.Fatalf("%s.Rank = %v, want %d", id, c.Rank, want)
		}
	}
	if c, _ := tbl.Cell("p"); c.Rank != nil {
		t.Fatalf("p.Rank = %d, want nil (key absent)", *c.Rank)
	}
}

func TestLoadCanonicalisesCellScopes(t *testing.T) {
	dir := t.TempDir()
	body := "cells:\n  t: { config_dir: /cfg/t, scope: gx }\n  s: { config_dir: /cfg/s, scope: globex }\n  p: { config_dir: /cfg/p, scope: Perso }\n"
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tb, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"t": "globex", "s": "globex", "p": "perso"} {
		if c, _ := tb.Cell(id); c.Scope != want {
			t.Errorf("cell %s scope = %q, want %q", id, c.Scope, want)
		}
	}
}

// loadTable builds on the file's existing writeTable(t, body) string
// (returns the dir): never redeclare writeTable.
func loadTable(t *testing.T, body string) *Table {
	t.Helper()
	tb, err := Load(writeTable(t, body))
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func TestHomeForLongestPrefix(t *testing.T) {
	root := t.TempDir()
	dev := filepath.Join(root, "Developer")
	globex := filepath.Join(dev, "clients", "globex")
	for _, d := range []string{filepath.Join(globex, "monorepo"), filepath.Join(dev, "home", "pigtail")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	tb := loadTable(t, "cells:\n"+
		"  mt: { config_dir: /x/mt, scope: globex, account: m, rank: 0, cwd_home: "+globex+" }\n"+
		"  jt: { config_dir: /x/jt, scope: globex, account: j, rank: 4, cwd_home: "+globex+" }\n"+
		"  jp: { config_dir: /x/jp, scope: perso, account: j, rank: 5, cwd_home: "+dev+" }\n")
	h, err := tb.HomeFor(filepath.Join(globex, "monorepo"))
	if err != nil || h.Scope != "globex" || strings.Join(h.Cells, ",") != "jt,mt" {
		t.Fatalf("globex dir = %+v %v", h, err)
	}
	h, err = tb.HomeFor(filepath.Join(dev, "home", "pigtail"))
	if err != nil || h.Scope != "perso" || strings.Join(h.Cells, ",") != "jp" {
		t.Fatalf("perso dir = %+v %v", h, err)
	}
	if _, err := tb.HomeFor(t.TempDir()); !errors.Is(err, ErrNoHome) {
		t.Fatalf("outside every home = %v, want ErrNoHome", err)
	}
}

func TestHomeForBoundaries(t *testing.T) {
	root := t.TempDir()
	dev := filepath.Join(root, "Developer")
	sib := filepath.Join(root, "Developer2")
	_ = os.MkdirAll(dev, 0o700)
	_ = os.MkdirAll(sib, 0o700)
	tb := loadTable(t, "cells:\n  jp: { config_dir: /x/jp, scope: perso, cwd_home: "+dev+"/ }\n")
	if _, err := tb.HomeFor(sib); !errors.Is(err, ErrNoHome) {
		t.Fatalf("sibling with a common prefix = %v, want ErrNoHome", err)
	}
	if h, err := tb.HomeFor(dev); err != nil || h.Scope != "perso" {
		t.Fatalf("the home itself = %+v %v", h, err)
	}
	// macOS: t.TempDir() lives under /var → /private/var; both spellings match.
	if real, err := filepath.EvalSymlinks(dev); err == nil && real != dev {
		if h, err := tb.HomeFor(real); err != nil || h.Scope != "perso" {
			t.Fatalf("symlink-resolved spelling = %+v %v", h, err)
		}
	}
}

func TestHomeForAmbiguousAndAbsent(t *testing.T) {
	d := t.TempDir()
	tb := loadTable(t, "cells:\n"+
		"  a: { config_dir: /x/a, scope: perso, cwd_home: "+d+" }\n"+
		"  b: { config_dir: /x/b, scope: globex, cwd_home: "+d+" }\n")
	var amb *AmbiguousHomeError
	if _, err := tb.HomeFor(d); !errors.As(err, &amb) || strings.Join(amb.Cells, ",") != "a,b" {
		t.Fatalf("ambiguous = %v", err)
	}
	old := loadTable(t, "cells:\n  p: { config_dir: /x/p, scope: perso }\n")
	if old.HasHomes() {
		t.Fatal("a table without cwd_home reports homes")
	}
	if _, err := old.HomeFor(d); !errors.Is(err, ErrNoHomes) {
		t.Fatalf("no cwd_home at all = %v, want ErrNoHomes", err)
	}
}

func TestSameAccountIn(t *testing.T) {
	tb := loadTable(t, "cells:\n"+
		"  mt: { config_dir: /x/mt, scope: gx, account: m, rank: 0 }\n"+
		"  zt: { config_dir: /x/zt, scope: gx, account: m, rank: 9 }\n"+
		"  it: { config_dir: /x/it, scope: gx, account: i, rank: 2 }\n"+
		"  mp: { config_dir: /x/mp, scope: perso, account: m, rank: 1 }\n")
	got := tb.SameAccountIn("m", "globex") // alias read canonical at Load
	if len(got) != 2 || got[0].ID != "mt" || got[1].ID != "zt" {
		t.Fatalf("same account = %+v", got)
	}
	if got := tb.SameAccountIn("", "globex"); len(got) != 0 {
		t.Fatalf("empty account matched %+v", got)
	}
	if ids := strings.Join(tb.IDsInScope("globex"), ","); ids != "it,mt,zt" {
		t.Fatalf("IDsInScope = %s", ids)
	}
}

func TestLoadReadsThresholds(t *testing.T) {
	tbl, err := Load(writeTable(t, `cells:
  mt: { config_dir: /x/mt, scope: gx, account: m, rank: 0, cwd_home: /x, select_pct: 70, switch_pct: 78 }
  it: { config_dir: /x/it, scope: gx, account: i, rank: 2, cwd_home: /x }
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	mt, _ := tbl.Cell("mt")
	if mt.SelectPct == nil || *mt.SelectPct != 70 || mt.SwitchPct == nil || *mt.SwitchPct != 78 {
		t.Fatalf("mt thresholds: %v %v", mt.SelectPct, mt.SwitchPct)
	}
	it, _ := tbl.Cell("it")
	if it.SelectPct != nil || it.SwitchPct != nil {
		t.Fatalf("it thresholds: %v %v", it.SelectPct, it.SwitchPct)
	}
	in := mt.UsageInput()
	if in.ID != "mt" || in.ConfigDir != "/x/mt" || in.Account != "m" || in.Rank == nil || *in.Rank != 0 ||
		in.SelectPct == nil || *in.SelectPct != 70 || in.SwitchPct == nil || *in.SwitchPct != 78 {
		t.Fatalf("UsageInput: %+v", in)
	}
	if got := in.Thresholds(); got.SelectPct != 70 || got.SwitchPct != 78 {
		t.Fatalf("Thresholds: %+v", got)
	}
}

func TestCellBudgetPctFromYAML(t *testing.T) {
	tbl, err := Load(writeTable(t, `cells:
  mt: { config_dir: /x/mt, scope: gx, account: m, budget_pct: 60 }
  it: { config_dir: /x/it, scope: gx, account: i }
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	mt, _ := tbl.Cell("mt")
	if mt.BudgetPct == nil || *mt.BudgetPct != 60 {
		t.Fatalf("mt budget: %v", mt.BudgetPct)
	}
	if in := mt.UsageInput(); in.BudgetPct == nil || *in.BudgetPct != 60 {
		t.Fatalf("UsageInput budget: %v", in.BudgetPct)
	}
	it, _ := tbl.Cell("it")
	if it.BudgetPct != nil || it.UsageInput().BudgetPct != nil {
		t.Fatalf("it budget: %v", it.BudgetPct)
	}
}
