package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
)

const (
	labRoot = "0ae10000-0000-4000-8000-000000000001"
	labAcme = "acac0000-0000-4000-8000-000000000002"
)

var threeCells = map[string]string{"s": "globex", "m": "perso", "a": "acme"}

// D22: the hook names the owner (and who lifted) as the reader's cell may
// see that session; outside any cell the reader is the default scope.
func TestMaintenanceHookOriginFollowsTheReaderCell(t *testing.T) {
	scopetest.Three(t)
	for _, c := range []struct {
		cell, want string
		leak       bool
	}{{"s", "set by session acac0000", false}, {"m", "set by session deploy acme-ops", true}, {"", "set by session acac0000", false}} {
		dirs := withCellScopes(t, threeCells, c.cell)
		dir := t.TempDir()
		cfg := dirs[c.cell]
		if c.cell == "" {
			cfg = t.TempDir()
		}
		getenv := func(k string) string {
			return map[string]string{"PIGTAIL_MAINTENANCE_DIR": dir, "CLAUDE_CONFIG_DIR": cfg}[k]
		}
		_ = maintenance.Save(dir, maintenance.State{ID: "mnt_a", On: true, Reason: "migration", Since: time.Now(),
			Owner: labAcme, OwnerLabel: "deploy acme-ops", OwnerScope: "acme"})
		var out, errw bytes.Buffer
		runMaintenanceHook(strings.NewReader(`{"session_id":"s1","hook_event_name":"PostToolUse"}`), &out, &errw, getenv, time.Now)
		if !strings.Contains(out.String(), c.want) || strings.Contains(out.String(), "acme") != c.leak {
			t.Fatalf("cell %q: want %q, acme shown=%v:\n%s", c.cell, c.want, c.leak, out.String())
		}
		if c.cell != "s" {
			continue
		}
		_ = maintenance.Lift(dir, "mnt_a", time.Now(), labAcme, "deploy acme-ops", "acme")
		out.Reset()
		runMaintenanceHook(strings.NewReader(`{"session_id":"s1","hook_event_name":"PostToolUse"}`), &out, &errw, getenv, time.Now)
		if !strings.Contains(out.String(), "lifted by session acac0000") || strings.Contains(out.String(), "acme") {
			t.Fatalf("lift, cell s:\n%s", out.String())
		}
	}
}

// D22: status names a session only to a caller who sees it; --json hides the
// label and the scope name alike.
func TestMaintenanceStatusLabelsFollowTheCaller(t *testing.T) {
	scopetest.Three(t)
	d, out, _, _ := deps(t, nil)
	_ = maintenance.Save(d.dir, maintenance.State{ID: "mnt_a", On: true, Reason: "migration", Freeze: true, Since: mt0,
		Owner: labRoot, OwnerLabel: "chantier racine", OwnerScope: "perso",
		Exempt: labAcme, ExemptLabel: "review acme-ops", ExemptScope: "acme"})
	d.reader = func() (scope.Scope, error) { return "globex", nil }
	if err := runMaintenanceStatus(d, false); err != nil {
		t.Fatal(err)
	}
	o := out.String()
	if !strings.Contains(o, "  owner  0ae10000") || !strings.Contains(o, "  exempt acac0000") || strings.Contains(o, "acme") || strings.Contains(o, "chantier racine") {
		t.Fatalf("globex status:\n%s", o)
	}
	out.Reset()
	if err := runMaintenanceStatus(d, true); err != nil {
		t.Fatal(err)
	}
	if o := out.String(); strings.Contains(o, "acme") || strings.Contains(o, "chantier racine") || strings.Contains(o, "perso") {
		t.Fatalf("globex status --json:\n%s", o)
	}
	d.reader = func() (scope.Scope, error) { return "perso", nil }
	out.Reset()
	if err := runMaintenanceStatus(d, false); err != nil {
		t.Fatal(err)
	}
	if o := out.String(); !strings.Contains(o, "chantier racine ["+labRoot+"]") || !strings.Contains(o, "review acme-ops ["+labAcme+"]") {
		t.Fatalf("master status:\n%s", o)
	}
	out.Reset()
	if err := runMaintenanceStatus(d, true); err != nil {
		t.Fatal(err)
	}
	var v struct {
		State maintenance.State `json:"state"`
	}
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || v.State.ExemptLabel != "review acme-ops" || v.State.ExemptScope != "acme" || v.State.OwnerLabel != "chantier racine" {
		t.Fatalf("master status --json: %+v %v", v.State, err)
	}
}

// D22: on records the scope of each label it read.
func TestMaintenanceOnRecordsLabelScopes(t *testing.T) {
	scopetest.Three(t)
	d, _, _, _ := deps(t, nil)
	d.session = labRoot
	d.resolve = func(a string) (string, string, string, error) {
		switch a {
		case labRoot:
			return labRoot, "chantier racine", "perso", nil
		case "acme-ops":
			return labAcme, "review acme-ops", "acme", nil
		}
		return "", "", "", nil
	}
	d.reader = func() (scope.Scope, error) { return "perso", nil }
	if err := runMaintenanceOn(d, "migration", "", true, "acme-ops"); err != nil {
		t.Fatal(err)
	}
	st, ok, err := maintenance.Load(d.dir)
	if err != nil || !ok || st.ExemptScope != "acme" || st.OwnerScope != "perso" {
		t.Fatalf("%+v %v %v", st, ok, err)
	}
}

// D23: « already on » and the lift refusal name the owner per caller.
func TestMaintenanceRefusalsNameOwnerPerCaller(t *testing.T) {
	scopetest.Three(t)
	for _, c := range []struct {
		reader scope.Scope
		title  bool
	}{{"globex", false}, {"perso", true}} {
		d, _, _, _ := deps(t, nil)
		_ = maintenance.Save(d.dir, maintenance.State{ID: "mnt_a", On: true, Reason: "migration", Freeze: true, Since: mt0,
			Owner: labRoot, OwnerLabel: "chantier racine", OwnerScope: "perso"})
		d.reader = func() (scope.Scope, error) { return c.reader, nil }
		errOn := runMaintenanceOn(asSession(d, sidB), "other", "", false, "")
		errOff := runMaintenanceOff(asSession(d, sidB))
		if errOn == nil || errOff == nil {
			t.Fatalf("%s: on=%v off=%v", c.reader, errOn, errOff)
		}
		if c.title {
			if !strings.Contains(errOn.Error(), "set by session chantier racine") || !strings.Contains(errOff.Error(), "was set by session chantier racine ["+labRoot+"]") {
				t.Fatalf("master: on=%v off=%v", errOn, errOff)
			}
			continue
		}
		if !strings.Contains(errOn.Error(), "already on") || !strings.Contains(errOn.Error(), "set by session 0ae10000") ||
			!strings.Contains(errOff.Error(), "was set by session 0ae10000 — only that session") ||
			strings.Contains(errOn.Error()+errOff.Error(), "chantier racine") {
			t.Fatalf("globex: on=%v off=%v", errOn, errOff)
		}
	}
}
