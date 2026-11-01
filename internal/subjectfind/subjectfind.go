// Package subjectfind ranks subjects against a few words (sub_x000000000000031):
// the words of the title and short title, their prefixes and two-letter
// acronyms, the theme's name as a bonus, every status (closed ones a little
// lower), the last owner log line as the tie-break. It is pure: the daemon
// reads, this package judges. It reuses suggest.Tokens and never changes it.
package subjectfind

import (
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/suggest"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Subject is one subject of the corpus, every scope and status included.
type Subject struct {
	ID, Title, ShortTitle, Theme, Scope, Status string
	LastActivity                                int64 // epoch ms of the last owner log line, 0 = none
}

// Candidate is one ranked subject.
type Candidate struct {
	Subject
	Score   float64  // the ranking score, closed factor included
	Matched []string // the query terms it matched, in query order
}

const (
	// PrefixWeight scales a prefix match (notif → notifications, sujet ↔ sujets).
	PrefixWeight = 0.8
	// ThemeWeight scales a term found only in the subject's theme name.
	ThemeWeight = 0.5
	// ClosedFactor puts a closed subject under an open one of comparable relevance.
	ClosedFactor = 0.85

	minForward   = 3 // a query term this long matches the terms it starts
	minReverse   = 4 // a subject term this long matches the query terms it starts
	scoreEpsilon = 1e-9
)

// stop2 are the two-letter words that are no acronym (French, English).
var stop2 = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`de la le un et en au ce se ne on il je tu du sa ma ta me te ni si va
		at to of in is it an as be by do go if my no or so up us we he am`) {
		stop2[w] = true
	}
}

// Terms returns the searchable words of text: suggest.Tokens, then the
// two-letter ASCII words that are not stop words (DS, PR, UI), lower case.
func Terms(text string) []string {
	out := suggest.Tokens(text)
	seen := make(map[string]bool, len(out))
	for _, t := range out {
		seen[t] = true
	}
	for _, f := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(f) == 2 && isASCIILower(f) && !stop2[f] && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

func isASCIILower(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 'a' || s[i] > 'z' {
			return false
		}
	}
	return true
}

type match int

const (
	noMatch match = iota
	looseMatch
	exactMatch
)

// matchOf is how well query term q matches a set of terms.
func matchOf(q string, terms []string) match {
	best := noMatch
	for _, t := range terms {
		if t == q {
			return exactMatch
		}
		if best == noMatch && isLoose(q, t) {
			best = looseMatch
		}
	}
	return best
}

// isLoose: a prefix in either direction; a two-letter acronym never matches loosely.
func isLoose(q, t string) bool {
	nq, nt := utf8.RuneCountInString(q), utf8.RuneCountInString(t)
	if nq < 3 || nt < 3 {
		return false
	}
	return (nq >= minForward && strings.HasPrefix(t, q)) || (nt >= minReverse && strings.HasPrefix(q, t))
}

// idf is the smoothed inverse document frequency: ln((N+1)/(df+1)).
func idf(n, df int) float64 { return math.Log(float64(n+1) / float64(df+1)) }

// Rank returns every subject matching at least one term of query, sorted by
// score, then last activity (most recent first, none last), then id. It never
// truncates. The weights are computed over every subject passed in.
func Rank(query string, subjects []Subject) []Candidate {
	qt := Terms(query)
	if len(qt) == 0 || len(subjects) == 0 {
		return nil
	}
	n := len(subjects)
	docs := make([][]string, n)
	themes := make([][]string, n)
	for i, s := range subjects {
		docs[i] = Terms(s.Title + " " + s.ShortTitle)
		themes[i] = Terms(s.Theme)
	}
	type weight struct{ exact, loose, theme float64 }
	ws := make([]weight, len(qt))
	for k, q := range qt {
		var dfE, dfL, dfT int
		for i := range subjects {
			switch matchOf(q, docs[i]) {
			case exactMatch:
				dfE++
				dfL++
			case looseMatch:
				dfL++
			}
			if matchOf(q, themes[i]) != noMatch {
				dfT++
			}
		}
		ws[k] = weight{idf(n, dfE), PrefixWeight * idf(n, dfL), ThemeWeight * idf(n, dfT)}
	}
	var out []Candidate
	for i, s := range subjects {
		var raw float64
		matched := []string{}
		for k, q := range qt {
			switch matchOf(q, docs[i]) {
			case exactMatch:
				raw += ws[k].exact
			case looseMatch:
				raw += ws[k].loose
			default:
				if matchOf(q, themes[i]) == noMatch {
					continue
				}
				raw += ws[k].theme
			}
			matched = append(matched, q)
		}
		if len(matched) == 0 {
			continue
		}
		score := raw * float64(len(matched)) / float64(len(qt))
		if pigtail.IsClosedStatus(s.Status) {
			score *= ClosedFactor
		}
		out = append(out, Candidate{Subject: s, Score: score, Matched: matched})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if d := a.Score - b.Score; math.Abs(d) > scoreEpsilon {
			return d > 0
		}
		if a.LastActivity != b.LastActivity {
			return a.LastActivity > b.LastActivity
		}
		return a.ID < b.ID
	})
	return out
}
