package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func runRole(t *testing.T, s *stubDaemon, args ...string) (string, error) {
	t.Helper()
	testutil.SerialOnly(t) // swaps os.Stdout
	var out string
	var err error
	withStubDaemon(t, s, func() {
		stdout := os.Stdout
		rd, wr, perr := os.Pipe()
		if perr != nil {
			t.Fatal(perr)
		}
		os.Stdout = wr
		cmd := roleCmd()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		err = cmd.Execute()
		_ = wr.Close()
		os.Stdout = stdout
		b, _ := io.ReadAll(rd)
		out = string(b)
	})
	return out, err
}

func roleDaemon(t *testing.T, snap func() ([]pigtail.RoleStatus, bool)) *stubDaemon {
	t.Helper()
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemonWith(t, func(r chi.Router, st *store.Store) {
		handlers.RegisterRoleRoutes(r, &handlers.Services{Store: st, RoleSnapshot: snap}, envelopeResponder{})
	})
	return s
}

func TestRoleAddFlags(t *testing.T) {
	s := roleDaemon(t, nil)
	if _, err := runRole(t, s, "add", "pilote", "--subject", "sub_p"); err == nil {
		t.Fatal("add without --cadence must fail")
	}
	out, err := runRole(t, s, "add", "pilote", "--subject", "sub_p", "--cadence", "30m", "--stall", "45m")
	if err != nil || !strings.Contains(out, "role pilote") || !strings.Contains(out, "every 30m0s") || !strings.Contains(out, "stall 45m0s") || !strings.Contains(out, "grace 10m0s") {
		t.Fatalf("add: %q %v", out, err)
	}
	if out, err := runRole(t, s, "ls"); err != nil || !strings.Contains(out, "pilote") || !strings.Contains(out, "sub_p") {
		t.Fatalf("ls: %q %v", out, err)
	}
	if _, err := runRole(t, s, "add", "x", "--subject", "sub_nope", "--cadence", "5m"); err == nil {
		t.Fatal("unknown subject must fail")
	}
	if out, err := runRole(t, s, "rm", "pilote"); err != nil || !strings.Contains(out, "removed") {
		t.Fatalf("rm: %q %v", out, err)
	}
	if out, err := runRole(t, s, "ls"); err != nil || !strings.Contains(out, "no role declared") {
		t.Fatalf("ls after rm: %q %v", out, err)
	}
}

func TestRoleStatusJSON(t *testing.T) {
	dl := time.Now().Add(-41 * time.Minute)
	s := roleDaemon(t, func() ([]pigtail.RoleStatus, bool) {
		return []pigtail.RoleStatus{{Role: pigtail.Role{Scope: "perso", Name: "pilote"}, State: "silent",
			Reason: "wakeup due 11:31 never fired", Deadline: &dl}}, true
	})
	if _, err := runRole(t, s, "add", "pilote", "--subject", "sub_p", "--cadence", "30m"); err != nil {
		t.Fatal(err)
	}
	out, err := runRole(t, s, "status", "pilote", "--json")
	var st pigtail.RolesStatusResponse
	if err != nil || json.Unmarshal([]byte(out), &st) != nil || !st.Watch || len(st.Roles) != 1 ||
		st.Roles[0].State != "silent" || st.Roles[0].Reason != "wakeup due 11:31 never fired" {
		t.Fatalf("status --json: %q %v", out, err)
	}
	out, err = runRole(t, s, "status")
	if err != nil || !strings.Contains(out, "pilote") || !strings.Contains(out, "silent") || !strings.Contains(out, "+ 10m0s (41m") ||
		!strings.Contains(out, "— wakeup due 11:31 never fired") {
		t.Fatalf("status text: %q %v", out, err)
	}
	if _, err := runRole(t, s, "status", "nope"); err == nil {
		t.Fatal("status of an unknown role must fail")
	}
}

func TestRoleStatusWatchOffSaysSo(t *testing.T) {
	s := roleDaemon(t, nil)
	if _, err := runRole(t, s, "add", "pilote", "--subject", "sub_p", "--cadence", "30m"); err != nil {
		t.Fatal(err)
	}
	out, err := runRole(t, s, "status")
	if err != nil || !strings.Contains(out, "watch: false") {
		t.Fatalf("status: %q %v", out, err)
	}
}
