package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/pkg/pigtail"
	"github.com/go-chi/chi/v5"
)

// e2eDaemon serves the real subject routes, FreezeMW included, on a unix
// socket in a temp dir, with the maintenance state in that same dir.
func e2eDaemon(t *testing.T, st maintenance.State) string {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "p.sock")
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_ = maintenance.Save(dir, st)
	svc := &handlers.Services{Store: db, Maintenance: maintenance.NewWatch(dir)}
	r := chi.NewRouter()
	handlers.RegisterSubjectRoutes(r, svc, e2eResponder{})
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: r}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// e2eResponder writes pigtail's real error envelope, so the real client
// decodes an *APIError (testResponder, in package handlers, does not).
type e2eResponder struct{}

func (e2eResponder) JSON(w http.ResponseWriter, _ *http.Request, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}
func (e2eResponder) Error(w http.ResponseWriter, _ *http.Request, status int, code pigtail.ErrorCode, msg string, details map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": msg, "details": details}})
}

func TestExemptEndToEnd(t *testing.T) {
	sock := e2eDaemon(t, maintenance.State{ID: "mnt_e", On: true, Freeze: true, Reason: "e2e", Exempt: "sid-lead", Since: time.Now()})
	t.Setenv("PIGTAIL_SOCKET", sock)
	write := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := newClient().WithScope("perso").AddJournal(ctx, pigtail.JournalEntry{Kind: "note"})
		return err
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-other")
	var api *pigtail.APIError
	if err := write(); !errors.As(err, &api) || api.Code != pigtail.ErrMaintenanceFrozen || !strings.Contains(api.Message, "e2e") {
		t.Fatalf("non-exempt session: want MAINTENANCE_FROZEN, got %v", err)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-lead")
	if err := write(); errors.As(err, &api) && api.Code == pigtail.ErrMaintenanceFrozen {
		t.Fatalf("exempt session frozen: %v", err)
	}
}

func TestOwnerEndToEnd(t *testing.T) {
	sock := e2eDaemon(t, maintenance.State{ID: "mnt_o", On: true, Freeze: true, Reason: "e2e", Owner: "sid-owner", OwnerLabel: "Lead", Since: time.Now()})
	t.Setenv("PIGTAIL_SOCKET", sock)
	write := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := newClient().WithScope("perso").AddJournal(ctx, pigtail.JournalEntry{Kind: "note"})
		return err
	}
	var api *pigtail.APIError
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-other")
	if err := write(); !errors.As(err, &api) || api.Code != pigtail.ErrMaintenanceFrozen || !strings.Contains(api.Message, "set by session Lead") {
		t.Fatalf("other session: want MAINTENANCE_FROZEN naming the owner, got %v", err)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-owner")
	if err := write(); errors.As(err, &api) && api.Code == pigtail.ErrMaintenanceFrozen {
		t.Fatalf("owner frozen: %v", err)
	}
}

// cmd/pigtail/maintenance_e2e_test.go — the « Ada » half (no CLAUDE_CODE_SESSION_ID) is proven on the
// built binary with an explicit environment: no agent ever plays Ada with `env -u` in its own shell.
func TestMaintenanceAdaEndToEnd(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(t.TempDir(), "pigtail")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil { // cwd = cmd/pigtail
		t.Fatalf("build: %v %s", err, out)
	}
	dir := t.TempDir()
	// The two-scope table the D23 check below reads: outside any cell, the
	// caller is the default scope, not the master (the neutral builtin's one
	// scope is both, and would name the full id).
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "pigtail"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "pigtail", "scopes.yaml"), []byte(scopetest.TwoYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(sid string, args ...string) (string, int) {
		c := exec.Command(bin, append([]string{"maintenance"}, args...)...)
		c.Env = []string{"HOME=" + t.TempDir(), "XDG_CONFIG_HOME=" + xdg, "PATH=/usr/bin:/bin", "PIGTAIL_MAINTENANCE_DIR=" + dir,
			"PIGTAIL_SOCKET=" + filepath.Join(dir, "absent.sock")} // explicit: no CLAUDE_CODE_SESSION_ID unless given
		if sid != "" {
			c.Env = append(c.Env, "CLAUDE_CODE_SESSION_ID="+sid)
		}
		out, err := c.CombinedOutput()
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) { // the binary did not run at all
			t.Fatalf("run %v: %v %s", args, err, out)
		}
		return string(out), c.ProcessState.ExitCode()
	}
	// (a) Ada sets: owner empty; a session cannot lift; Ada lifts.
	if out, rc := run("", "on", "--reason", "e2e"); rc != 0 || !strings.Contains(out, "owner  the user (outside Claude Code)") {
		t.Fatalf("ada on: %d %s", rc, out)
	}
	if out, rc := run(sidA, "off"); rc != 1 || !strings.Contains(out, "set by the user — only they lift it") {
		t.Fatalf("session off of an Ada maintenance: %d %s", rc, out)
	}
	if out, rc := run("", "off"); rc != 0 {
		t.Fatalf("ada off: %d %s", rc, out)
	}
	// (b) a session sets (owner = sidA); another session is refused; Ada lifts it (dead owner).
	_, _ = run(sidA, "on", "--reason", "e2e", "--freeze")
	// daemon unreachable: the label is the id, with no scope — read outside any
	// cell (the default scope), only its short id is named (D23)
	if out, rc := run(sidB, "off"); rc != 1 || !strings.Contains(out, "set by session "+sidA[:8]+" — only that session") {
		t.Fatalf("other session off: %d %s", rc, out)
	}
	if out, rc := run("", "off"); rc != 0 {
		t.Fatalf("ada off of a session maintenance: %d %s", rc, out)
	}
	if st, _, _ := maintenance.Load(dir); st.On || !st.LiftedByUser {
		t.Fatalf("%+v", st)
	}
	// (c) unreadable state: a session is refused (S22), Ada repairs.
	_, _ = run(sidA, "on", "--reason", "e2e")
	_ = os.WriteFile(filepath.Join(dir, maintenance.FileName), []byte("{"), 0o600)
	if out, rc := run(sidA, "off"); rc != 1 || !strings.Contains(out, "only the user, outside Claude Code, repairs it; do not run `off`") {
		t.Fatalf("session repair: %d %s", rc, out)
	}
	if out, rc := run("", "off"); rc != 0 || !strings.Contains(out, "deleting it") {
		t.Fatalf("ada repair: %d %s", rc, out)
	}
}
