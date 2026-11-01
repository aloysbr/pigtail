package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/launchmargin"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func fourCells(t *testing.T, session string) (map[string]string, string, string) {
	root := t.TempDir()
	dev := filepath.Join(root, "dev")
	work := filepath.Join(dev, "clients", "work")
	_ = os.MkdirAll(work, 0o700)
	dirs := withCellRows(t, map[string]cellRow{
		"wa": {Scope: "globex", Account: "m", Home: work, Rank: 0},
		"ha": {Scope: "perso", Account: "m", Home: dev, Rank: 1},
		"wb": {Scope: "globex", Account: "j", Home: work, Rank: 4},
		"hb": {Scope: "perso", Account: "j", Home: dev, Rank: 5},
	}, session)
	return dirs, dev, work
}

func TestSpawnCellSameScopeWithSubject(t *testing.T) {
	dirs, dev, _ := fourCells(t, "ha")
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	cwd := filepath.Join(dev, "repo")
	_ = os.MkdirAll(cwd, 0o700)
	f.launchesAs(dirs["hb"], cwd)
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--cell", "hb", "--prompt", spawnPrompt, "--cwd", cwd)
	if err != nil {
		t.Fatalf("same-scope --cell = %v", err)
	}
	spec := f.created[0]
	if !strings.Contains(spec.Command, " c hb ") || !slices.Contains(spec.Env, "CLAUDE_CONFIG_DIR="+dirs["hb"]) {
		t.Fatalf("not launched under hb: %q %v", spec.Command, spec.Env)
	}
	if len(f.preTrustCalls) != 1 || !strings.HasPrefix(f.preTrustCalls[0], dirs["hb"]+"|") {
		t.Fatalf("pre-trust not on the target cell: %v", f.preTrustCalls)
	}
	if !strings.Contains(out, "cell hb") {
		t.Fatalf("result does not name hb: %s", out)
	}
	es := spawnEntries(t)
	if len(es) != 1 || es[0].Cell != "hb" || es[0].SubjectID != "sub_p" {
		t.Fatalf("registry = %+v", es)
	}
}

func TestSpawnMasterCrossScopeDefaultsToSameAccountCell(t *testing.T) {
	_, _, work := fourCells(t, "ha")
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	cwd := filepath.Join(work, "station")
	_ = os.MkdirAll(cwd, 0o700)
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_t", "--cross-scope", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run")
	if err != nil || !strings.Contains(out, "cell      wa (") || !strings.Contains(out, "same account m in the subject's scope") {
		t.Fatalf("default cell = %v\n%s", err, out)
	}
}

// AC1: the visible differences for the shell callers' plain subject spawn
// (no --cell, own-scope subject), text and --json.
func TestSpawnCellOriginOfAPlainSubjectSpawn(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	cwd := t.TempDir()
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run")
	if err != nil || !strings.Contains(out, "cell      p ("+persoCellDir(t)+") — caller's cell\n") {
		t.Fatalf("plain dry-run cell line = %v\n%s", err, out)
	}
	out, _, err = runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run", "--json")
	var v map[string]any
	if err != nil || json.Unmarshal([]byte(out), &v) != nil || v["cell_origin"] != "caller's cell" || v["cell"] != "p" {
		t.Fatalf("plain dry-run json = %v %v\n%s", err, v, out)
	}
	if sl, ok := v["subjectless"]; !ok || sl != false {
		t.Fatalf("subjectless = %v (present %v), want false", sl, ok)
	}
}

func TestSpawnCellRefusals(t *testing.T) {
	cases := []struct {
		name, session string
		edit          func(t *testing.T, dirs map[string]string)
		args          []string
		want          []string
		forbid        []string // never in a non-master refusal
	}{
		{"cell of another scope than the subject", "wa", nil,
			[]string{"--subject", "sub_t", "--cell", "ha"},
			[]string{"cell ha does not run subject sub_t's scope", "--cell wa,wb"}, // R7: ids comma-separated, sorted
			[]string{"perso", "globex"}},
		{"unknown cell", "ha", nil,
			[]string{"--subject", "sub_p", "--cell", "zz"},
			[]string{`unknown cell "zz"`, "ha,hb,wa,wb"}, nil},
		{"target config dir missing", "ha", func(t *testing.T, dirs map[string]string) { _ = os.RemoveAll(dirs["hb"]) },
			[]string{"--subject", "sub_p", "--cell", "hb"},
			[]string{"cell hb's config dir", "does not exist"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dirs, _, _ := fourCells(t, c.session)
			if c.edit != nil {
				c.edit(t, dirs)
			}
			s, _ := realSubjectDaemon(t)
			f := installSpawnFake(t)
			args := append(c.args, "--prompt", spawnPrompt, "--cwd", t.TempDir())
			_, _, err := runSpawnCLI(t, s, args...)
			if err == nil || !strings.HasPrefix(err.Error(), "session spawn: ") || !strings.Contains(err.Error(), " — ") {
				t.Fatalf("err = %v", err)
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("missing %q in %v", w, err)
				}
			}
			for _, w := range c.forbid {
				if strings.Contains(err.Error(), w) {
					t.Errorf("non-master refusal names %q: %v", w, err)
				}
			}
			if len(f.created) != 0 {
				t.Fatal("a refusal created a workspace")
			}
		})
	}
}

func TestSpawnMasterCrossScopeNoSameAccountCell(t *testing.T) {
	root := t.TempDir()
	withCellRows(t, map[string]cellRow{
		"wa": {Scope: "globex", Account: "m", Home: filepath.Join(root, "work"), Rank: 0},
		"hb": {Scope: "perso", Account: "j", Home: root, Rank: 5},
	}, "hb")
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_t", "--cross-scope", "--prompt", spawnPrompt, "--cwd", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "no cell of subject sub_t's scope shares account j with cell hb") ||
		!strings.Contains(err.Error(), "candidates: wa") || len(f.created) != 0 {
		t.Fatalf("err = %v", err)
	}
}

// Spec row 5: master --cross-scope + --cell of the subject's scope, real launch.
// The manual-attach hint keeps --cross-scope: it is computed from the CALLER's
// cell (perso) against the subject (work), not from the target cell (work) —
// bindHint reading the target's scope would silently drop --cross-scope. The
// bind is a conflict, so the hint is the replace form (#174): … --replace --cross-scope.
func TestSpawnMasterCrossScopeCellAttachHintKeepsCrossScope(t *testing.T) {
	dirs, _, _ := fourCells(t, "ha")
	s, st := realSubjectDaemon(t)
	f := installSpawnFake(t)
	cwd := t.TempDir()
	f.launchesAs(dirs["wa"], cwd)
	// The new workspace id is already bound elsewhere: attach answers CONFLICT.
	if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: spawnNewWS, SubjectID: "sub_p", Source: "attach", BoundAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := runSpawnCLI(t, s, "--subject", "sub_t", "--cross-scope", "--cell", "wa", "--prompt", spawnPrompt, "--cwd", cwd)
	if err != nil {
		t.Fatalf("row 5 launch = %v\n%s", err, errOut)
	}
	if spec := f.created[0]; !strings.Contains(spec.Command, " c wa ") || !slices.Contains(spec.Env, "CLAUDE_CONFIG_DIR="+dirs["wa"]) {
		t.Fatalf("not launched under wa: %q %v", spec.Command, spec.Env)
	}
	if !strings.Contains(errOut, "pigtail subject attach sub_t --workspace "+spawnNewWS+" --source ptnew --replace --cross-scope") {
		t.Fatalf("attach hint dropped --cross-scope: %q", errOut)
	}
	if !strings.Contains(out, "cell wa") {
		t.Fatalf("result = %s", out)
	}
}

// nothing of a target subject is read, bound or written — and since the
// burst detector, no subject status is read at all: the live entry on sub_t stays to prove it.
func TestSpawnSubjectlessIntoOtherScopeCell(t *testing.T) {
	dirs, dev, _ := fourCells(t, "wa") // non-master work caller
	s, _ := realSubjectDaemonWith(t, nil)
	f := installSpawnFake(t)
	cwd := filepath.Join(dev, "home", "pigtail-wt", "x")
	_ = os.MkdirAll(cwd, 0o700)
	f.launchesAs(dirs["hb"], cwd)
	reg := spawnreg.Open(spawnreg.DefaultPath())
	if err := reg.Append(spawnreg.Entry{TS: time.Now().UTC(), SubjectID: "sub_t", WorkspaceID: spawnOtherWS, Cell: "wa", Result: "launched"}); err != nil {
		t.Fatal(err)
	}
	f.addProc(6000, "/x/bin/claude", "CMUX_WORKSPACE_ID="+spawnOtherWS) // live: no longer read for any count
	out, _, err := runSpawnCLI(t, s, "--cell", "hb", "--cwd", cwd, "--prompt", "/example-skill reprends")
	if err != nil {
		t.Fatalf("spawn = %v", err)
	}
	if got := subjectCalls(s); len(got) != 0 {
		t.Fatalf("/subjects requests = %v, want none (no cap tally reads a subject any more)", got)
	}
	if !strings.Contains(out, "no subject") || !strings.Contains(out, "cell hb") {
		t.Fatalf("result = %s", out)
	}
	es := spawnEntries(t)
	if len(es) != 2 || es[1].SubjectID != "" || es[1].Cell != "hb" {
		t.Fatalf("registry = %+v, want the seeded sub_t entry then the new one", es)
	}
	if f.created[0].Name != "x" {
		t.Fatalf("default name = %q, want the cwd basename", f.created[0].Name)
	}
}

// dry-run, text and --json (§ Spec « Dry-run »).
func TestSpawnSubjectlessDryRun(t *testing.T) {
	_, dev, _ := fourCells(t, "wa")
	s, _ := realSubjectDaemonWith(t, nil)
	installSpawnFake(t)
	cwd := mk(t, filepath.Join(dev, "home", "x"))
	out, _, err := runSpawnCLI(t, s, "--cell", "hb", "--cwd", cwd, "--prompt", spawnPrompt, "--dry-run")
	if err != nil {
		t.Fatalf("dry-run = %v", err)
	}
	for _, want := range []string{
		"subject   none — the new session binds itself (subjectless spawn into cell hb)",
		"cell      hb (", ") — --cell",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run lacks %q:\n%s", want, out)
		}
	}
	out, _, err = runSpawnCLI(t, s, "--cell", "hb", "--cwd", cwd, "--prompt", spawnPrompt, "--dry-run", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, out)
	}
	if v["cell_origin"] != "--cell" || v["subjectless"] != true || v["subject_id"] != "" || v["subject_title"] != "" || v["scope"] != "" || v["cell"] != "hb" {
		t.Fatalf("dry-run json = %v", v)
	}
	if _, ok := v["new_worktree"]; ok {
		t.Fatalf("new_worktree present without the flag: %v", v)
	}
	if got := subjectCalls(s); len(got) != 0 { // no live entry here: not even the tally reads one
		t.Errorf("dry-run touched a subject route: %v", got)
	}
}

// (Ada, A): a subjectless spawn never renames the calling tab, and never
// looks up the caller's binding. The fake puts the caller in cmux (spawnCallerWS)
// and the daemon holds no binding for it: on #174 code an unbound caller is
// renamed after the subject — here there is none ("↗ " + an empty title).
func TestSpawnSubjectlessLeavesTheCallerTitle(t *testing.T) {
	dirs, dev, _ := fourCells(t, "wa")
	s, _ := realSubjectDaemonWith(t, nil)
	f := installSpawnFake(t)
	cwd := mk(t, filepath.Join(dev, "home", "x"))
	f.launchesAs(dirs["hb"], cwd)
	out, _, err := runSpawnCLI(t, s, "--cell", "hb", "--cwd", cwd, "--prompt", spawnPrompt, "--dry-run", "--json")
	if err != nil {
		t.Fatalf("dry-run = %v", err)
	}
	var v struct {
		CallerTitle *string `json:"caller_title"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.CallerTitle == nil || *v.CallerTitle != "" {
		t.Fatalf("dry-run caller_title = %v, want present and \"\" (%v)\n%s", v.CallerTitle, err, out)
	}
	out, _, err = runSpawnCLI(t, s, "--cell", "hb", "--cwd", cwd, "--prompt", spawnPrompt, "--dry-run")
	if err != nil || strings.Contains(out, "would rename") {
		t.Fatalf("text dry-run announces a caller rename (err %v):\n%s", err, out)
	}
	out, _, err = runSpawnCLI(t, s, "--cell", "hb", "--cwd", cwd, "--prompt", spawnPrompt)
	if err != nil {
		t.Fatalf("spawn = %v", err)
	}
	if len(f.renamed) != 0 || strings.Contains(out, "caller:") {
		t.Fatalf("subjectless spawn renamed the caller: %q\n%s", f.renamed, out)
	}
	s.pathsMu.Lock()
	defer s.pathsMu.Unlock()
	for _, p := range s.paths {
		if strings.HasPrefix(p, "GET /cmux-workspaces/") {
			t.Fatalf("caller binding looked up on a subjectless spawn: %s", p)
		}
	}
}

// , pending (ex-TestSpawnSubjectlessUnverified): nothing starts in the new
// tab. The pending result names no subject: « left open » alone, no attach,
// nothing renamed.
func TestSpawnSubjectlessPending(t *testing.T) {
	_, dev, _ := fourCells(t, "wa") // non-master work caller
	s, _ := realSubjectDaemonWith(t, nil)
	f := installSpawnFake(t) // no launchesAs: nothing starts in the new workspace
	cwd := mk(t, filepath.Join(dev, "home", "x"))
	out, _, err := runSpawnCLI(t, s, "--cell", "hb", "--cwd", cwd, "--prompt", spawnPrompt, "--json")
	if err != nil {
		t.Fatalf("err = %v, want a pending launch (exit 0)", err)
	}
	var v struct {
		State       string  `json:"state"`
		Bound       *bool   `json:"bound"`
		CallerTitle *string `json:"caller_title"`
	}
	if jErr := json.Unmarshal([]byte(out), &v); jErr != nil || v.State != "pending" || v.Bound == nil || *v.Bound || v.CallerTitle == nil || *v.CallerTitle != "" {
		t.Fatalf("json = %+v (%v)\n%s", v, jErr, out)
	}
	if got := subjectCalls(s); len(got) != 0 {
		t.Fatalf("subjectless pending spawn touched a subject route: %v", got)
	}
	es := registryEntries(t)
	assertNoUnverified(t, es)
	if entryKinds(es) != "launch-reserve,launch-ref" || es[0].SubjectID != "" || es[1].SubjectID != "" {
		t.Fatalf("registry = %+v, want a subjectless reserve then launch-ref", es)
	}
	f.stamp = "auto" // a fresh permission record for the text call
	text, _, err := runSpawnCLI(t, s, "--cell", "hb", "--cwd", cwd, "--prompt", spawnPrompt)
	if err != nil || !strings.Contains(text, "left open") || strings.Contains(text, "bound to") {
		t.Fatalf("text = %v\n%s, want « left open » with no « bound to »", err, text)
	}
}

func TestSpawnSubjectlessRefusals(t *testing.T) {
	cases := []struct {
		name, session string
		args          []string // --prompt appended
		cwdUnder      string   // "dev" | "work" | "outside" | "" (no --cwd)
		table         string   // "" = fourCells | "nohomes" (no cwd_home) | "ambiguous" (wa and hb share dev)
		want          string
		wantAlso      string // checked like want when non-empty (R4's next step)
		mainCk        bool   // the fake answers « shared main checkout » for the --cwd
	}{
		{"no subject no cell", "wa", nil, "dev", "", "--subject is required", "", false},
		{"own scope cell", "wa", []string{"--cell", "wb"}, "work", "", "cell wb runs this session's own scope", "pigtail subject create", false}, // R4
		{"no cwd", "wa", []string{"--cell", "hb"}, "", "", "a subjectless spawn needs --cwd", "", false},
		{"cross-scope flag", "ha", []string{"--cell", "wa", "--cross-scope"}, "work", "", "--cross-scope has no effect without --subject", "", false},
		{"cwd outside every home", "wa", []string{"--cell", "hb"}, "outside", "", "is under no cell's home", "", false},
		{"cwd in the caller's home", "wa", []string{"--cell", "hb"}, "work", "", "is under the home of cells wa,wb", "", false},
		{"table without cwd_home", "wa", []string{"--cell", "hb"}, "dev", "nohomes", "claude-cells.yaml carries no cwd_home", "", false},
		{"ambiguous home", "wa", []string{"--cell", "hb"}, "dev", "ambiguous", "cells hb,wa of different scopes share the home", "", false}, // R14, CLI text
		// Spec row 12 on the incident's path: a subjectless --cwd under the target's home
		// that is a shared main checkout → R16, naming --new-worktree.
		{"shared main checkout", "wa", []string{"--cell", "hb"}, "dev", "", "is the main checkout of a repo with linked worktrees", "--new-worktree", true},
		{"new-worktree without branch", "wa", []string{"--cell", "hb", "--new-worktree"}, "dev", "", "--new-worktree without --subject needs --branch", "", false}, // R18
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, dev, work := fourCells(t, c.session)
			switch c.table {
			case "nohomes":
				withCellRows(t, map[string]cellRow{
					"wa": {Scope: "globex", Account: "m"}, "hb": {Scope: "perso", Account: "j"},
				}, c.session)
			case "ambiguous":
				withCellRows(t, map[string]cellRow{
					"wa": {Scope: "globex", Account: "m", Home: dev}, "hb": {Scope: "perso", Account: "j", Home: dev},
				}, c.session)
			}
			s, _ := realSubjectDaemonWith(t, nil)
			f := installSpawnFake(t)
			f.mainCk = c.mainCk
			args := append([]string{}, c.args...)
			switch c.cwdUnder {
			case "dev":
				args = append(args, "--cwd", mk(t, filepath.Join(dev, "r")))
			case "work":
				args = append(args, "--cwd", mk(t, filepath.Join(work, "r")))
			case "outside":
				args = append(args, "--cwd", t.TempDir())
			}
			args = append(args, "--prompt", spawnPrompt)
			_, _, err := runSpawnCLI(t, s, args...)
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), " — ") {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if c.wantAlso != "" && !strings.Contains(err.Error(), c.wantAlso) {
				t.Fatalf("err = %v, want also %q", err, c.wantAlso)
			}
			if strings.Contains(err.Error(), "perso") || strings.Contains(err.Error(), "globex") {
				t.Fatalf("refusal names a scope: %v", err)
			}
			if len(f.created) != 0 {
				t.Fatal("created a workspace")
			}
		})
	}
}

func mk(t *testing.T, d string) string { t.Helper(); _ = os.MkdirAll(d, 0o700); return d }

// Review Focus 1
func TestSpawnSubjectlessEntryNeverBlocksASubject(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	reg := spawnreg.Open(spawnreg.DefaultPath())
	_ = reg.Append(spawnreg.Entry{TS: time.Now().UTC(), SubjectID: "", WorkspaceID: spawnOtherWS, Cell: "t", Result: "launched"})
	f.addProc(6000, "/x/bin/claude", "CMUX_WORKSPACE_ID="+spawnOtherWS)
	if _, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run"); err != nil {
		t.Fatalf("a live subjectless entry blocked a subject spawn: %v", err)
	}
}

// a subjectless spawn keeps the caller's group; the
// daemon's grace is what stops it from being pulled out. Green on the base
// on purpose: a guard against the spawn ever dropping the group.
func TestSpawnSubjectlessKeepsCallerGroup(t *testing.T) {
	dirs, dev, _ := fourCells(t, "wa")
	s, _ := realSubjectDaemonWith(t, nil)
	f := installSpawnFake(t)
	cwd := filepath.Join(dev, "home", "pigtail-wt", "x")
	_ = os.MkdirAll(cwd, 0o700)
	f.launchesAs(dirs["hb"], cwd)
	if _, _, err := runSpawnCLI(t, s, "--cell", "hb", "--cwd", cwd, "--prompt", "/example-skill reprends"); err != nil {
		t.Fatalf("spawn = %v", err)
	}
	if len(f.created) != 1 {
		t.Fatalf("created %d workspaces", len(f.created))
	}
	if spec := f.created[0]; spec.GroupRef != "workspace_group:3" {
		t.Fatalf("placement = %+v, want the caller's group", spec)
	}
}

// Account rotation.

const (
	rotUUIDM = "11111111-1111-4111-8111-111111111111"
	rotUUIDJ = "33333333-3333-4333-8333-333333333333"
)

// writeUsage writes a fresh .claude.json into a cell's config dir: 7-day
// window at p7 % renewing in r7, 5-hour window at 5 % renewing in an hour.
func writeUsage(t *testing.T, dir, uuid string, p7 int, r7 time.Duration) {
	t.Helper()
	now := time.Now().UTC()
	iso := func(d time.Duration) string { return now.Add(d).Format("2006-01-02T15:04:05.000000+00:00") }
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b := maxCellFile(t, uuid, now.Add(-time.Minute), p7, iso(r7), 5, iso(time.Hour))
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// rotationCells: fourCells with readings — j's 7-day window renews first.
func rotationCells(t *testing.T, session string) (map[string]string, string, string) {
	dirs, dev, work := fourCells(t, session)
	for _, id := range []string{"wa", "ha"} {
		writeUsage(t, dirs[id], rotUUIDM, 40, 5*24*time.Hour)
	}
	for _, id := range []string{"wb", "hb"} {
		writeUsage(t, dirs[id], rotUUIDJ, 40, 24*time.Hour)
	}
	return dirs, dev, work
}

func dryRunJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	return v
}

func TestSpawnRotationPicksAccountSameScope(t *testing.T) {
	dirs, dev, _ := rotationCells(t, "ha")
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	cwd := mk(t, filepath.Join(dev, "repo"))
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run", "--json")
	v := dryRunJSON(t, out)
	if err != nil || v["cell"] != "hb" || v["cell_origin"] != "rotation: account j" {
		t.Fatalf("dry-run = %v cell %v origin %v", err, v["cell"], v["cell_origin"])
	}
	f.launchesAs(dirs["hb"], cwd)
	_, stderr, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	spec := f.created[0]
	if !strings.Contains(spec.Command, " c hb ") || !slices.Contains(spec.Env, "CLAUDE_CONFIG_DIR="+dirs["hb"]) {
		t.Fatalf("not launched under hb: %q %v", spec.Command, spec.Env)
	}
	if len(f.preTrustCalls) != 1 || !strings.HasPrefix(f.preTrustCalls[0], dirs["hb"]+"|") {
		t.Fatalf("pre-trust not on the picked cell: %v", f.preTrustCalls)
	}
	if strings.Contains(stderr, "rotation") {
		t.Fatalf("a fresh pick warns nothing, stderr:\n%s", stderr)
	}
}

func TestSpawnRotationCrossScope(t *testing.T) {
	_, _, work := rotationCells(t, "ha")
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	cwd := mk(t, filepath.Join(work, "station"))
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_t", "--cross-scope", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run", "--json")
	v := dryRunJSON(t, out)
	if err != nil || v["cell"] != "wb" || v["cell_origin"] != "rotation: account j" {
		t.Fatalf("cross-scope = %v cell %v origin %v, want wb (j renews first), not the same-account wa", err, v["cell"], v["cell_origin"])
	}
}

// The launch margin guard replaces the old
// invariant (pickCell never called with --cell): --cell goes through the
// guard once (pickCell still not called; origin --cell; no warning under the
// margin), and --over-margin imposes the cell without any usage read.
func TestSpawnRotationFlagWins(t *testing.T) {
	rotationCells(t, "ha")
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	saved := pickCell
	t.Cleanup(func() { pickCell = saved })
	called := false
	pickCell = func(tbl *claudeprofile.Table, sc string, l map[string]time.Time) (claudeprofile.Cell, claudeusage.Choice, error) {
		called = true
		return saved(tbl, sc, l)
	}
	guards, reads := countGuard(t)
	out, stderr, err := runSpawnCLI(t, s, "--subject", "sub_p", "--cell", "ha", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run", "--json")
	v := dryRunJSON(t, out)
	if err != nil || v["cell"] != "ha" || v["cell_origin"] != "--cell" || called || strings.Contains(stderr, "rotation") || strings.Contains(stderr, "launch margin") {
		t.Fatalf("--cell = %v cell %v origin %v picked %v stderr %q", err, v["cell"], v["cell_origin"], called, stderr)
	}
	if v["rotation"] != nil {
		t.Fatalf("rotation = %v, want null (not tried)", v["rotation"])
	}
	if *guards != 1 || *reads == 0 {
		t.Fatalf("guard calls = %d, usage reads = %d, want one guard that reads", *guards, *reads)
	}
	*guards, *reads = 0, 0
	out, _, err = runSpawnCLI(t, s, "--subject", "sub_p", "--cell", "ha", "--over-margin", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run", "--json")
	v = dryRunJSON(t, out)
	m, _ := v["margin"].(map[string]any)
	if err != nil || v["cell"] != "ha" || m["decision"] != "forced" || *guards != 1 || *reads != 0 {
		t.Fatalf("--over-margin = %v cell %v margin %v guards %d reads %d, want forced ha with no usage read", err, v["cell"], v["margin"], *guards, *reads)
	}
}

// countGuard wraps the launchDeps seam: how many guards ran, how many usage
// files they read.
func countGuard(t *testing.T) (*int, *int) {
	t.Helper()
	testutil.SerialOnly(t) // swaps launchDeps
	saved := launchDeps
	t.Cleanup(func() { launchDeps = saved })
	guards, reads := new(int), new(int)
	launchDeps = func(cells *claudeprofile.Table, v launchmargin.Viewer) launchmargin.Deps {
		*guards++
		d := saved(cells, v)
		rf := d.ReadFile
		d.ReadFile = func(p string) ([]byte, error) { *reads++; return rf(p) }
		return d
	}
	return guards, reads
}

// saturateM puts account m (cells ha, wa) over the 7-day margin; saturateJ, j.
func saturateM(t *testing.T, dirs map[string]string) {
	t.Helper()
	for _, id := range []string{"wa", "ha"} {
		writeUsage(t, dirs[id], rotUUIDM, 97, 2*24*time.Hour)
	}
}

func saturateJ(t *testing.T, dirs map[string]string) {
	t.Helper()
	for _, id := range []string{"wb", "hb"} {
		writeUsage(t, dirs[id], rotUUIDJ, 96, 30*time.Hour)
	}
}

func TestSpawnCellFlagToSaturatedAccountSwitches(t *testing.T) {
	dirs, dev, _ := rotationCells(t, "ha")
	saturateM(t, dirs)
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	cwd := mk(t, filepath.Join(dev, "repo"))
	f.launchesAs(dirs["hb"], cwd)
	_, stderr, err := runSpawnCLI(t, s, "--subject", "sub_p", "--cell", "ha", "--prompt", spawnPrompt, "--cwd", cwd)
	if err != nil {
		t.Fatalf("spawn: %v\n%s", err, stderr)
	}
	spec := f.created[0]
	if !strings.Contains(spec.Command, " c hb ") || !slices.Contains(spec.Env, "CLAUDE_CONFIG_DIR="+dirs["hb"]) {
		t.Fatalf("not launched under hb: %q %v", spec.Command, spec.Env)
	}
	if !strings.Contains(stderr, "warning: launch margin: cell ha (account m) is over the margin — launching on hb (account j) instead") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestSpawnCellUnderMarginUnchanged(t *testing.T) {
	_, dev, _ := rotationCells(t, "ha")
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	cwd := mk(t, filepath.Join(dev, "repo"))
	out, stderr, err := runSpawnCLI(t, s, "--subject", "sub_p", "--cell", "ha", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run", "--json")
	v := dryRunJSON(t, out)
	if err != nil || v["cell"] != "ha" || v["cell_origin"] != "--cell" || strings.Contains(stderr, "launch margin") {
		t.Fatalf("under margin = %v cell %v origin %v stderr %q", err, v["cell"], v["cell_origin"], stderr)
	}
}

func TestSpawnSubjectlessSaturatedCellSwitchesSameScope(t *testing.T) {
	dirs, _, work := rotationCells(t, "ha") // the master caller
	saturateM(t, dirs)
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	cwd := mk(t, filepath.Join(work, "station"))
	out, stderr, err := runSpawnCLI(t, s, "--cell", "wa", "--cwd", cwd, "--prompt", spawnPrompt, "--dry-run", "--json")
	v := dryRunJSON(t, out)
	if err != nil || v["cell"] != "wb" || v["cell_origin"] != "margin switch: account j" {
		t.Fatalf("subjectless switch = %v cell %v origin %v\n%s", err, v["cell"], v["cell_origin"], stderr)
	}
	if !strings.Contains(stderr, "warning: launch margin: cell wa (account m) is over the margin — launching on wb (account j) instead") {
		t.Fatalf("stderr = %q", stderr)
	}
	m, _ := v["margin"].(map[string]any)
	if m["decision"] != "switched" || m["requested"] != "wa" || m["final"] != "wb" {
		t.Fatalf("margin = %v", v["margin"])
	}
}

func TestSpawnSubjectlessSwitchBoundedForNonMaster(t *testing.T) {
	dirs, dev, _ := rotationCells(t, "wa") // a work caller
	saturateM(t, dirs)
	s, _ := realSubjectDaemonWith(t, nil)
	installSpawnFake(t)
	cwd := mk(t, filepath.Join(dev, "home", "x"))
	out, stderr, err := runSpawnCLI(t, s, "--cell", "ha", "--cwd", cwd, "--prompt", spawnPrompt, "--dry-run", "--json")
	v := dryRunJSON(t, out)
	if err != nil || v["cell"] != "hb" || v["cell_origin"] != "margin switch" {
		t.Fatalf("bounded switch = %v cell %v origin %v", err, v["cell"], v["cell_origin"])
	}
	if !strings.Contains(stderr, "launching on hb instead") || strings.Contains(stderr, "account") || strings.Contains(stderr, "perso") {
		t.Fatalf("bounded warning = %q", stderr)
	}
}

func TestSpawnSubjectlessRefusalBoundedForNonMaster(t *testing.T) {
	dirs, dev, _ := rotationCells(t, "wa")
	saturateM(t, dirs)
	saturateJ(t, dirs)
	s, _ := realSubjectDaemonWith(t, nil)
	f := installSpawnFake(t)
	cwd := mk(t, filepath.Join(dev, "home", "x"))
	_, _, err := runSpawnCLI(t, s, "--cell", "ha", "--cwd", cwd, "--prompt", spawnPrompt)
	if exitCode(err) != 3 || len(f.created) != 0 {
		t.Fatalf("exit %d (%v) created %d, want 3 and nothing", exitCode(err), err, len(f.created))
	}
	if msg := err.Error(); strings.Contains(msg, "perso") || strings.Contains(msg, "first release: ") || strings.Contains(msg, "(account") || !strings.Contains(msg, "cell ha") || !strings.Contains(msg, "--over-margin") {
		t.Fatalf("bounded refusal = %q", msg)
	}
}

func TestSpawnWholeScopeOverRefused(t *testing.T) {
	dirs, dev, _ := rotationCells(t, "ha")
	saturateM(t, dirs)
	saturateJ(t, dirs)
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	cwd := mk(t, filepath.Join(dev, "repo"))
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--cell", "ha", "--prompt", spawnPrompt, "--cwd", cwd)
	if exitCode(err) != 3 || len(f.created) != 0 {
		t.Fatalf("exit %d (%v) created %d, want 3 and nothing launched", exitCode(err), err, len(f.created))
	}
	if !strings.Contains(err.Error(), "every account of scope perso is over the margin") || !strings.Contains(err.Error(), "first release: j at ") {
		t.Fatalf("refusal = %q", err)
	}
}

func TestSpawnOverMarginFlagImposesCell(t *testing.T) {
	dirs, dev, _ := rotationCells(t, "ha")
	saturateM(t, dirs)
	saturateJ(t, dirs)
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	cwd := mk(t, filepath.Join(dev, "repo"))
	f.launchesAs(dirs["ha"], cwd)
	_, stderr, err := runSpawnCLI(t, s, "--subject", "sub_p", "--cell", "ha", "--over-margin", "--prompt", spawnPrompt, "--cwd", cwd)
	if err != nil || len(f.created) != 1 || !strings.Contains(f.created[0].Command, " c ha ") {
		t.Fatalf("--over-margin = %v created %+v", err, f.created)
	}
	if !strings.Contains(stderr, "warning: launch margin: --over-margin") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestSpawnNoCellSubjectAllOverRefused(t *testing.T) {
	dirs, dev, _ := rotationCells(t, "ha")
	saturateM(t, dirs)
	saturateJ(t, dirs)
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	cwd := mk(t, filepath.Join(dev, "repo"))
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd)
	if exitCode(err) != 3 || len(f.created) != 0 || !strings.Contains(err.Error(), "--over-margin") {
		t.Fatalf("exit %d (%v) created %d, want 3 (was: launched with a warning)", exitCode(err), err, len(f.created))
	}
	// --over-margin: the Pick fallback, with the rotation warning
	out, stderr, err := runSpawnCLI(t, s, "--subject", "sub_p", "--over-margin", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run", "--json")
	v := dryRunJSON(t, out)
	if err != nil || v["cell"] != "hb" || !strings.Contains(stderr, "every account of scope perso is over the margin") {
		t.Fatalf("--over-margin = %v cell %v stderr %q", err, v["cell"], stderr)
	}
}

func TestSpawnSwitchRespectsConfigDirCheck(t *testing.T) {
	dirs, dev, _ := rotationCells(t, "ha")
	saturateM(t, dirs)
	jpFile := filepath.Join(dirs["hb"], ".claude.json")
	hb, err := os.ReadFile(jpFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dirs["hb"]); err != nil { // the switch target's dir is gone, its reading served from memory
		t.Fatal(err)
	}
	saved := launchDeps
	t.Cleanup(func() { launchDeps = saved })
	launchDeps = func(cells *claudeprofile.Table, v launchmargin.Viewer) launchmargin.Deps {
		d := saved(cells, v)
		rf := d.ReadFile
		d.ReadFile = func(p string) ([]byte, error) {
			if p == jpFile {
				return hb, nil
			}
			return rf(p)
		}
		return d
	}
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	cwd := mk(t, filepath.Join(dev, "repo"))
	_, _, err = runSpawnCLI(t, s, "--subject", "sub_p", "--cell", "ha", "--prompt", spawnPrompt, "--cwd", cwd)
	if err == nil || !strings.Contains(err.Error(), "cell hb's config dir") || len(f.created) != 0 {
		t.Fatalf("err = %v created %d, want the config dir refusal on the final cell hb", err, len(f.created))
	}
}

func TestSpawnRotationUnavailableFallsBack(t *testing.T) {
	fourCells(t, "ha") // no .claude.json anywhere
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	out, stderr, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run", "--json")
	v := dryRunJSON(t, out)
	if err != nil || v["cell"] != "ha" || v["cell_origin"] != "caller's cell" {
		t.Fatalf("fallback = %v cell %v origin %v", err, v["cell"], v["cell_origin"])
	}
	if !strings.Contains(stderr, "warning: rotation: no candidate") || !strings.Contains(stderr, "falls back to cell ha (caller's cell)") {
		t.Fatalf("stderr %q, want the rotation warning and the fallback", stderr)
	}
	rot, _ := v["rotation"].(map[string]any)
	ws, _ := rot["warnings"].([]any)
	if len(ws) != 1 || !strings.HasPrefix(ws[0].(string), "rotation: no candidate") {
		t.Fatalf("rotation = %v, want rotation.warnings", v["rotation"])
	}
}

func TestSpawnRotationDryRunShowsRanking(t *testing.T) {
	rotationCells(t, "ha")
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "— rotation: account j\n") {
		t.Fatalf("cell line lacks the rotation origin:\n%s", out)
	}
	j, m := strings.Index(out, "  rotation  1. j on hb — fresh"), strings.Index(out, "  rotation  2. m on ha — fresh")
	if j < 0 || m < j {
		t.Fatalf("ranking not shown in order:\n%s", out)
	}
	out, _, _ = runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run", "--json")
	v := dryRunJSON(t, out)
	rot, _ := v["rotation"].(map[string]any)
	ch, _ := rot["choice"].(map[string]any)
	if rk, _ := ch["ranking"].([]any); len(rk) != 2 || ch["label"] != "j" {
		t.Fatalf("rotation json = %v", v["rotation"])
	}
}

func TestRotationOffSpawn(t *testing.T) {
	rotationCells(t, "ha")
	t.Setenv("PIGTAIL_ROTATION", "off")
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	out, stderr, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run", "--json")
	v := dryRunJSON(t, out)
	if err != nil || v["cell"] != "ha" || v["cell_origin"] != "caller's cell" {
		t.Fatalf("off = %v cell %v origin %v", err, v["cell"], v["cell_origin"])
	}
	if strings.Count(stderr, "rotation: off") != 1 {
		t.Fatalf("stderr %q, want one rotation: off line", stderr)
	}
}

// marginEntries are the launch margin rows of the spawn registry (Task 4).
func marginEntries(t *testing.T) []spawnreg.Entry {
	t.Helper()
	all, err := spawnreg.Open(spawnreg.DefaultPath()).Entries()
	if err != nil {
		t.Fatal(err)
	}
	var out []spawnreg.Entry
	for _, e := range all {
		if e.Kind == spawnreg.KindMargin {
			out = append(out, e)
		}
	}
	return out
}

// marginJournalRows are the launch-margin rows the daemon stored, any scope.
func marginJournalRows(t *testing.T, st *store.Store) []pigtail.JournalEntry {
	t.Helper()
	rows, err := st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: launchmargin.JournalKind})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestSpawnSwitchWritesMarginEntry(t *testing.T) {
	dirs, dev, _ := rotationCells(t, "ha")
	saturateM(t, dirs)
	s, st := realSubjectDaemon(t)
	f := installSpawnFake(t)
	cwd := mk(t, filepath.Join(dev, "repo"))
	f.launchesAs(dirs["hb"], cwd)
	if _, stderr, err := runSpawnCLI(t, s, "--subject", "sub_p", "--cell", "ha", "--prompt", spawnPrompt, "--cwd", cwd); err != nil {
		t.Fatalf("spawn: %v\n%s", err, stderr)
	}
	m := marginEntries(t)
	if len(m) != 1 || m[0].Cell != "ha" || m[0].FreshCell != "hb" || m[0].Result != "switched" || m[0].SubjectID != "sub_p" || m[0].Margin == "" {
		t.Fatalf("margin entries = %+v", m)
	}
	all, _ := spawnreg.Open(spawnreg.DefaultPath()).Entries()
	if n := len(spawnreg.Launches(all, time.Time{}, time.Now().UTC(), spawnreg.PendingOpts{})); n != 1 {
		t.Fatalf("launch entries = %d, want the spawn's own one, unchanged", n)
	}
	rows := marginJournalRows(t, st)
	if len(rows) != 1 || !strings.Contains(rows[0].Summary, "switched to hb") || rows[0].Source != "pigtail:spawns" {
		t.Fatalf("journal = %+v", rows)
	}
}

func TestSpawnDryRunWritesNoMarginEntry(t *testing.T) {
	dirs, dev, _ := rotationCells(t, "ha")
	saturateM(t, dirs)
	s, st := realSubjectDaemon(t)
	installSpawnFake(t)
	cwd := mk(t, filepath.Join(dev, "repo"))
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--cell", "ha", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run", "--json")
	if v := dryRunJSON(t, out); err != nil || v["cell"] != "hb" {
		t.Fatalf("dry-run = %v cell %v", err, v["cell"])
	}
	if m, rows := marginEntries(t), marginJournalRows(t, st); len(m) != 0 || len(rows) != 0 {
		t.Fatalf("a dry run traced: entries %+v journal %+v", m, rows)
	}
}

func TestSpawnRefusalWritesRefusedEntryAndJournal(t *testing.T) {
	dirs, dev, _ := rotationCells(t, "ha")
	saturateM(t, dirs)
	saturateJ(t, dirs)
	s, st := realSubjectDaemon(t)
	installSpawnFake(t)
	cwd := mk(t, filepath.Join(dev, "repo"))
	if _, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--cell", "ha", "--prompt", spawnPrompt, "--cwd", cwd); exitCode(err) != 3 {
		t.Fatalf("exit %d (%v), want 3", exitCode(err), err)
	}
	m := marginEntries(t)
	if len(m) != 1 || m[0].Result != "refused" || m[0].Cell != "ha" || !strings.HasPrefix(m[0].Margin, "refused perso frees ") || m[0].WorkspaceID != "" {
		t.Fatalf("margin entries = %+v", m)
	}
	rows := marginJournalRows(t, st)
	if len(rows) != 1 || !strings.Contains(rows[0].Summary, "refused cell ha") {
		t.Fatalf("journal = %+v", rows)
	}
	// The guard refuses before the launch reservation: no reserve, no launch
	// record — the margin row is the registry's only line.
	if es := registryEntries(t); len(es) != 0 {
		t.Fatalf("registry = %+v, want the margin row alone (no reservation)", es)
	}
}

func TestSpawnSubjectlessRefusalJournalBounded(t *testing.T) {
	dirs, dev, _ := rotationCells(t, "wa") // a work caller
	saturateM(t, dirs)
	saturateJ(t, dirs)
	s, st := realSubjectDaemon(t)
	installSpawnFake(t)
	cwd := mk(t, filepath.Join(dev, "home", "x"))
	if _, _, err := runSpawnCLI(t, s, "--cell", "ha", "--cwd", cwd, "--prompt", spawnPrompt); exitCode(err) != 3 {
		t.Fatalf("exit %d (%v), want 3", exitCode(err), err)
	}
	raw, err := os.ReadFile(spawnreg.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	m, rows := marginEntries(t), marginJournalRows(t, st)
	if len(m) != 1 || len(rows) != 1 || m[0].Margin == "" {
		t.Fatalf("entries %+v journal %+v, want one of each", m, rows)
	}
	for _, text := range []string{string(raw), rows[0].Summary} {
		if strings.Contains(text, "perso") || strings.Contains(text, "(account") || strings.Contains(text, "first release: ") {
			t.Fatalf("bounded trace names another scope or an account: %q", text)
		}
	}
}

func TestBurstIgnoresMarginEntries(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	appendLaunches(t, spawnBurstMax, time.Minute, func(e *spawnreg.Entry) {
		e.Kind, e.WorkspaceID, e.Result, e.Margin = spawnreg.KindMargin, "", "refused", "refused perso frees unknown"
	})
	all, _ := spawnreg.Open(spawnreg.DefaultPath()).Entries()
	if burstAt(all, time.Now()).full() {
		t.Fatal("margin entries filled the burst window")
	}
	if _, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run"); err != nil {
		t.Fatalf("spawn after %d margin entries: %v", spawnBurstMax, err)
	}
}

// Pin: a margin entry carries the subject but no workspace — the candidates
// loop by SubjectID finds no live session through it.
func TestSpawnCandidatesIgnoreMarginEntry(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	appendLaunches(t, 1, time.Minute, func(e *spawnreg.Entry) {
		e.Kind, e.SubjectID, e.WorkspaceID, e.Result, e.Margin = spawnreg.KindMargin, "sub_p", "", "switched", "switched ha -> hb"
	})
	if _, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run"); err != nil {
		t.Fatalf("a margin entry blocked the subject's spawn: %v", err)
	}
}

// A margin row is no launch: even carrying the WorkspaceID of a live
// workspace it never makes that workspace a live session of the subject.
func TestSpawnCandidatesIgnoreMarginEntryWithWorkspace(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	appendLaunches(t, 1, time.Minute, func(e *spawnreg.Entry) {
		e.Kind, e.SubjectID, e.WorkspaceID, e.Result, e.Margin = spawnreg.KindMargin, "sub_p", spawnOtherWS, "switched", "switched ha -> hb"
	})
	f.addProc(6000, "/x/bin/claude", "CMUX_WORKSPACE_ID="+spawnOtherWS)
	if _, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run"); err != nil {
		t.Fatalf("a margin entry naming a live workspace blocked the spawn: %v", err)
	}
}

// A real spawn re-checks under the launch reservation's lock (spawnGuards):
// a margin row naming a live workspace of the subject never blocks it there.
func TestSpawnReservationIgnoresMarginEntryWithWorkspace(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	f.launchesAs(persoCellDir(t), cwd)
	appendLaunches(t, 1, time.Minute, func(e *spawnreg.Entry) {
		e.Kind, e.SubjectID, e.WorkspaceID, e.Result, e.Margin = spawnreg.KindMargin, "sub_p", spawnOtherWS, "switched", "switched ha -> hb"
	})
	f.addProc(6000, "/x/bin/claude", "CMUX_WORKSPACE_ID="+spawnOtherWS)
	if _, stderr, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd); err != nil || len(f.created) != 1 {
		t.Fatalf("a margin entry naming a live workspace blocked the spawn: %v created %d\n%s", err, len(f.created), stderr)
	}
	if es := spawnEntries(t); len(es) != 1 || es[0].Result != spawnreg.ResultLaunched {
		t.Fatalf("spawn entries = %+v, want one launched", es)
	}
}

func TestSpawnJournalFailureDoesNotBlock(t *testing.T) {
	dirs, dev, _ := rotationCells(t, "ha")
	saturateM(t, dirs)
	saved := marginJournal
	t.Cleanup(func() { marginJournal = saved })
	marginJournal = func(*pigtail.Client, *pigtail.JournalEntry) error { return errors.New("daemon down") }
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	cwd := mk(t, filepath.Join(dev, "repo"))
	f.launchesAs(dirs["hb"], cwd)
	_, stderr, err := runSpawnCLI(t, s, "--subject", "sub_p", "--cell", "ha", "--prompt", spawnPrompt, "--cwd", cwd)
	if err != nil || len(f.created) != 1 {
		t.Fatalf("a failed journal blocked the spawn: %v created %d", err, len(f.created))
	}
	if !strings.Contains(stderr, "warning: launch margin: journal: daemon down") || len(marginEntries(t)) != 1 {
		t.Fatalf("stderr %q entries %+v", stderr, marginEntries(t))
	}
}
