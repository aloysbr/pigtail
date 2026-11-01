package maintenance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/scope/scopetest"
)

const (
	rootOwner = "0ae10000-0000-4000-8000-000000000001"
	acmeSess  = "acac0000-0000-4000-8000-000000000002"
	tsReader  = "7a7a0000-0000-4000-8000-000000000003"
)

// rootExemptsAcme: the master's session set the maintenance and exempted a
// session of acme.
func rootExemptsAcme() State {
	st := State{ID: "mnt_0123456789abcdef", On: true, Reason: "migration", Freeze: true, Since: t0,
		Owner: rootOwner, OwnerLabel: "chantier racine", OwnerScope: "perso",
		Exempt: acmeSess, ExemptLabel: "review acme-ops", ExemptScope: "acme"}
	return st
}

// D22: a session label the state records is shown only to a reader who can
// see that session; any other reader gets its short id, never its title.
func TestRenderLabelsFollowTheReaderView(t *testing.T) {
	t.Run("master exempts acme, read from globex", func(t *testing.T) {
		scopetest.Three(t)
		s := Render(rootExemptsAcme(), t0, tsReader, "globex")
		for _, want := range []string{"Session aussi exemptée du gel : acac0000.", "Posée par la session 0ae10000"} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %q in:\n%s", want, s)
			}
		}
		if strings.Contains(s, "acme") || strings.Contains(s, "chantier racine") {
			t.Errorf("a label of a session globex cannot see leaked:\n%s", s)
		}
	})
	t.Run("acme owner, read from globex", func(t *testing.T) {
		scopetest.Three(t)
		st := State{ID: "mnt_0123456789abcdef", On: true, Reason: "migration", Freeze: true, Since: t0,
			Owner: acmeSess, OwnerLabel: "deploy acme-ops", OwnerScope: "acme",
			Exempt: tsReader, ExemptLabel: "ts lead", ExemptScope: "globex"}
		s := Render(st, t0, tsReader, "globex")
		for _, want := range []string{"Tu es aussi exemptée du gel (la session acac0000 mène le chantier).", "Posée par la session acac0000"} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %q in:\n%s", want, s)
			}
		}
		if strings.Contains(s, "acme") {
			t.Errorf("acme leaked to the exempt reader:\n%s", s)
		}
		s = Render(st, t0, "7a7a0000-0000-4000-8000-000000000009", "globex")
		if !strings.Contains(s, "Posée par la session acac0000") || strings.Contains(s, "acme") {
			t.Errorf("another globex reader:\n%s", s)
		}
	})
	t.Run("read from the master", func(t *testing.T) {
		scopetest.Three(t)
		s := Render(rootExemptsAcme(), t0, tsReader, "perso")
		if !strings.Contains(s, "review acme-ops") || !strings.Contains(s, "chantier racine") {
			t.Errorf("the master lost a label:\n%s", s)
		}
	})
	t.Run("read from a renamed master", func(t *testing.T) {
		scopetest.RenamedMaster(t)
		st := rootExemptsAcme()
		st.OwnerScope = "root"
		s := Render(st, t0, tsReader, "root")
		if !strings.Contains(s, "review acme-ops") || !strings.Contains(s, "chantier racine") {
			t.Errorf("the renamed master lost a label:\n%s", s)
		}
	})
	t.Run("positive control, read from acme", func(t *testing.T) {
		scopetest.Three(t)
		s := Render(rootExemptsAcme(), t0, tsReader, "acme")
		if !strings.Contains(s, "review acme-ops") || !strings.Contains(s, "0ae10000") || strings.Contains(s, "chantier racine") {
			t.Errorf("acme reader:\n%s", s)
		}
	})
	t.Run("older state without scopes", func(t *testing.T) {
		scopetest.Three(t)
		st := rootExemptsAcme()
		st.OwnerScope, st.ExemptScope = "", ""
		s := Render(st, t0, tsReader, "globex")
		if !strings.Contains(s, "0ae10000") || !strings.Contains(s, "acac0000") || strings.Contains(s, "chantier racine") || strings.Contains(s, "review acme-ops") {
			t.Errorf("non-master reader of an older state:\n%s", s)
		}
		s = Render(st, t0, tsReader, "perso")
		if !strings.Contains(s, "chantier racine") || !strings.Contains(s, "review acme-ops") {
			t.Errorf("master reader of an older state:\n%s", s)
		}
	})
}

// The lift notice names who lifted the same way.
func TestRenderLiftNamesLifterPerReader(t *testing.T) {
	scopetest.Three(t)
	dir := t.TempDir()
	_ = Save(dir, rootExemptsAcme())
	if err := Lift(dir, rootExemptsAcme().ID, t0, acmeSess, "deploy acme-ops", "acme"); err != nil {
		t.Fatal(err)
	}
	st, ok, err := Load(dir)
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if s := Render(st, t0, tsReader, "globex"); !strings.Contains(s, "par la session acac0000") || strings.Contains(s, "acme") {
		t.Errorf("globex reader:\n%s", s)
	}
	if s := Render(st, t0, tsReader, "perso"); !strings.Contains(s, "par la session deploy acme-ops") {
		t.Errorf("master reader:\n%s", s)
	}
	// The older form names a label with no id nor scope.
	_ = os.WriteFile(filepath.Join(dir, LiftedName), []byte(st.ID+" freeze session Alpha\n"), 0o600)
	st, _, _ = Load(dir)
	if s := Render(st, t0, tsReader, "globex"); !strings.Contains(s, "par une session") || strings.Contains(s, "Alpha") {
		t.Errorf("globex reader, older form:\n%s", s)
	}
	if s := Render(st, t0, tsReader, "perso"); !strings.Contains(s, "par la session Alpha") {
		t.Errorf("master reader, older form:\n%s", s)
	}
}

// owner_scope / exempt_scope round-trip; a state without them reads with
// empty scopes (omitempty both ways).
func TestSaveLoadKeepsLabelScopes(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, rootExemptsAcme()); err != nil {
		t.Fatal(err)
	}
	st, ok, err := Load(dir)
	if err != nil || !ok || st.OwnerScope != "perso" || st.ExemptScope != "acme" {
		t.Fatalf("%+v %v %v", st, ok, err)
	}
	_ = os.WriteFile(filepath.Join(dir, FileName), []byte(`{"id":"mnt_old","on":true,"reason":"r","owner":"x","owner_label":"X","since":"2026-10-04T10:00:00Z"}`), 0o600)
	st, ok, err = Load(dir)
	if err != nil || !ok || st.OwnerScope != "" || st.ExemptScope != "" || st.OwnerLabel != "X" {
		t.Fatalf("older state: %+v %v %v", st, ok, err)
	}
	if err := Save(dir, State{ID: "mnt_new", On: true, Since: t0}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, FileName))
	if strings.Contains(string(raw), "_scope") {
		t.Fatalf("empty scopes written: %s", raw)
	}
}

// D23: the refusals name the owner only to a reader who sees its session.
func TestRefusalsNameOwnerPerReader(t *testing.T) {
	t.Run("master owner", func(t *testing.T) {
		scopetest.Three(t)
		st := rootExemptsAcme()
		for _, m := range []string{FrozenMessage(st, "globex"), st.SetByEN("globex")} {
			if !strings.Contains(m, "set by session 0ae10000 —") || strings.Contains(m, "chantier racine") {
				t.Errorf("globex reader: %q", m)
			}
		}
		if m := FrozenMessage(st, "perso"); !strings.Contains(m, "set by session chantier racine —") {
			t.Errorf("master reader: %q", m)
		}
	})
	t.Run("renamed master", func(t *testing.T) {
		scopetest.RenamedMaster(t)
		st := rootExemptsAcme()
		st.OwnerScope = "root"
		if m := FrozenMessage(st, "root"); !strings.Contains(m, "set by session chantier racine —") {
			t.Errorf("root reader: %q", m)
		}
	})
	t.Run("positive control and older state", func(t *testing.T) {
		scopetest.Three(t)
		st := rootExemptsAcme()
		st.Owner, st.OwnerLabel, st.OwnerScope = acmeSess, "deploy acme-ops", "acme"
		if m := FrozenMessage(st, "acme"); !strings.Contains(m, "set by session deploy acme-ops") {
			t.Errorf("acme reader of an acme owner: %q", m)
		}
		old := rootExemptsAcme()
		old.OwnerScope = ""
		if m := FrozenMessage(old, "globex"); !strings.Contains(m, "0ae10000") || strings.Contains(m, "chantier racine") {
			t.Errorf("older state, globex reader: %q", m)
		}
	})
}
