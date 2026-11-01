package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/internal/space/spacetest"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestSubjectOffTopicCLI(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	t.Setenv(cmux.EnvWorkspaceID, attachWS)
	savedClaude, savedSession := spawnCallerClaude, spawnCallerSession
	t.Cleanup(func() { spawnCallerClaude, spawnCallerSession = savedClaude, savedSession })
	spawnCallerClaude = func() (claudesession.Proc, bool, error) { return claudesession.Proc{PID: 4242}, true, nil }
	spawnCallerSession = func(string, int) (string, bool) { return "sess-caller", true }

	out, err := runSubject(t, s, "off-topic", "ls")
	if err != nil || !strings.Contains(out, "no off-topic workspace") {
		t.Fatalf("ls empty = %q %v", out, err)
	}
	first, err := runSubject(t, s, "off-topic", "declare", "--reason", "micro-fix", "--space", "Inbox")
	since, ok := strings.CutPrefix(strings.TrimSpace(first), "✓ cmux workspace "+attachWS+" declared off-topic (since ")
	if err != nil || !ok || !strings.HasSuffix(since, ") → folder Inbox") { // D7 (sub_x000000000000019): the folder, Inbox without a space
		t.Fatalf("declare = %q %v", first, err)
	}
	if _, perr := time.Parse("15:04", strings.TrimSuffix(since, ") → folder Inbox")); perr != nil { // D19 n° 1: (since HH:MM)
		t.Fatalf("declare since not HH:MM: %q %v", since, perr)
	}
	// D19 n° 2: declaring again (200) prints the same line, with the first declaration's time.
	if again, err := runSubject(t, s, "off-topic", "declare", "--reason", "autre"); err != nil || again != first {
		t.Fatalf("declare again = %q %v, want %q", again, err, first)
	}
	out, err = runSubject(t, s, "off-topic", "ls", "--json")
	if err != nil || !strings.Contains(out, attachWS) || !strings.Contains(out, `"reason":"micro-fix"`) || !strings.Contains(out, `"session_id":"sess-caller"`) {
		t.Fatalf("ls --json (reason, session_id) = %q %v", out, err)
	}
	if out, err := runSubject(t, s, "off-topic", "lift"); err != nil || !strings.Contains(out, "✓ cmux workspace "+attachWS+": off-topic lifted") {
		t.Fatalf("lift = %q %v", out, err)
	}
	if _, err := runSubject(t, s, "off-topic", "lift"); err == nil || !strings.Contains(err.Error(), "no off-topic declaration on cmux workspace "+attachWS) {
		t.Fatalf("lift absent = %v", err)
	}
	// No Claude above this pigtail: the declaration still goes through, without a session id.
	spawnCallerClaude = func() (claudesession.Proc, bool, error) { return claudesession.Proc{}, false, nil }
	if _, err := runSubject(t, s, "off-topic", "declare", "--space", "Inbox"); err != nil {
		t.Fatal(err)
	}
	if out, _ := runSubject(t, s, "off-topic", "ls", "--json"); strings.Contains(out, "session_id") {
		t.Fatalf("session_id without a caller = %q", out)
	}
	// D19 (sorties 5 et 6): the text table. tabwriter pads each column to its widest cell (2 spaces
	// minimum), so the header is compared field by field, not as one literal line.
	out, err = runSubject(t, s, "off-topic", "ls")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if err != nil || len(lines) != 2 || strings.Join(strings.Fields(lines[0]), " ") != "WORKSPACE SINCE SESSION FOLDER REASON" {
		t.Fatalf("ls header = %q %v", out, err)
	}
	if f := strings.Fields(lines[1]); len(f) != 5 || f[0] != attachWS || f[3] != "-" || f[4] != "Inbox" { // no reason: 5 fields, SESSION "-", FOLDER Inbox last
		t.Fatalf("ls row (want WS, date, time, -, Inbox) = %q", lines[1])
	} else if _, perr := time.ParseInLocation("2006-01-02 15:04", f[1]+" "+f[2], time.Local); perr != nil {
		t.Fatalf("ls SINCE not 2006-01-02 15:04: %q %v", f[1]+" "+f[2], perr)
	}
	// D19 (sortie 9): a 201-rune reason is refused with the daemon's 400 text, as the CLI shows it.
	if _, err := runSubject(t, s, "off-topic", "declare", "--reason", strings.Repeat("é", 201)); err == nil || !strings.Contains(err.Error(), "INVALID_REQUEST: reason: one line, 200 characters at most") {
		t.Fatalf("declare 201 runes = %v", err)
	}
	// D8: --if-session lifts only the declaring session's row (the SessionEnd hook).
	if _, err := runSubject(t, s, "off-topic", "lift"); err != nil {
		t.Fatal(err)
	}
	spawnCallerClaude = func() (claudesession.Proc, bool, error) { return claudesession.Proc{PID: 4242}, true, nil }
	if _, err := runSubject(t, s, "off-topic", "declare", "--space", "Inbox"); err != nil {
		t.Fatal(err)
	}
	if _, err := runSubject(t, s, "off-topic", "lift", "--if-session", "sess-other"); err == nil || !strings.Contains(err.Error(), "no off-topic declaration on cmux workspace "+attachWS) {
		t.Fatalf("lift --if-session other = %v", err)
	}
	if out, _ := runSubject(t, s, "off-topic", "ls", "--json"); !strings.Contains(out, `"session_id":"sess-caller"`) {
		t.Fatalf("another session's end lifted the declaration: %q", out)
	}
	if out, err := runSubject(t, s, "off-topic", "lift", "--if-session", "sess-caller"); err != nil || !strings.Contains(out, "off-topic lifted") {
		t.Fatalf("lift --if-session same = %q %v", out, err)
	}
	// D19 (sortie 8): declare on a bound workspace is refused with the daemon's 409 text, as the CLI shows it.
	if _, err := runSubject(t, s, "attach", "sub_p"); err != nil {
		t.Fatal(err)
	}
	if _, err := runSubject(t, s, "off-topic", "declare"); err == nil || !strings.Contains(err.Error(), "CONFLICT: cmux workspace bound to a subject (detach it first)") {
		t.Fatalf("declare on a bound workspace = %v", err)
	}
	// A3, CLI side: this perso (master) cell lifts a work declaration only with --cross-scope (the WithCrossScopeWrite branch of lift).
	if err := st.CreateOffTopic(&pigtail.WorkspaceOffTopic{CmuxWorkspaceID: attachWS2, Scope: "globex", DeclaredAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := runSubject(t, s, "off-topic", "lift", "--workspace", attachWS2); err == nil || !strings.Contains(err.Error(), "no off-topic declaration on cmux workspace "+attachWS2) {
		t.Fatalf("lift work row without --cross-scope = %v", err)
	}
	if _, err := st.GetOffTopic(attachWS2); err != nil {
		t.Fatalf("work row lifted without --cross-scope: %v", err)
	}
	if out, err := runSubject(t, s, "off-topic", "lift", "--workspace", attachWS2, "--cross-scope"); err != nil || !strings.Contains(out, "✓ cmux workspace "+attachWS2+": off-topic lifted") {
		t.Fatalf("lift work row with --cross-scope = %q %v", out, err)
	}
	if _, err := st.GetOffTopic(attachWS2); err == nil {
		t.Fatal("work row kept after lift --cross-scope")
	}
}

// offTopicSpaceDaemon serves the real routes with the builtin space table and
// thm_p (sub_p's theme, perso) in « Allotment »; no Claude above the CLI.
func offTopicSpaceDaemon(t *testing.T) (*stubDaemon, *store.Store) {
	t.Helper()
	testutil.SerialOnly(t) // swaps spawnCallerClaude
	tbl, err := space.Parse([]byte(spacetest.FormerYAML), "spacetest", scopetest.TwoTable(t))
	if err != nil {
		t.Fatal(err)
	}
	s, st := realSubjectDaemonSvc(t, func(svc *handlers.Services) { svc.Spaces = tbl }, nil)
	sp := "Allotment"
	if err := st.UpdateTheme("thm_p", pigtail.ThemeUpdate{Space: &sp}); err != nil {
		t.Fatal(err)
	}
	saved := spawnCallerClaude
	t.Cleanup(func() { spawnCallerClaude = saved })
	spawnCallerClaude = func() (claudesession.Proc, bool, error) { return claudesession.Proc{}, false, nil }
	return s, st
}

func seedOffTopic(t *testing.T, st *store.Store, ws, sc, sp string) {
	t.Helper()
	if err := st.CreateOffTopic(&pigtail.WorkspaceOffTopic{CmuxWorkspaceID: ws, Scope: sc, Space: sp, DeclaredAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}

const offTopicElsewhere = " already declared off-topic elsewhere: folder unchanged\n" // D7

// D1, D6, D7: the folder shown comes from the daemon's resolved_space; --space
// moves it; ls has a FOLDER column, « (inferred) » when inferred.
func TestSubjectOffTopicSpaceCLI(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := offTopicSpaceDaemon(t)
	out, err := runSubject(t, s, "off-topic", "declare", "--workspace", attachWS, "--reason", "revue de sub_p")
	if err != nil || !strings.HasPrefix(out, "✓ cmux workspace "+attachWS+" declared off-topic (since ") || !strings.HasSuffix(out, ") → folder Allotment\n") {
		t.Fatalf("declare citing sub_p = %q %v", out, err)
	}
	out, err = runSubject(t, s, "off-topic", "ls")
	if err != nil || !strings.Contains(out, "Allotment (inferred)") {
		t.Fatalf("ls inferred = %q %v", out, err)
	}
	out, err = runSubject(t, s, "off-topic", "declare", "--workspace", attachWS, "--space", "Garden")
	if err != nil || !strings.HasSuffix(out, ") → folder Garden\n") || strings.Contains(out, "elsewhere") {
		t.Fatalf("redeclare --space Garden = %q %v", out, err)
	}
	if o, _ := st.GetOffTopic(attachWS); o == nil || o.Space != "Garden" {
		t.Fatalf("stored = %+v, want space Garden", o)
	}
	out, err = runSubject(t, s, "off-topic", "ls")
	if err != nil || !strings.Contains(out, "Garden") || strings.Contains(out, "(inferred)") {
		t.Fatalf("ls explicit = %q %v", out, err)
	}
	out, err = runSubject(t, s, "off-topic", "ls", "--json")
	var rows []pigtail.WorkspaceOffTopic
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0].Space != "Garden" || rows[0].ResolvedSpace != "Garden" {
		t.Fatalf("ls --json = %q %v", out, err)
	}
	if _, err := runSubject(t, s, "off-topic", "declare", "--workspace", attachWS2, "--space", "Nope"); err == nil || !strings.Contains(err.Error(), "unknown space (known: ") || strings.Contains(err.Error(), "Nope") {
		t.Fatalf("unknown --space = %v", err)
	}
	cmd := offTopicDeclareCmd()
	if f := cmd.Flags().Lookup("space"); f == nil || f.Usage != "cmux sidebar folder for this off-topic session, one of pigtail theme spaces; required unless a sub_… cited in --reason (not \"<id> refusé\") resolves one; \"Inbox\" files it in Inbox" {
		t.Fatalf("--space help = %+v", f)
	}
}

// Redeclaring one's own row without --space: the normal line with the stored
// folder, never « elsewhere » (D6).
func TestRedeclareWithoutSpaceShowsStoredFolder(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := offTopicSpaceDaemon(t)
	seedOffTopic(t, st, attachWS, "perso", "Garden")
	out, err := runSubject(t, s, "off-topic", "declare", "--workspace", attachWS)
	if err != nil || !strings.HasPrefix(out, "✓ cmux workspace "+attachWS+" declared off-topic (since ") || !strings.HasSuffix(out, ") → folder Garden\n") || strings.Contains(out, "elsewhere") {
		t.Fatalf("redeclare without --space = %q %v", out, err)
	}
}

// A redeclaration with --space left unchanged (another scope's row, masked or
// not): « folder unchanged », naming neither the requested space nor the old.
func TestDeclareRefusedRedeclareShowsNoFolder(t *testing.T) {
	t.Run("non-master on the master's row", func(t *testing.T) {
		withCells(t, func(work, _ string) string { return work })
		s, st := offTopicSpaceDaemon(t)
		seedOffTopic(t, st, attachWS, "perso", "Garden")
		out, err := runSubject(t, s, "off-topic", "declare", "--workspace", attachWS, "--space", "Vineyard")
		if err != nil || out != "✓ cmux workspace "+attachWS+offTopicElsewhere {
			t.Fatalf("declare = %q %v", out, err)
		}
		if o, _ := st.GetOffTopic(attachWS); o.Space != "Garden" {
			t.Fatalf("row changed: %+v", o)
		}
	})
	t.Run("master cross-scope on another scope's row", func(t *testing.T) {
		withCells(t, func(_, perso string) string { return perso })
		s, st := offTopicSpaceDaemon(t)
		seedOffTopic(t, st, attachWS, "globex", "Vineyard")
		out, err := runSubject(t, s, "off-topic", "declare", "--workspace", attachWS, "--space", "Allotment", "--cross-scope")
		if err != nil || out != "✓ cmux workspace "+attachWS+offTopicElsewhere {
			t.Fatalf("declare = %q %v", out, err)
		}
		if o, _ := st.GetOffTopic(attachWS); o.Space != "Vineyard" {
			t.Fatalf("row changed: %+v", o)
		}
	})
}

// A non-master redeclaring the master's row without --space (masked answer):
// « folder unchanged », no space named, not even Inbox.
func TestRedeclareMaskedWithoutSpaceSaysElsewhere(t *testing.T) {
	withCells(t, func(work, _ string) string { return work })
	s, st := offTopicSpaceDaemon(t)
	seedOffTopic(t, st, attachWS, "perso", "Garden")
	out, err := runSubject(t, s, "off-topic", "declare", "--workspace", attachWS)
	if err != nil || out != "✓ cmux workspace "+attachWS+offTopicElsewhere {
		t.Fatalf("declare = %q %v", out, err)
	}
}

// pflag names a flag's argument after the first `quoted` word of its usage:
// the --space help must not read « --space pigtail theme spaces ».
func TestDeclareSpaceHelpArgName(t *testing.T) {
	t.Parallel()
	cmd := offTopicDeclareCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	help := out.String()
	if !strings.Contains(help, "--space string") || strings.Contains(help, "--space pigtail") {
		t.Fatalf("--help shows the --space argument wrong:\n%s", help)
	}
	if !strings.Contains(help, "one of pigtail theme spaces; required unless a sub_… cited in --reason (not \"<id> refusé\") resolves one; \"Inbox\" files it in Inbox") {
		t.Fatalf("--space usage text changed:\n%s", help)
	}
}

// D4 (sub_x000000000000033): --space Inbox (space.NoSubject) is the sentinel;
// the CLI prints the folder Inbox, never « elsewhere », on a first declaration and on a redeclaration.
func TestSubjectOffTopicInboxSentinelCLI(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := offTopicSpaceDaemon(t)
	out, err := runSubject(t, s, "off-topic", "declare", "--workspace", attachWS, "--space", "Inbox")
	if err != nil || !strings.HasSuffix(out, ") → folder Inbox\n") || strings.Contains(out, "elsewhere") {
		t.Fatalf("declare --space Inbox = %q %v", out, err)
	}
	seedOffTopic(t, st, attachWS2, "perso", "Allotment")
	out, err = runSubject(t, s, "off-topic", "declare", "--workspace", attachWS2, "--space", "Inbox")
	if err != nil || !strings.HasSuffix(out, ") → folder Inbox\n") || strings.Contains(out, "elsewhere") {
		t.Fatalf("redeclare --space Inbox = %q %v", out, err)
	}
	// D4: no space resolved → the CLI fails with the daemon's message.
	_, err = runSubject(t, s, "off-topic", "declare", "--workspace", "0D7E5B1C-3A44-4F0E-9D11-3C1E2B9A7F01", "--reason", "micro-fix")
	if err == nil || !strings.Contains(err.Error(), "INVALID_REQUEST") || !strings.Contains(err.Error(), "--space") || !strings.Contains(err.Error(), "Inbox") {
		t.Fatalf("declare without space = %v, want the D4 refusal", err)
	}
}

// A8: `off-topic set-space` rewrites only the space of an existing row (master
// cell); --space is required, ” lets the daemon infer it.
func TestOffTopicSetSpaceCLI(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	tbl, err := space.Parse([]byte(spacetest.FormerYAML), "spacetest", scopetest.TwoTable(t))
	if err != nil {
		t.Fatal(err)
	}
	s, st := realSubjectDaemonSvc(t, func(svc *handlers.Services) { svc.Spaces = tbl }, nil)
	declared := time.Now().UTC().Truncate(time.Millisecond)
	if err := st.CreateOffTopic(&pigtail.WorkspaceOffTopic{CmuxWorkspaceID: attachWS, Scope: "perso", SessionID: "sess-x", Reason: "keep me", Space: "Allotment", DeclaredAt: declared}); err != nil {
		t.Fatal(err)
	}
	if _, err := runSubject(t, s, "off-topic", "set-space", "--workspace", attachWS); err == nil || !strings.Contains(err.Error(), "--space is required") {
		t.Fatalf("set-space without --space = %v", err)
	}
	out, err := runSubject(t, s, "off-topic", "set-space", "--workspace", attachWS, "--space", "Garden")
	if err != nil || !strings.Contains(out, "folder Garden") {
		t.Fatalf("set-space = %q %v", out, err)
	}
	o, err := st.GetOffTopic(attachWS)
	if err != nil || o.Space != "Garden" || o.SessionID != "sess-x" || o.Reason != "keep me" || !o.DeclaredAt.Equal(declared) {
		t.Fatalf("row = %+v %v: only the space may change", o, err)
	}
	if _, err := runSubject(t, s, "off-topic", "set-space", "--workspace", attachWS, "--space", "Nope"); err == nil || !strings.Contains(err.Error(), "unknown space") {
		t.Fatalf("set-space unknown = %v", err)
	}
	if o, _ := st.GetOffTopic(attachWS); o.Space != "Garden" {
		t.Fatalf("a refused call changed the space to %q", o.Space)
	}
}
