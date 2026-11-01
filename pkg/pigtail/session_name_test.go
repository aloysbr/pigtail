package pigtail

import "testing"

func TestSessionDisplayName(t *testing.T) {
	cases := []struct {
		name string
		in   DisplayNameInput
		want string
	}{
		{"non lié : le titre cmux", DisplayNameInput{Shown: "nas"}, "nas"},
		{"non lié, titre court ignoré", DisplayNameInput{Shown: "nas", ShortTitle: "X"}, "nas"},
		{"lié : le titre court, quoi qu'affiche cmux", DisplayNameInput{Shown: "fait", ShortTitle: "Relance cmux sûre", Bound: true}, "Relance cmux sûre"},
		{"lié, espaces réduits", DisplayNameInput{Shown: "x", ShortTitle: " Relance  cmux sûre ", Bound: true}, "Relance cmux sûre"},
		{"lié à un sujet illisible : le titre affiché", DisplayNameInput{Shown: "fait", Bound: true}, "fait"},
		{"rien", DisplayNameInput{}, ""},
	}
	for _, c := range cases {
		if got := SessionDisplayName(c.in); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCleanCmuxTitle(t *testing.T) {
	for in, want := range map[string]string{"✳ claude": "claude", "◑ Relance": "Relance", "nas": "nas", "": ""} {
		if got := CleanCmuxTitle(in); got != want {
			t.Errorf("CleanCmuxTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShownTitle(t *testing.T) {
	cases := []struct {
		title, custom string
		has           bool
		want          string
	}{
		{"✳ claude", "", false, "claude"},                      // auto title: glyph removed
		{"#201 relance", "#201 relance", true, "#201 relance"}, // manual, starts with a symbol: kept whole
		{"✳ 🔥 urgent", "🔥 urgent", true, "🔥 urgent"},           // manual, whatever cmux prefixes to Title
		{"✳ claude", "  ", true, "claude"},                     // custom flag but blank: falls back
		{"", "", false, ""},
	}
	for _, c := range cases {
		if got := ShownTitle(c.title, c.custom, c.has); got != c.want {
			t.Errorf("ShownTitle(%q, %q, %v) = %q, want %q", c.title, c.custom, c.has, got, c.want)
		}
	}
}
