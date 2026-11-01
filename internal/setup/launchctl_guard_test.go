package setup

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The launchctl guard: launchctl is a machine-wide switch, so only the Runner
// (internal/setup/runner.go) execs it, and only the CLI may name the runner
// and the launchd helpers. A test that reached the real launchctl would load
// or kick the user's own daemon.

const modulePath = "github.com/aloysbr/pigtail"
const setupImport = modulePath + "/internal/setup"

var (
	execAllowed = map[string]bool{
		"internal/setup/runner.go": true,
		"internal/tui/commands.go": true,
	}
	refAllowedPrefix = []string{"internal/setup/", "cmd/pigtail/"}
	forbiddenRefs    = map[string]bool{"ExecRunner": true, "Kickstart": true, "LoadLaunchd": true, "UnloadLaunchd": true}
	skipDirs         = map[string]bool{"testdata": true, "node_modules": true, ".git": true, "web": true, "vendor": true}
)

type violation struct {
	File string
	Line int
	Msg  string
}

type goPkg struct {
	files  map[string]*ast.File // module-relative path → file
	consts map[string]ast.Expr
}

type guard struct {
	root string
	fset *token.FileSet
	pkgs map[string]*goPkg // module-relative dir
}

func (g *guard) pkg(dir string) *goPkg {
	if p, ok := g.pkgs[dir]; ok {
		return p
	}
	p := &goPkg{files: map[string]*ast.File{}, consts: map[string]ast.Expr{}}
	g.pkgs[dir] = p
	ents, _ := os.ReadDir(filepath.Join(g.root, filepath.FromSlash(dir)))
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		rel := path.Join(dir, n)
		f, err := parser.ParseFile(g.fset, filepath.Join(g.root, filepath.FromSlash(rel)), nil, 0)
		if err != nil {
			continue
		}
		p.files[rel] = f
		ast.Inspect(f, func(nd ast.Node) bool {
			if gd, ok := nd.(*ast.GenDecl); ok && gd.Tok == token.CONST {
				for _, sp := range gd.Specs {
					vs := sp.(*ast.ValueSpec)
					for i, id := range vs.Names {
						if i < len(vs.Values) {
							p.consts[id.Name] = vs.Values[i]
						}
					}
				}
			}
			return true
		})
	}
	return p
}

// resolve folds an expression to a string when it is a literal, a constant or
// a + of those (same package, or pkg.Const from a package of the module).
func (g *guard) resolve(dir string, f *ast.File, e ast.Expr, depth int) (string, bool) {
	if depth > 8 {
		return "", false
	}
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			s, err := strconv.Unquote(x.Value)
			return s, err == nil
		}
	case *ast.ParenExpr:
		return g.resolve(dir, f, x.X, depth+1)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		l, ok1 := g.resolve(dir, f, x.X, depth+1)
		r, ok2 := g.resolve(dir, f, x.Y, depth+1)
		return l + r, ok1 && ok2
	case *ast.Ident:
		if v, ok := g.pkg(dir).consts[x.Name]; ok {
			return g.resolve(dir, f, v, depth+1)
		}
	case *ast.SelectorExpr:
		id, ok := x.X.(*ast.Ident)
		if !ok {
			return "", false
		}
		for _, im := range f.Imports {
			ip, _ := strconv.Unquote(im.Path.Value)
			name := path.Base(ip)
			if im.Name != nil {
				name = im.Name.Name
			}
			if name != id.Name || !strings.HasPrefix(ip, modulePath+"/") {
				continue
			}
			d := strings.TrimPrefix(ip, modulePath+"/")
			if v, ok := g.pkg(d).consts[x.Sel.Name]; ok {
				return g.resolve(d, g.anyFile(d), v, depth+1)
			}
		}
	}
	return "", false
}

func (g *guard) anyFile(dir string) *ast.File {
	for _, f := range g.pkg(dir).files {
		return f
	}
	return &ast.File{}
}

func checkLaunchctl(root string) []violation {
	g := &guard{root: root, fset: token.NewFileSet(), pkgs: map[string]*goPkg{}}
	var dirs []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if p != root && skipDirs[d.Name()] {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(root, p)
		dirs = append(dirs, filepath.ToSlash(rel))
		return nil
	})
	var out []violation
	for _, dir := range dirs {
		pk := g.pkg(dir)
		for rel, f := range pk.files {
			// setup alias(es) in this file
			aliases := map[string]bool{}
			for _, im := range f.Imports {
				if ip, _ := strconv.Unquote(im.Path.Value); ip == setupImport {
					n := "setup"
					if im.Name != nil {
						n = im.Name.Name
					}
					aliases[n] = true
				}
			}
			ast.Inspect(f, func(nd ast.Node) bool {
				switch x := nd.(type) {
				case *ast.CallExpr:
					se, ok := x.Fun.(*ast.SelectorExpr)
					if !ok || len(x.Args) == 0 {
						break
					}
					id, ok := se.X.(*ast.Ident)
					if !ok || id.Name != "exec" || (se.Sel.Name != "Command" && se.Sel.Name != "CommandContext") {
						break
					}
					arg := x.Args[0]
					if se.Sel.Name == "CommandContext" && len(x.Args) > 1 {
						arg = x.Args[1]
					}
					if v, ok := g.resolve(dir, f, arg, 0); ok && path.Base(v) == "launchctl" && !execAllowed[rel] {
						out = append(out, violation{rel, g.fset.Position(x.Pos()).Line, "execs launchctl outside setup.Runner"})
					}
				case *ast.SelectorExpr:
					id, ok := x.X.(*ast.Ident)
					if ok && aliases[id.Name] && forbiddenRefs[x.Sel.Name] {
						allowed := false
						for _, pre := range refAllowedPrefix {
							allowed = allowed || strings.HasPrefix(rel, pre)
						}
						if !allowed {
							out = append(out, violation{rel, g.fset.Position(x.Pos()).Line, "references setup." + x.Sel.Name})
						}
					}
				}
				return true
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for d := wd; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			t.Fatal("go.mod not found")
		}
	}
}

func TestLaunchctlOnlyInSetup(t *testing.T) {
	if vs := checkLaunchctl(moduleRoot(t)); len(vs) != 0 {
		t.Fatalf("launchctl guard: %s", fmt.Sprint(vs))
	}
}

// Positive control: the guard sees what it must.
func TestLaunchctlGuardCatchesViolations(t *testing.T) {
	vs := checkLaunchctl(filepath.Join("testdata", "launchctlguard"))
	got := map[string]int{}
	for _, v := range vs {
		got[strings.SplitN(v.File, "/", 2)[0]]++
	}
	for dir, want := range map[string]int{"literal": 1, "constant": 1, "reference": 1, "clean": 0} {
		if got[dir] != want {
			t.Errorf("%s: %d violations, want %d (all: %v)", dir, got[dir], want, vs)
		}
	}
}
