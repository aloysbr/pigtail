package pigtail

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// ShortTitleMax bounds an explicit Subject.ShortTitle, in runes.
	ShortTitleMax = 32
	// ShortTitleDerivedMax bounds the text kept from Title when no explicit
	// short title is set, in runes, before the "…" suffix.
	ShortTitleDerivedMax = 28
)

// ShortTitle returns the label to show for sub where Title does not fit:
// sub.ShortTitle when set, else Title cut on a word boundary to at most
// ShortTitleDerivedMax runes followed by "…". A Title that already fits is
// returned whole. Whitespace runs collapse to one space. Pure.
func ShortTitle(sub Subject) string {
	if st := strings.TrimSpace(sub.ShortTitle); st != "" {
		return st
	}
	title := strings.Join(strings.Fields(sub.Title), " ")
	runes := []rune(title)
	if len(runes) <= ShortTitleDerivedMax {
		return title
	}
	// A space right after the limit is a boundary too: look one rune further.
	cut := ShortTitleDerivedMax
	if i := lastSpace(runes[:ShortTitleDerivedMax+1]); i > 0 {
		cut = i
	}
	head := strings.TrimRightFunc(string(runes[:cut]), func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",;:-–—(", r)
	})
	if head == "" { // title made of punctuation: fall back to a hard cut
		head = string(runes[:ShortTitleDerivedMax])
	}
	return head + "…"
}

func lastSpace(runes []rune) int {
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] == ' ' {
			return i
		}
	}
	return -1
}

// ErrShortTitleInvalid is returned by ValidateShortTitle.
var ErrShortTitleInvalid = errors.New("invalid short_title")

// ValidateShortTitle normalises an explicit short title for storage: it is
// trimmed, must fit on one line (no control characters) and hold at most
// ShortTitleMax runes. "" (or blank) is valid and means "derive from Title".
func ValidateShortTitle(s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.ContainsFunc(s, unicode.IsControl) {
		return "", fmt.Errorf("%w: must be a single line without control characters", ErrShortTitleInvalid)
	}
	if n := utf8.RuneCountInString(s); n > ShortTitleMax {
		return "", fmt.Errorf("%w: %d characters, max %d", ErrShortTitleInvalid, n, ShortTitleMax)
	}
	return s, nil
}
