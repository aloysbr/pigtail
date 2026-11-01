package cmuxcheck

import (
	"path/filepath"
	"testing"

	"github.com/aloysbr/pigtail/internal/claudesession"
)

func TestLoadPhoto(t *testing.T) {
	p, err := LoadPhoto(filepath.Join("testdata", "session-before.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Panels) != 5 {
		t.Fatalf("panels = %d, want 5", len(p.Panels))
	}
	a, ok := p.Panel("S-A")
	if !ok || a.Binding == nil || a.Binding.CheckpointID != "sid-a" || a.Binding.ConfigDir() != "/cfg/a" || a.WorkspaceTitle != "alpha" || a.Title != "✳ alpha" {
		t.Fatalf("S-A = %+v", a)
	}
	if d, _ := p.Panel("S-D"); d.Binding != nil {
		t.Fatalf("S-D has no claude binding, got %+v", d.Binding)
	}
	if c, _ := p.Panel("S-C"); c.Binding.AutoResume || c.Title != "gamma" {
		t.Fatalf("S-C = %+v (autoResume false, the panel's own title)", c)
	}
	if p.TakenAt.IsZero() {
		t.Fatal("TakenAt is the file mtime, never zero")
	}
}

func TestExpectedMode(t *testing.T) {
	p, err := LoadPhoto(filepath.Join("testdata", "session-before.json"))
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{
		"S-A": "auto",        // agent.permissionMode
		"S-B": "",            // none anywhere: D3 signals only
		"S-E": "acceptEdits", // only in the binding command, quoted (F4)
	} {
		pn, _ := p.Panel(id)
		if got := pn.ExpectedMode(); got != want {
			t.Errorf("%s: ExpectedMode = %q, want %q", id, got, want)
		}
	}
	// cmux quotes --dangerously-skip-permissions too: it is bypassPermissions (D8)
	skip := Panel{Binding: &Binding{Command: `/bin/sh -c '"claude" '\''--resume'\'' '\''x'\'' '\''--dangerously-skip-permissions'\'''`}}
	if got := skip.ExpectedMode(); got != claudesession.ModeBypass {
		t.Errorf("quoted skip flag: ExpectedMode = %q, want %q", got, claudesession.ModeBypass)
	}
}

func TestLoadPhotoErrors(t *testing.T) {
	if _, err := LoadPhoto(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing file must be an error, never an empty photo")
	}
}
