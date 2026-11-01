package waittype

import "testing"

func TestWorkingLine(t *testing.T) {
	for _, tc := range []struct {
		name, msg, reason string
		ok                bool
	}{
		{"incident", "Détail.\n\n---\n**TL;DR**\n\nOn y va.\n\n🔄 En cours — la création des deux worktrees tourne en arrière-plan, je reviens seul pour lancer la rédaction du plan. Rien à faire de ton côté.",
			"la création des deux worktrees tourne en…", true},
		{"short reason, cut at the sentence", "🔄 En cours — CI de la PR #12. Rien à faire de ton côté.", "CI de la PR #12", true},
		{"indented, en dash", "  🔄 En cours – tests en arrière-plan.", "tests en arrière-plan", true},
		{"hyphen", "🔄 En cours - un agent relit le plan.", "un agent relit le plan", true},
		{"a dot inside a word is no sentence end", "🔄 En cours — vet de main.go et v1.2 en cours", "vet de main.go et v1.2 en cours", true},
		{"no dash: no reason", "🔄 En cours.", "", true},
		{"the last 🔄 line wins", "🔄 En cours — un.\n🔄 En cours — deux.", "deux", true},
		{"bold markers dropped", "🔄 **En cours** — **build** en cours.", "build en cours", true},
		{"nothing to do alone", "Rien à faire de ton côté.", "", false},
		{"🔄 mid-line", "voir 🔄 plus haut — non.", "", false},
		{"empty", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, ok := WorkingLine(tc.msg)
			if ok != tc.ok || r != tc.reason {
				t.Fatalf("WorkingLine = %q, %v; want %q, %v", r, ok, tc.reason, tc.ok)
			}
			if len([]rune(r)) > workingReasonRunes+1 {
				t.Fatalf("reason %q longer than %d runes", r, workingReasonRunes)
			}
		})
	}
}
