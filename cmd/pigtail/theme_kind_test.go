package main

import (
	"strings"
	"testing"
)

// TestThemeCreateKindOngoing: --kind advertises project|ongoing, and the
// historical --kind area is still accepted, stored as ongoing (D1, A7).
func TestThemeCreateKindOngoing(t *testing.T) {
	cmd := themeCreateCmd()
	if !strings.Contains(cmd.Use, "project|ongoing") {
		t.Fatalf("Use = %q, want project|ongoing", cmd.Use)
	}
	if f := cmd.Flags().Lookup("kind"); f == nil || !strings.Contains(f.Usage, "project|ongoing") {
		t.Fatalf("--kind usage = %+v, want project|ongoing", f)
	}

	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	if err := runCLI(t, s, themeCmd, "create", "--name", "O", "--kind", "area", "--vault-link", "Perso/Areas/o"); err != nil {
		t.Fatalf("--kind area must stay accepted: %v", err)
	}
	if err := runCLI(t, s, themeCmd, "create", "--name", "O2", "--kind", "ongoing", "--vault-link", "Perso/Areas/o2"); err != nil {
		t.Fatalf("--kind ongoing: %v", err)
	}
	if err := runCLI(t, s, themeCmd, "create", "--name", "O3", "--kind", "epic", "--vault-link", "Perso/Areas/o3"); err == nil {
		t.Fatal("--kind epic must be refused")
	}
	ths, err := st.ListThemes("perso")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, th := range ths {
		if th.Name == "O" || th.Name == "O2" {
			n++
			if th.Kind != "ongoing" {
				t.Errorf("theme %s kind = %q, want ongoing", th.Name, th.Kind)
			}
		}
	}
	if n != 2 {
		t.Fatalf("found %d created themes, want 2", n)
	}
}
