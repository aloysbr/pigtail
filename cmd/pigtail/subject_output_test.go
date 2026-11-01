package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// runCobraIO runs a root-less command built by mk against s, capturing stdout
// and stderr on separate pipes (never merged).
func runCobraIO(t *testing.T, s *stubDaemon, mk func() *cobra.Command, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	testutil.SerialOnly(t) // swaps os.Stdout
	withStubDaemon(t, s, func() {
		savedOut, savedErr := os.Stdout, os.Stderr
		outR, outW, _ := os.Pipe()
		errR, errW, _ := os.Pipe()
		os.Stdout, os.Stderr = outW, errW
		var ob, eb bytes.Buffer
		done := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(&ob, outR); done <- struct{}{} }()
		go func() { _, _ = io.Copy(&eb, errR); done <- struct{}{} }()
		cmd := mk()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		if cmd.ErrOrStderr() == os.Stderr {
			cmd.SetErr(io.Discard)
		}
		err = cmd.Execute()
		_ = outW.Close()
		_ = errW.Close()
		<-done
		<-done
		os.Stdout, os.Stderr = savedOut, savedErr
		stdout, stderr = ob.String(), eb.String()
	})
	return stdout, stderr, err
}

// runRootIO runs `pigtail <args>` through the real root command against s:
// stdout, os.Stderr and cobra's own error line ("Error: …", printed by the
// root as main does) each on its own buffer, never merged.
func runRootIO(t *testing.T, s *stubDaemon, args ...string) (stdout, stderr, cobraErr string, err error) {
	t.Helper()
	var eb bytes.Buffer
	stdout, stderr, err = runCobraIO(t, s, func() *cobra.Command {
		root := newRootCmd()
		root.SetErr(&eb)
		return root
	}, append([]string{"--http", strings.TrimPrefix(s.srv.URL, "http://")}, args...)...)
	return stdout, stderr, eb.String(), err
}

func outputRows(t *testing.T, st *store.Store, subjectID string) []pigtail.SubjectOutput {
	t.Helper()
	out, err := st.ListSubjectOutputs(context.Background(), subjectID, store.OutputFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func bindWS(t *testing.T, st *store.Store, subjectID string) {
	t.Helper()
	t.Setenv(cmux.EnvWorkspaceID, attachWS)
	if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: attachWS, SubjectID: subjectID, Source: "attach", BoundAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

// outputEnv clears the pane and session variables the agent's own shell may carry.
func outputEnv(t *testing.T) {
	t.Helper()
	t.Setenv(cmux.EnvWorkspaceID, "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
}

var outputLineRE = regexp.MustCompile(`^✓ output out_[0-9a-f]{12} \(capture\) → a\.png\n$`)

func TestSubjectOutputAddBound(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	s, st := realSubjectDaemon(t)
	bindWS(t, st, "sub_p")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-1234567890")
	p := filepath.Join(t.TempDir(), "a.png")
	out, _, err := runCobraIO(t, s, subjectCmd, "output", "add", "--kind", "capture", "--path", p)
	if err != nil || !outputLineRE.MatchString(out) {
		t.Fatalf("add = %q %v", out, err)
	}
	out, _, err = runCobraIO(t, s, subjectCmd, "output", "add", "--kind", "capture", "--path", p)
	if err != nil || !regexp.MustCompile(`^= output out_[0-9a-f]{12} already registered\n$`).MatchString(out) {
		t.Fatalf("replay = %q %v", out, err)
	}
	rows := outputRows(t, st, "sub_p")
	if len(rows) != 1 || rows[0].SessionID != "sess-1234567890" || rows[0].Title != "a.png" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestSubjectOutputAddUnbound(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	s, st := realSubjectDaemon(t)
	p := filepath.Join(t.TempDir(), "a.png")
	out, _, err := runCobraIO(t, s, subjectCmd, "output", "add", "--kind", "capture", "--path", p)
	if exitCode(err) != 2 || err.Error() != "no subject: pass <subject_id> or bind the workspace" || out != "" {
		t.Fatalf("unbound = %q %v (exit %d)", out, err, exitCode(err))
	}
	out, errOut, err := runCobraIO(t, s, subjectCmd, "output", "add", "--kind", "capture", "--path", p, "--quiet-unbound")
	if err != nil || out != "" || errOut != "" {
		t.Fatalf("--quiet-unbound = %q %q %v", out, errOut, err)
	}
	if rows := outputRows(t, st, "sub_p"); len(rows) != 0 {
		t.Fatalf("rows = %+v", rows)
	}
}

// Verify gap 1: a pane with a UUID whose lookup answers 404 (unbound, or
// bound to a subject this scope cannot read) is "not bound", not an error.
func TestSubjectOutputAddPaneLookup404(t *testing.T) {
	for _, tc := range []struct {
		name string
		cell func(work, perso string) string
		bind bool
	}{
		{"uuid unbound", func(_, perso string) string { return perso }, false},
		{"bound elsewhere", func(work, _ string) string { return work }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withCells(t, tc.cell)
			outputEnv(t)
			s, st := realSubjectDaemon(t)
			t.Setenv(cmux.EnvWorkspaceID, attachWS)
			if tc.bind {
				bindWS(t, st, "sub_p")
			}
			p := filepath.Join(t.TempDir(), "a.png")
			out, _, err := runCobraIO(t, s, subjectCmd, "output", "add", "--kind", "capture", "--path", p)
			if exitCode(err) != 2 || err.Error() != errNoSubject.Error() || out != "" {
				t.Fatalf("= %q %v (exit %d)", out, err, exitCode(err))
			}
			out, _, err = runCobraIO(t, s, subjectCmd, "output", "add", "--kind", "capture", "--path", p, "--quiet-unbound")
			if err != nil || out != "" {
				t.Fatalf("--quiet-unbound = %q %v", out, err)
			}
			if !slices.Contains(pathsOf(s), "GET /cmux-workspaces/"+attachWS) {
				t.Fatalf("lookup never reached: %v", pathsOf(s))
			}
			if rows := outputRows(t, st, "sub_p"); len(rows) != 0 {
				t.Fatalf("rows = %+v", rows)
			}
		})
	}
}

func pathsOf(s *stubDaemon) []string {
	s.pathsMu.Lock()
	defer s.pathsMu.Unlock()
	return slices.Clone(s.paths)
}

func TestSubjectOutputAddQuietUnboundKeepsRealErrors(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	s, _ := realSubjectDaemon(t)
	_, _, err := runCobraIO(t, s, subjectCmd, "output", "add", "sub_zz", "--kind", "capture", "--path", "/a/b.png", "--quiet-unbound")
	if err == nil || exitCode(err) == 0 {
		t.Fatalf("unknown subject with --quiet-unbound = %v", err)
	}
}

func TestSubjectOutputAddQuietUnboundResolutionError(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	s, st := realSubjectDaemonWith(t, func(r chi.Router, _ *store.Store) {
		r.Get("/cmux-workspaces/{wsid}", func(w http.ResponseWriter, req *http.Request) {
			envelopeResponder{}.Error(w, req, http.StatusInternalServerError, pigtail.ErrInternal, "boom", nil)
		})
	})
	t.Setenv(cmux.EnvWorkspaceID, attachWS)
	out, _, err := runCobraIO(t, s, subjectCmd, "output", "add", "--kind", "capture", "--path", "/a/b.png", "--quiet-unbound")
	if err == nil || exitCode(err) == 0 || out != "" {
		t.Fatalf("resolution 500 with --quiet-unbound = %q %v", out, err)
	}
	if !slices.Contains(pathsOf(s), "GET /cmux-workspaces/"+attachWS) {
		t.Fatalf("faulty route never reached: %v", pathsOf(s))
	}
	if rows := outputRows(t, st, "sub_p"); len(rows) != 0 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestSubjectOutputAddRelativePath(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	s, st := realSubjectDaemon(t)
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	t.Chdir(tmp)
	if _, _, err := runCobraIO(t, s, subjectCmd, "output", "add", "sub_p", "--kind", "capture", "--path", "rel.png"); err != nil {
		t.Fatal(err)
	}
	rows := outputRows(t, st, "sub_p")
	if len(rows) != 1 || rows[0].Path != filepath.Join(tmp, "rel.png") {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestSubjectOutputAddJSONOnly(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	s, _ := realSubjectDaemon(t)
	out, _, err := runCobraIO(t, s, subjectCmd, "output", "add", "sub_p", "--kind", "capture", "--path", "/a/b.png", "--json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(out))
	var res pigtail.SubjectOutputAddResult
	if err := dec.Decode(&res); err != nil || !res.Created || res.Output.Key != "/a/b.png" {
		t.Fatalf("json = %q %v", out, err)
	}
	if rest, _ := io.ReadAll(dec.Buffered()); strings.TrimSpace(string(rest)) != "" || dec.More() {
		t.Fatalf("extra bytes after the JSON object: %q", out)
	}
}

func TestSubjectOutputAddTitleTruncated(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	s, st := realSubjectDaemon(t)
	if _, _, err := runCobraIO(t, s, subjectCmd, "output", "add", "sub_p", "--kind", "file", "--path", "/a/b", "--title", strings.Repeat("é", 80)); err != nil {
		t.Fatal(err)
	}
	rows := outputRows(t, st, "sub_p")
	if len(rows) != 1 || len([]rune(rows[0].Title)) != 60 || !strings.HasSuffix(rows[0].Title, "…") {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestSubjectOutputAddTS(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	s, st := realSubjectDaemon(t)
	if _, _, err := runCobraIO(t, s, subjectCmd, "output", "add", "sub_p", "--kind", "file", "--path", "/a/b", "--ts", "2026-10-21T10:00:00.250Z"); err != nil {
		t.Fatal(err)
	}
	if rows := outputRows(t, st, "sub_p"); len(rows) != 1 || rows[0].TS != 1792576800250 {
		t.Fatalf("rows = %+v", rows)
	}
	_, _, err := runCobraIO(t, s, subjectCmd, "output", "add", "sub_p", "--kind", "file", "--path", "/a/c", "--ts", "hier")
	if err == nil || !strings.Contains(err.Error(), "--ts") || !strings.Contains(err.Error(), "RFC 3339") {
		t.Fatalf("--ts hier = %v", err)
	}
	if rows := outputRows(t, st, "sub_p"); len(rows) != 1 {
		t.Fatalf("refused --ts still stored: %+v", rows)
	}
}

func TestSubjectOutputLs(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	s, _ := realSubjectDaemon(t)
	dir := t.TempDir()
	kept, gone := filepath.Join(dir, "kept.png"), filepath.Join(dir, "gone.png")
	for _, p := range []string{kept, gone} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runCobraIO(t, s, subjectCmd, "output", "add", "sub_p", "--kind", "capture", "--path", p); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCobraIO(t, s, subjectCmd, "output", "ls", "sub_p")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 || strings.Join(strings.Fields(lines[0]), " ") != "SEQ KIND TITLE KEY SESSION TS EXISTS" {
		t.Fatalf("ls = %q", out)
	}
	if !strings.HasSuffix(lines[1], "yes") || !strings.Contains(lines[2], "gone.png") || !strings.HasSuffix(lines[2], "no") {
		t.Fatalf("exists column = %q", out)
	}
	out, _, err = runCobraIO(t, s, subjectCmd, "output", "ls", "sub_p", "--json")
	var rows []pigtail.SubjectOutput
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 2 {
		t.Fatalf("ls --json = %q %v", out, err)
	}
	out, _, err = runCobraIO(t, s, subjectCmd, "output", "ls", "sub_p", "--json", "--since", "1")
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0].Seq != 2 {
		t.Fatalf("ls --since = %q %v", out, err)
	}
}

func TestSubjectOutputPurgeCLI(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	s, _ := realSubjectDaemon(t)
	if _, _, err := runCobraIO(t, s, subjectCmd, "output", "add", "sub_p", "--kind", "capture", "--path", "/a/b.png"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCobraIO(t, s, subjectCmd, "output", "purge", "sub_p", "--key", "/a/b.png")
	if err != nil || !regexp.MustCompile(`^✓ purged out_[0-9a-f]{12}\n$`).MatchString(out) {
		t.Fatalf("purge = %q %v", out, err)
	}
	if out, _, _ := runCobraIO(t, s, subjectCmd, "output", "ls", "sub_p"); !strings.Contains(out, "purged") {
		t.Fatalf("ls after purge = %q", out)
	}
	out, errOut, cobraErr, err := runRootIO(t, s, "subject", "output", "purge", "sub_p", "--key", "/nope/x.png")
	if err == nil || exitCode(err) != 1 || err.Error() != "no such output" || out != "" || errOut != "" || cobraErr != "Error: no such output\n" {
		t.Fatalf("unknown key = stdout %q stderr %q cobra %q %v", out, errOut, cobraErr, err)
	}
	out, errOut, cobraErr, err = runRootIO(t, s, "subject", "output", "purge", "sub_zz", "--key", "/a/b.png")
	if err == nil || exitCode(err) != 1 || err.Error() != "NOT_FOUND: no such subject" || out != "" || errOut != "" || cobraErr != "Error: NOT_FOUND: no such subject\n" {
		t.Fatalf("unknown subject = stdout %q stderr %q cobra %q %v", out, errOut, cobraErr, err)
	}
}

// Verify gap 2: --cross-scope of add and purge is wired to the opt-in header.
func TestSubjectOutputCrossScopeWrite(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	s, st := realSubjectDaemon(t)
	wantScopeMismatch(t, "perso add on work", runCLI(t, s, subjectCmd, "output", "add", "sub_t", "--kind", "file", "--path", "/a/t"))
	if rows := outputRows(t, st, "sub_t"); len(rows) != 0 {
		t.Fatalf("refused add landed: %+v", rows)
	}
	if err := runCLI(t, s, subjectCmd, "output", "add", "sub_t", "--kind", "file", "--path", "/a/t", "--cross-scope"); err != nil {
		t.Fatalf("add --cross-scope: %v", err)
	}
	wantScopeMismatch(t, "perso purge on work", runCLI(t, s, subjectCmd, "output", "purge", "sub_t", "--key", "/a/t"))
	if rows := outputRows(t, st, "sub_t"); len(rows) != 1 || rows[0].PurgedAt != nil {
		t.Fatalf("after refused purge: %+v", rows)
	}
	if err := runCLI(t, s, subjectCmd, "output", "purge", "sub_t", "--key", "/a/t", "--cross-scope"); err != nil {
		t.Fatalf("purge --cross-scope: %v", err)
	}
	if rows := outputRows(t, st, "sub_t"); len(rows) != 1 || rows[0].PurgedAt == nil {
		t.Fatalf("after purge: %+v", rows)
	}
}

func TestSubjectOutputAddMetaInvalid(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	s, st := realSubjectDaemon(t)
	out, _, err := runCobraIO(t, s, subjectCmd, "output", "add", "sub_p", "--kind", "file", "--path", "/a/b", "--meta", "{not json")
	if err == nil || exitCode(err) != 1 || err.Error() != "meta: invalid JSON" || out != "" {
		t.Fatalf("invalid --meta = %q %v", out, err)
	}
	if slices.Contains(pathsOf(s), "POST /subjects/sub_p/outputs") || len(outputRows(t, st, "sub_p")) != 0 {
		t.Fatalf("invalid --meta still sent: %v", pathsOf(s))
	}
}

func TestSubjectShowOutputsLine(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	s, _ := realSubjectDaemon(t)
	out, _, err := runCobraIO(t, s, subjectCmd, "show", "sub_p")
	if err != nil || strings.Contains(out, "outputs:") {
		t.Fatalf("show without output = %q %v", out, err)
	}
	if _, _, err := runCobraIO(t, s, subjectCmd, "output", "add", "sub_p", "--kind", "file", "--path", "/a/b"); err != nil {
		t.Fatal(err)
	}
	out, _, err = runCobraIO(t, s, subjectCmd, "show", "sub_p")
	if err != nil || !regexp.MustCompile(`(?m)^  outputs: 1 \(last \d{4}-\d{2}-\d{2} \d{2}:\d{2}\)$`).MatchString(out) {
		t.Fatalf("show = %q %v", out, err)
	}
}
