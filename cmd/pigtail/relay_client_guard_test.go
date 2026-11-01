package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestRelaySessionCallsUseSessionClient is the source guard behind
// sessionClient. The daemon lets a caller act as a relay session only when
// X-Pigtail-Scope equals the session's registered scope, so a session-bound
// call made through a bare newClient() (no header = work) silently loses every
// perso session's inbox. The guard traces, in the package's non-test files,
// the receiver of every relay client method back to the constructor that
// built it, and fails with file:line when that is not an allowed one:
//
//   - ClaimMessages, WaitMessages, AckMessage, SendMessage act as a session:
//     sessionClient only;
//   - PostPeers registers or ends a session (hooks, msg) or refreshes the
//     registry from a snapshot (session ls): sessionClient, or newScopedClient
//     for the snapshot — a scope-sending client either way, never newClient.
//
// Tracing is structural and deliberately small: a receiver is a direct call,
// a local variable (every assignment in the enclosing function), a function
// parameter (every call site of that function in the package), a parameter of
// a func literal passed to a package function (every call of that callback
// inside the function), or a struct field (every keyed composite literal
// setting it); X.WithCrossScopeWrite() traces to X (it keeps X's scope
// header). Anything it cannot trace fails — rewrite the call so it can.
func TestRelaySessionCallsUseSessionClient(t *testing.T) {
	t.Parallel()
	allowed := map[string]map[string]bool{
		"ClaimMessages": {"sessionClient": true},
		"WaitMessages":  {"sessionClient": true},
		"AckMessage":    {"sessionClient": true},
		"SendMessage":   {"sessionClient": true},
		// A broadcast acts as the sending session, like SendMessage.
		"BroadcastMessage": {"sessionClient": true},
		// Typed waits act as the session too (POST/DELETE /peers/{s}/wait).
		"SetSessionWait":   {"sessionClient": true},
		"ClearSessionWait": {"sessionClient": true},
		"PostPeers":        {"sessionClient": true, "newScopedClient": true},
		// An ack acts as the session when it names one (sessionClient); by hand it
		// names none and speaks for the caller's scope, --scope honoured
		// (newScopedClient), as `alerts list` reads it. It holds because the
		// newScopedClient branch never sends a session_id.
		"AckAlert": {"sessionClient": true, "newScopedClient": true},
	}
	pkg := loadPackageSource(t)
	checked := 0
	for _, f := range pkg.files {
		walkWithStack(f, func(n ast.Node, stack []ast.Node) {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return
			}
			want, ok := allowed[sel.Sel.Name]
			if !ok {
				return
			}
			checked++
			origins, why := pkg.origins(sel.X, stack, 0)
			pos := pkg.fset.Position(call.Pos())
			if why != "" {
				t.Errorf("%s:%d: %s: cannot trace the client (%s) — build it with sessionClient(…) where it is used, or in a local variable",
					filepath.Base(pos.Filename), pos.Line, sel.Sel.Name, why)
				return
			}
			for _, o := range origins {
				if !want[o] {
					t.Errorf("%s:%d: %s goes through a client built by %s — a relay call acting as a session must use sessionClient (it sends the session's scope; newClient sends none = work)",
						filepath.Base(pos.Filename), pos.Line, sel.Sel.Name, o)
				}
			}
		})
	}
	if checked < 11 {
		t.Fatalf("checked only %d relay calls — the guard is not looking at the right code", checked)
	}
}

type pkgSource struct {
	fset  *token.FileSet
	files []*ast.File
	funcs map[string]*ast.FuncDecl
	// fields maps "Type.field" to every value a keyed composite literal of
	// Type gives it.
	fields map[string][]ast.Expr
}

func loadPackageSource(t *testing.T) *pkgSource {
	t.Helper()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	p := &pkgSource{fset: token.NewFileSet(), funcs: map[string]*ast.FuncDecl{}, fields: map[string][]ast.Expr{}}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(p.fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		p.add(f)
	}
	return p
}

// add indexes one parsed file: its package functions and the keyed fields of
// its composite literals.
func (p *pkgSource) add(f *ast.File) {
	p.files = append(p.files, f)
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
			p.funcs[fd.Name.Name] = fd
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		typ, ok := lit.Type.(*ast.Ident)
		if !ok {
			return true
		}
		for _, el := range lit.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				if k, ok := kv.Key.(*ast.Ident); ok {
					p.fields[typ.Name+"."+k.Name] = append(p.fields[typ.Name+"."+k.Name], kv.Value)
				}
			}
		}
		return true
	})
}

// pkgFromSource builds a pkgSource from one in-memory file (guard self-tests).
func pkgFromSource(t *testing.T, src string) *pkgSource {
	t.Helper()
	p := &pkgSource{fset: token.NewFileSet(), funcs: map[string]*ast.FuncDecl{}, fields: map[string][]ast.Expr{}}
	f, err := parser.ParseFile(p.fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	p.add(f)
	return p
}

// The tracer follows X.WithCrossScopeWrite() back to X's constructor — the
// derived client keeps X's scope header — and nothing else: a derivation from
// newClient is still refused, WithScope is still named, not followed.
func TestOriginsFollowWithCrossScopeWrite(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, body string
		want       string // the single origin expected for every trace
		accepted   bool   // under allowed["SendMessage"]
	}{
		{"direct derivation", `c := sessionClient(os.Getenv); c.WithCrossScopeWrite().SendMessage(ctx, r)`, "sessionClient", true},
		{"through a local", `c := sessionClient(os.Getenv); s := c; if x { s = c.WithCrossScopeWrite() }; s.SendMessage(ctx, r)`, "sessionClient", true},
		{"self-derivation", `c := sessionClient(os.Getenv); if x { c = c.WithCrossScopeWrite() }; c.SendMessage(ctx, r)`, "sessionClient", true},
		{"from newClient", `c := newClient(); c.WithCrossScopeWrite().SendMessage(ctx, r)`, "newClient", false},
		{"WithScope not followed", `c := sessionClient(os.Getenv); c.WithScope("x").SendMessage(ctx, r)`, "(c).WithScope", false},
	}
	for _, c := range cases {
		p := pkgFromSource(t, "package main\n\nfunc f() {\n"+c.body+"\n}\n")
		var got []string
		var why string
		walkWithStack(p.files[0], func(n ast.Node, stack []ast.Node) {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "SendMessage" {
					got, why = p.origins(sel.X, stack, 0)
				}
			}
		})
		if why != "" || len(got) == 0 {
			t.Errorf("%s: cannot trace (%s)", c.name, why)
			continue
		}
		accepted := true
		for _, o := range got {
			if o != c.want {
				t.Errorf("%s: origin %q, want %q (all: %v)", c.name, o, c.want, got)
			}
			accepted = accepted && o == "sessionClient"
		}
		if accepted != c.accepted {
			t.Errorf("%s: accepted = %v, want %v", c.name, accepted, c.accepted)
		}
	}
}

// walkWithStack calls fn on every node with its ancestors (outermost first).
func walkWithStack(root ast.Node, fn func(ast.Node, []ast.Node)) {
	var stack []ast.Node
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		fn(n, stack)
		stack = append(stack, n)
		return true
	})
}

// stackTo returns the ancestors of target inside root.
func stackTo(root, target ast.Node) []ast.Node {
	var found []ast.Node
	walkWithStack(root, func(n ast.Node, stack []ast.Node) {
		if n == target && found == nil {
			found = append([]ast.Node{}, stack...)
		}
	})
	return found
}

// origins names the constructors e may come from: the function called to
// build it (sessionClient, newScopedClient, newClient…). why is set when it
// cannot tell.
func (p *pkgSource) origins(e ast.Expr, stack []ast.Node, depth int) ([]string, string) {
	if depth > 8 {
		return nil, "tracing too deep"
	}
	switch x := e.(type) {
	case *ast.ParenExpr:
		return p.origins(x.X, stack, depth+1)
	case *ast.CallExpr:
		switch fn := x.Fun.(type) {
		case *ast.Ident:
			return []string{fn.Name}, ""
		case *ast.SelectorExpr:
			// c.WithCrossScopeWrite() copies c and only adds the opt-in header
			// (pkg/pigtail/client.go): the scope header is c's, so are its
			// origins. Any other derivation (WithScope…) changes what the client
			// sends: it is named, never followed.
			if fn.Sel.Name == "WithCrossScopeWrite" {
				return p.origins(fn.X, stack, depth+1)
			}
			return []string{"(" + exprString(fn.X) + ")." + fn.Sel.Name}, ""
		}
		return nil, "call of " + exprString(x.Fun)
	case *ast.SelectorExpr:
		v, ok := x.X.(*ast.Ident)
		if !ok {
			return nil, "selector " + exprString(x)
		}
		typ := p.typeOf(v.Name, stack)
		if typ == "" {
			return nil, "type of " + v.Name + " unknown"
		}
		vals := p.fields[typ+"."+x.Sel.Name]
		if len(vals) == 0 {
			return nil, "no composite literal sets " + typ + "." + x.Sel.Name
		}
		return p.all(vals, depth, func(e ast.Expr) []ast.Node { return p.stackOf(e) })
	case *ast.Ident:
		return p.identOrigins(x.Name, stack, depth)
	}
	return nil, "expression " + exprString(e)
}

func (p *pkgSource) all(vals []ast.Expr, depth int, stackOf func(ast.Expr) []ast.Node) ([]string, string) {
	var out []string
	for _, v := range vals {
		o, why := p.origins(v, stackOf(v), depth+1)
		if why != "" {
			return nil, why
		}
		out = append(out, o...)
	}
	return out, ""
}

// stackOf finds the ancestors of a node anywhere in the package.
func (p *pkgSource) stackOf(n ast.Node) []ast.Node {
	for _, f := range p.files {
		if s := stackTo(f, n); s != nil {
			return s
		}
	}
	return nil
}

// identOrigins resolves a variable: a parameter of the innermost enclosing
// function (literal or declaration), else every assignment to it in the
// enclosing declaration.
func (p *pkgSource) identOrigins(name string, stack []ast.Node, depth int) ([]string, string) {
	var decl *ast.FuncDecl
	for i := len(stack) - 1; i >= 0; i-- {
		switch fn := stack[i].(type) {
		case *ast.FuncLit:
			if idx, ok := paramIndex(fn.Type, name); ok {
				return p.funcLitParamOrigins(fn, idx, stack[:i], depth)
			}
		case *ast.FuncDecl:
			decl = fn
			if idx, ok := paramIndex(fn.Type, name); ok {
				return p.paramOrigins(fn.Name.Name, idx, depth)
			}
		}
		if decl != nil {
			break
		}
	}
	if decl == nil {
		return nil, name + " outside a function"
	}
	var vals []ast.Expr
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		switch a := n.(type) {
		case *ast.AssignStmt:
			for i, l := range a.Lhs {
				if id, ok := l.(*ast.Ident); ok && id.Name == name {
					var v ast.Expr
					if len(a.Rhs) == len(a.Lhs) {
						v = a.Rhs[i]
					} else if len(a.Rhs) == 1 && i == 0 {
						v = a.Rhs[0] // c, err := f()
					}
					if v != nil && !isSelfDerivation(v, name) {
						vals = append(vals, v)
					}
				}
			}
		case *ast.ValueSpec:
			for i, id := range a.Names {
				if id.Name == name && i < len(a.Values) {
					vals = append(vals, a.Values[i])
				}
			}
		}
		return true
	})
	if len(vals) == 0 {
		return nil, "no assignment to " + name + " in " + decl.Name.Name
	}
	return p.all(vals, depth, func(e ast.Expr) []ast.Node { return stackTo(decl, e) })
}

// isSelfDerivation reports name = name.WithCrossScopeWrite(): it keeps the
// origins the other assignments give name.
func isSelfDerivation(e ast.Expr, name string) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "WithCrossScopeWrite" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == name
}

// paramOrigins traces parameter idx of the package function fn through every
// call of fn.
func (p *pkgSource) paramOrigins(fn string, idx, depth int) ([]string, string) {
	var args []ast.Expr
	for _, f := range p.files {
		ast.Inspect(f, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok && id.Name == fn && idx < len(c.Args) {
					args = append(args, c.Args[idx])
				}
			}
			return true
		})
	}
	if len(args) == 0 {
		return nil, "no call of " + fn
	}
	return p.all(args, depth, func(e ast.Expr) []ast.Node { return p.stackOf(e) })
}

// funcLitParamOrigins traces parameter idx of lit, a callback passed to a
// package function F, through every call of that callback inside F.
func (p *pkgSource) funcLitParamOrigins(lit *ast.FuncLit, idx int, stack []ast.Node, depth int) ([]string, string) {
	if len(stack) == 0 {
		return nil, "func literal parameter"
	}
	call, ok := stack[len(stack)-1].(*ast.CallExpr)
	if !ok {
		return nil, "func literal not passed to a function"
	}
	fnID, ok := call.Fun.(*ast.Ident)
	if !ok {
		return nil, "func literal passed to " + exprString(call.Fun)
	}
	argIdx := -1
	for i, a := range call.Args {
		if a == lit {
			argIdx = i
		}
	}
	decl := p.funcs[fnID.Name]
	if decl == nil || argIdx < 0 {
		return nil, "callee " + fnID.Name + " not in the package"
	}
	cb := paramName(decl.Type, argIdx)
	if cb == "" {
		return nil, "callee parameter"
	}
	var args []ast.Expr
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == cb && idx < len(c.Args) {
				args = append(args, c.Args[idx])
			}
		}
		return true
	})
	if len(args) == 0 {
		return nil, fnID.Name + " never calls " + cb
	}
	return p.all(args, depth, func(e ast.Expr) []ast.Node { return stackTo(decl, e) })
}

// typeOf finds the declared type name of a parameter in the enclosing
// functions.
func (p *pkgSource) typeOf(name string, stack []ast.Node) string {
	for i := len(stack) - 1; i >= 0; i-- {
		var ft *ast.FuncType
		switch fn := stack[i].(type) {
		case *ast.FuncLit:
			ft = fn.Type
		case *ast.FuncDecl:
			ft = fn.Type
		default:
			continue
		}
		for _, f := range ft.Params.List {
			for _, n := range f.Names {
				if n.Name == name {
					if id, ok := f.Type.(*ast.Ident); ok {
						return id.Name
					}
					return ""
				}
			}
		}
	}
	return ""
}

func paramIndex(ft *ast.FuncType, name string) (int, bool) {
	i := 0
	for _, f := range ft.Params.List {
		for _, n := range f.Names {
			if n.Name == name {
				return i, true
			}
			i++
		}
	}
	return 0, false
}

func paramName(ft *ast.FuncType, idx int) string {
	i := 0
	for _, f := range ft.Params.List {
		for _, n := range f.Names {
			if i == idx {
				return n.Name
			}
			i++
		}
	}
	return ""
}

func exprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	case *ast.CallExpr:
		return exprString(x.Fun) + "(…)"
	}
	return "?"
}
