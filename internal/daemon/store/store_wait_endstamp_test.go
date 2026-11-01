package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The scenario of the third plan review: the session ends with a review (record
// stamped session_end), resumes but the restore never runs (daemon down), the
// done command's prompt clears with keep=subject and finds no row, the command
// declares wrapped (early return), the session ends again (a wrapped row stamps
// nothing) and resumes: the first end's stamp must not bring back `review`.
func TestEndStampDiesAtTheNextWaitWrite(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "A", declared("review", "PR", tw0), subjS)
	mustEnd(t, st, "A", tw0.Add(time.Minute))
	wantClosedBy(t, st, a.OpenedID, "session_end", "after the first end")
	if _, _, _, err := st.ClearSessionWaitsKeepingSubject("A"); err != nil {
		t.Fatal(err)
	}
	mustSet(t, st, "A", declared("wrapped", "", tw0.Add(2*time.Minute)), subjS)
	mustEnd(t, st, "A", tw0.Add(3*time.Minute))
	got, err := st.RestoreEndedWaits("A")
	if err != nil || len(got) != 0 {
		t.Fatalf("RestoreEndedWaits = %v, %v; want nothing", got, err)
	}
	if ws, _ := st.SessionWaits("A"); len(ws) != 0 {
		t.Fatalf("session_waits = %+v", ws)
	}
}

// Every write path of a session's waits, run after a session end stamped its
// records: no session_end stamp survives it and the restore gives nothing.
// the paths map below lists them; the AST guard
// TestEveryWaitWriteInvalidatesEndStamps fails when a new writer skips the helper.
func TestEndStampDiesOnEveryWritePath(t *testing.T) {
	paths := map[string]func(t *testing.T, st *Store, subjectOpenID string){
		"declare same type": func(t *testing.T, st *Store, _ string) {
			mustSet(t, st, "A", declared("review", "PR", tw0.Add(5*time.Minute)), subjS)
		},
		"declare wrapped": func(t *testing.T, st *Store, _ string) {
			mustSet(t, st, "A", declared("wrapped", "", tw0.Add(5*time.Minute)), subjS)
		},
		"declare done": func(t *testing.T, st *Store, _ string) {
			mustSet(t, st, "A", declared("done", "", tw0.Add(5*time.Minute)), subjS)
		},
		"declare working": func(t *testing.T, st *Store, _ string) {
			mustSet(t, st, "A", declared("working", "", tw0.Add(5*time.Minute)), subjS)
		},
		"replace by another type": func(t *testing.T, st *Store, _ string) {
			mustSet(t, st, "A", declared("question", "Q", tw0.Add(5*time.Minute)), subjS)
		},
		"auto dialog": func(t *testing.T, st *Store, _ string) {
			mustSet(t, st, "A", auto("permission", "Bash", tw0.Add(5*time.Minute)), WaitMeta{})
		},
		"answer": func(t *testing.T, st *Store, id string) {
			if _, err := st.AnswerWaitWithMessage(id, []byte(`{}`), tw0.Add(5*time.Minute), nil); err != nil {
				t.Fatal(err)
			}
		},
		"prompt clear keep=subject": func(t *testing.T, st *Store, _ string) {
			if _, _, _, err := st.ClearSessionWaitsKeepingSubject("A"); err != nil {
				t.Fatal(err)
			}
		},
		"prompt clear keep=false / turn end / --clear": func(t *testing.T, st *Store, _ string) {
			if _, err := st.ClearSessionWaits("A"); err != nil {
				t.Fatal(err)
			}
		},
		"clear one source": func(t *testing.T, st *Store, _ string) {
			if _, err := st.ClearSessionWaits("A", pigtail.WaitSourceDeclared); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, run := range paths {
		for _, meta := range []struct {
			label string
			m     WaitMeta
		}{{"subject", subjS}, {"session", WaitMeta{}}} {
			if name == "answer" && meta.label == "session" {
				continue // an ended session's own record is closed: nothing to answer
			}
			st := openMem(t)
			a := mustSet(t, st, "A", declared("review", "PR", tw0), meta.m)
			mustEnd(t, st, "A", tw0.Add(time.Minute))
			wantClosedBy(t, st, a.OpenedID, "session_end", name+"/"+meta.label+" stamped by the end")
			run(t, st, a.OpenedID)
			var n int
			if err := st.db.QueryRow(`SELECT COUNT(*) FROM wait_records WHERE session_id = 'A' AND closed_by = 'session_end'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("%s/%s: %d session_end stamp(s) survive the write", name, meta.label, n)
			}
			// a declare/replace leaves a fresh row of its own: drop it, the
			// question is what a restore would add on top of the write.
			if _, err := st.db.Exec(`DELETE FROM session_waits WHERE session_id = 'A'`); err != nil {
				t.Fatal(err)
			}
			if got, err := st.RestoreEndedWaits("A"); err != nil || len(got) != 0 {
				t.Errorf("%s/%s: RestoreEndedWaits = %v, %v; want nothing", name, meta.label, got, err)
			}
		}
	}
}

// TestEveryWaitWriteInvalidatesEndStamps: a function of the package that
// writes session_waits (INSERT/DELETE/UPDATE) must call invalidateEndStampsTx
// or be listed in endStampExempt with its reason. A new writer that forgets
// the invariant fails here.
func TestEveryWaitWriteInvalidatesEndStamps(t *testing.T) {
	fset := token.NewFileSet()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*ast.File{}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = f
	}
	writes := regexp.MustCompile(`(?i)(INSERT INTO|DELETE FROM|UPDATE|INSERT OR REPLACE INTO)\s+session_waits`)
	seen := 0
	for fname, f := range files {
		{
			src, err := os.ReadFile(fname)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				body := string(src[fset.Position(fn.Body.Pos()).Offset:fset.Position(fn.Body.End()).Offset])
				if !writes.MatchString(body) {
					continue
				}
				seen++
				if _, exempt := endStampExempt[fn.Name.Name]; exempt {
					continue
				}
				if !strings.Contains(body, "invalidateEndStampsTx(") {
					t.Errorf("%s writes session_waits without invalidateEndStampsTx: call it (D9) or list it in endStampExempt with a reason", fn.Name.Name)
				}
			}
		}
	}
	if seen < 5 { // positive control: SetSessionWaitRecord, clearTx, AnswerWait..., Restore..., ClearHeldWait
		t.Fatalf("the guard saw only %d writers: its pattern read nothing", seen)
	}
}
