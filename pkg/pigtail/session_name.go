package pigtail

import (
	"strings"
	"unicode"
)

// DisplayNameInput is what the one name of a session is made of
// (sub_x000000000000018, D3/D5).
type DisplayNameInput struct {
	Shown      string // the title the cmux sidebar shows, status glyph removed
	ShortTitle string // ShortTitle of the subject; "" when unknown or unreadable
	Bound      bool   // the cmux workspace is bound to that subject
}

// SessionDisplayName is the one name of a session (or a cmux workspace): the
// subject's short title when the workspace is bound — pigtail keeps that
// title on it (D5) —, else the title the sidebar shows. Pure.
func SessionDisplayName(in DisplayNameInput) string {
	if st := strings.Join(strings.Fields(in.ShortTitle), " "); in.Bound && st != "" {
		return st
	}
	return strings.Join(strings.Fields(in.Shown), " ")
}

// CleanCmuxTitle drops the status glyph cmux prefixes a workspace title with
// ("✳ ", "◑ ") so it reads and matches as text.
func CleanCmuxTitle(t string) string {
	return strings.TrimLeftFunc(t, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// ShownTitle is the one rule for the title a cmux sidebar shows: the name
// given by hand as is ("#201 relance" keeps its "#"), else cmux's own title
// without its status glyph.
func ShownTitle(title, customTitle string, hasCustom bool) string {
	if c := strings.TrimSpace(customTitle); hasCustom && c != "" {
		return c
	}
	return CleanCmuxTitle(strings.TrimSpace(title))
}
