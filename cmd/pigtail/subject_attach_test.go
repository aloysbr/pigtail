package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/testutil"
)

const (
	attachWS  = "29664DF9-67B9-4AAF-9419-B1FD8E3A8994"
	attachWS2 = "F8290864-F5B5-4B3F-9B93-0B027295DECC"
)

// stubCmuxRefs makes `workspace:N` resolve from a fixed table instead of cmux.
func stubCmuxRefs(t *testing.T, refs map[string]string) {
	t.Helper()
	testutil.SerialOnly(t) // swaps resolveCmuxRef
	prev := resolveCmuxRef
	resolveCmuxRef = func(ref string) (string, error) {
		if id, ok := refs[ref]; ok {
			return id, nil
		}
		return "", errors.New("no such workspace")
	}
	t.Cleanup(func() { resolveCmuxRef = prev })
}

func TestResolveCmuxWorkspaceArg(t *testing.T) {
	stubCmuxRefs(t, map[string]string{"workspace:4": attachWS2})
	t.Setenv(cmux.EnvWorkspaceID, "")
	if _, err := resolveCmuxWorkspaceArg(""); err == nil || !strings.Contains(err.Error(), "--workspace") {
		t.Fatalf("outside cmux = %v, want a hint at --workspace", err)
	}
	t.Setenv(cmux.EnvWorkspaceID, strings.ToLower(attachWS))
	if id, err := resolveCmuxWorkspaceArg(""); err != nil || id != attachWS {
		t.Fatalf("from env = %q %v", id, err)
	}
	if id, err := resolveCmuxWorkspaceArg("workspace:4"); err != nil || id != attachWS2 {
		t.Fatalf("ref = %q %v", id, err)
	}
	if _, err := resolveCmuxWorkspaceArg("workspace:9"); err == nil {
		t.Fatal("unknown ref resolved")
	}
	if _, err := resolveCmuxWorkspaceArg("my-workspace"); err == nil {
		t.Fatal("non-UUID accepted")
	}
}

// attach (current workspace, then a ref), show lists them, a second subject
// is refused until --replace, detach unbinds; perso writing work needs
// --cross-scope, and work never reaches a perso subject.
func TestSubjectAttachDetachE2E(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	stubCmuxRefs(t, map[string]string{"workspace:4": attachWS2})
	s, st := realSubjectDaemon(t)
	t.Setenv(cmux.EnvWorkspaceID, attachWS)

	if out, err := runSubject(t, s, "attach", "sub_p"); err != nil || !strings.Contains(out, attachWS) {
		t.Fatalf("attach = %q %v", out, err)
	}
	if _, err := runSubject(t, s, "attach", "sub_p", "--workspace", "workspace:4", "--source", "ptnew"); err != nil {
		t.Fatalf("attach ref: %v", err)
	}
	if b, err := st.GetCmuxBinding(attachWS2); err != nil || b.Source != "ptnew" || b.SubjectID != "sub_p" {
		t.Fatalf("ref binding = %+v %v", b, err)
	}
	sub, _ := showJSON(t, s, "sub_p")
	if strings.Join(sub.CmuxWorkspaces, ",") != attachWS+","+attachWS2 {
		t.Fatalf("show --json cmux_workspaces = %v", sub.CmuxWorkspaces)
	}
	if out, _ := runSubject(t, s, "show", "sub_p"); !strings.Contains(out, "cmux: "+attachWS) {
		t.Fatalf("show = %q", out)
	}

	// Work subject from a perso cell: --cross-scope, and first link wins.
	if _, err := runSubject(t, s, "attach", "sub_t", "--cross-scope"); err == nil || !strings.Contains(err.Error(), "already bound") {
		t.Fatalf("second subject = %v, want already bound", err)
	}
	if _, err := runSubject(t, s, "attach", "sub_t", "--replace"); err == nil {
		t.Fatal("perso wrote work without --cross-scope")
	}
	if _, err := runSubject(t, s, "attach", "sub_t", "--replace", "--cross-scope"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if b, _ := st.GetCmuxBinding(attachWS); b.SubjectID != "sub_t" {
		t.Fatalf("after replace bound to %q", b.SubjectID)
	}

	if _, err := runSubject(t, s, "detach", "sub_p", "--workspace", "workspace:4"); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if _, err := st.GetCmuxBinding(attachWS2); err == nil {
		t.Fatal("detach left the binding")
	}
	if _, err := runSubject(t, s, "detach", "sub_t"); err == nil {
		t.Fatal("perso detached work without --cross-scope")
	}
	if _, err := runSubject(t, s, "detach", "sub_t", "--cross-scope"); err != nil {
		t.Fatalf("detach work with --cross-scope: %v", err)
	}
}

// From a work cell, attach on a perso subject is the unknown-subject 404.
func TestSubjectAttachWorkCellNeverReachesPerso(t *testing.T) {
	withCells(t, func(work, _ string) string { return work })
	s, st := realSubjectDaemon(t)
	t.Setenv(cmux.EnvWorkspaceID, attachWS)
	_, err := runSubject(t, s, "attach", "sub_p", "--cross-scope")
	if err == nil || !strings.Contains(err.Error(), "no such subject") {
		t.Fatalf("work attach on perso = %v, want no such subject", err)
	}
	if _, err := st.GetCmuxBinding(attachWS); err == nil {
		t.Fatal("binding written")
	}
}

const (
	identifyUnknown = `{"caller":null,"focused":{}}`
	identifyKnown   = `{"caller":{"workspace_ref":"workspace:7","window_ref":"window:1"}}`
)

// fakeCmuxCLI installs a /bin/sh cmux stand-in that logs its arguments, then runs
// script. It returns the log path. Every attach test that needs cmux to
// answer `identify` goes through it; no test calls the real cmux.
func fakeCmuxCLI(t *testing.T, script string) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls.log")
	bin := filepath.Join(dir, "cmux")
	body := "#!/bin/sh\necho \"$*\" >> '" + log + "'\n" + script + "\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil { //nolint:gosec // an executable stand-in
		t.Fatal(err)
	}
	t.Setenv(cmux.EnvBundledCLI, bin)
	t.Cleanup(cmux.SetCallTimeoutForTest(time.Minute))
	return log
}

func identifyScript(json string) string {
	return "[ \"$1\" = identify ] || exit 1\ncat <<'EOF'\n" + json + "\nEOF"
}

func TestSubjectAttachRefusesWorkspaceCmuxDoesNotKnow(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	fakeCmuxCLI(t, identifyScript(identifyUnknown))
	s, st := realSubjectDaemon(t)
	t.Setenv(cmux.EnvWorkspaceID, attachWS)

	_, err := runSubject(t, s, "attach", "sub_p")
	if err == nil {
		t.Fatal("attach with an id cmux does not know succeeded")
	}
	for _, want := range []string{"$CMUX_WORKSPACE_ID (" + attachWS + ")", "périmé", "reprise", "Rien n'a été lié"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("env refusal %q lacks %q", err, want)
		}
	}
	if _, err := st.GetCmuxBinding(attachWS); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("binding after refusal = %v, want ErrNotFound", err)
	}

	_, err = runSubject(t, s, "attach", "sub_p", "--workspace", attachWS2)
	if err == nil {
		t.Fatal("attach --workspace <unknown UUID> succeeded")
	}
	for _, want := range []string{"workspace cmux " + attachWS2 + " inconnu de cmux", "Rien n'a été lié"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("flag refusal %q lacks %q", err, want)
		}
	}
	if _, err := st.GetCmuxBinding(attachWS2); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("binding after flag refusal = %v, want ErrNotFound", err)
	}
}

// Everything but an explicit "caller: null" lets the link through.
func TestSubjectAttachChecksOnlyWhatCmuxDenies(t *testing.T) {
	cases := []struct {
		name       string
		script     string
		args       []string
		timeout    time.Duration
		wantIdent  bool
		lowerEnvID bool
	}{
		{name: "known", script: identifyScript(identifyKnown), wantIdent: true, lowerEnvID: true},
		{name: "ref-path", script: identifyScript(identifyUnknown), args: []string{"--workspace", "workspace:4"}},
		{name: "fail-open/exit1", script: "exit 1", wantIdent: true},
		{name: "fail-open/garbage", script: "echo nope", wantIdent: true},
		{name: "fail-open/no-caller-key", script: identifyScript(`{"focused":{}}`), wantIdent: true},
		{name: "fail-open/timeout", script: "sleep 5", timeout: 300 * time.Millisecond, wantIdent: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withCells(t, func(_, perso string) string { return perso })
			stubCmuxRefs(t, map[string]string{"workspace:4": attachWS2})
			log := fakeCmuxCLI(t, c.script)
			if c.timeout > 0 {
				t.Cleanup(cmux.SetCallTimeoutForTest(c.timeout))
			}
			s, st := realSubjectDaemon(t)
			env := attachWS
			if c.lowerEnvID {
				env = strings.ToLower(attachWS)
			}
			t.Setenv(cmux.EnvWorkspaceID, env)
			wantWS := attachWS
			if len(c.args) > 0 {
				wantWS = attachWS2
			}
			out, err := runSubject(t, s, append([]string{"attach", "sub_p"}, c.args...)...)
			if err != nil {
				t.Fatalf("attach = %v", err)
			}
			if strings.Count(strings.TrimSpace(out), "\n") != 0 || !strings.HasPrefix(out, "✓ cmux workspace "+wantWS) {
				t.Fatalf("output = %q, want the single ✓ line", out)
			}
			if _, err := st.GetCmuxBinding(wantWS); err != nil {
				t.Fatalf("binding not written: %v", err)
			}
			calls, _ := os.ReadFile(log)
			if c.wantIdent && !strings.Contains(string(calls), "identify --workspace "+attachWS+"\n") {
				t.Fatalf("cmux calls = %q, want identify --workspace %s (upper case)", calls, attachWS)
			}
			if !c.wantIdent && strings.Contains(string(calls), "identify") {
				t.Fatalf("identify called on the ref path: %q", calls)
			}
		})
	}
}
