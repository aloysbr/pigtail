package pigtail

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestShortTitle(t *testing.T) {
	cases := []struct {
		name string
		sub  Subject
		want string
	}{
		{"explicit wins", Subject{Title: "Enrichir la sidebar cmux avec l'état pigtail", ShortTitle: "Sidebar pigtail"}, "Sidebar pigtail"},
		{"explicit trimmed", Subject{Title: "x", ShortTitle: "  Sidebar pigtail  "}, "Sidebar pigtail"},
		{"blank explicit falls back", Subject{Title: "Checklist style", ShortTitle: "   "}, "Checklist style"},
		{"short title kept whole", Subject{Title: "Checklist style"}, "Checklist style"},
		{"cut on a word boundary", Subject{Title: "Enrichir la sidebar cmux avec l'état pigtail (sujet, attente, serveurs)"}, "Enrichir la sidebar cmux…"},
		{"cut drops the partial word", Subject{Title: "Trier les workspaces cmux par attention puis rang pigtail"}, "Trier les workspaces cmux…"},
		{"trailing punctuation dropped", Subject{Title: "Refonte du module réunion, checklist et style"}, "Refonte du module réunion…"},
		{"accented runes counted as runes", Subject{Title: "Éléments à vérifier après génération complète"}, "Éléments à vérifier après…"},
		{"28 accented runes fit", Subject{Title: "éééééééééé ééééééééé ééééééé"}, "éééééééééé ééééééééé ééééééé"},
		{"space right after the limit keeps 28", Subject{Title: "aaaaaaaaaa bbbbbbbbbb cccccc dddd"}, "aaaaaaaaaa bbbbbbbbbb cccccc…"},
		{"no space: hard cut", Subject{Title: "Supercalifragilisticexpialidocious"}, "Supercalifragilisticexpialid…"},
		{"whitespace collapsed", Subject{Title: "Checklist\n  style"}, "Checklist style"},
		{"empty", Subject{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ShortTitle(c.sub)
			if got != c.want {
				t.Fatalf("ShortTitle = %q, want %q", got, c.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("ShortTitle produced invalid UTF-8: %q", got)
			}
			if c.sub.ShortTitle == "" && utf8.RuneCountInString(got) > ShortTitleDerivedMax+1 {
				t.Fatalf("derived title %q longer than %d runes + ellipsis", got, ShortTitleDerivedMax)
			}
		})
	}
}

func TestValidateShortTitle(t *testing.T) {
	ok := map[string]string{
		"":                                 "",
		"Sidebar pigtail":                  "Sidebar pigtail",
		"  Sidebar pigtail  ":              "Sidebar pigtail",
		"   ":                              "",
		strings.Repeat("é", ShortTitleMax): strings.Repeat("é", ShortTitleMax), // 32 runes, 64 bytes
	}
	for in, want := range ok {
		got, err := ValidateShortTitle(in)
		if err != nil || got != want {
			t.Errorf("ValidateShortTitle(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}
	for _, in := range []string{
		strings.Repeat("a", ShortTitleMax+1),
		"two\nlines",
		"tab\tinside",
		"cr\rinside",
	} {
		if _, err := ValidateShortTitle(in); err == nil {
			t.Errorf("ValidateShortTitle(%q): expected an error", in)
		}
	}
}

func TestSubjectShortTitleJSON(t *testing.T) {
	b, err := json.Marshal(Subject{ID: "sub_1", ShortTitle: "Sidebar pigtail"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["short_title"] != "Sidebar pigtail" {
		t.Fatalf("short_title not on the wire: %s", b)
	}
	b, _ = json.Marshal(Subject{ID: "sub_1"})
	m = nil
	_ = json.Unmarshal(b, &m)
	if _, present := m["short_title"]; present {
		t.Fatalf("unset short_title must be omitted (it is never the derived value): %s", b)
	}
}
