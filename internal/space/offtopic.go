package space

import (
	"regexp"
	"strings"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// citedSubjectRE finds the subject ids cited in an off-topic reason; a sub_
// glued to a word (xsub_…) is not one.
var citedSubjectRE = regexp.MustCompile(`\bsub_[0-9a-z]{1,64}\b`)

// refusedSubjectRE finds a subject cited as refused (« délie : <id> refusé »,
// sub_x000000000000027 D9): the id immediately followed by " refusé". No final
// \b: é is not a word character for RE2.
var refusedSubjectRE = regexp.MustCompile(`\bsub_[0-9a-z]{1,64} refusé`)

// RefusedIDs returns the subject ids an off-topic reason cites as refused —
// the one definition of a refusal, read by OffTopicSpace and by subject
// suggest. A sub_… cited without " refusé" glued to it is not one.
func RefusedIDs(reason string) []string {
	var out []string
	for _, m := range refusedSubjectRE.FindAllString(reason, -1) {
		out = append(out, strings.TrimSuffix(m, " refusé"))
	}
	return out
}

// Lookup reads what OffTopicSpace needs from the store.
type Lookup struct {
	Subject  func(id string) (*pigtail.Subject, error)
	Theme    func(id string) (*pigtail.Theme, error)
	NotFound func(error) bool
	// KnownSpace tells whether name is a space the theme's scope may file in
	// (spaces.yaml); nil = every name is known. A subject's own space that is
	// not known any more is skipped: the subject follows its theme (D5).
	KnownSpace func(name, scope string) bool
}

// OffTopicSpace is the space of an off-topic declaration (D1, sub_x000000000000019):
// its explicit space, else the space of the first subject cited in its reason
// (sub_…) that the declaration's scope may read and whose theme has one; ""
// otherwise. A subject cited as refused (RefusedIDs) is skipped: « délie »
// never files the session in the folder of the subject it refused.
// A lookup error other than notFound is returned, never read as « none ».
func OffTopicSpace(o pigtail.WorkspaceOffTopic, lookup Lookup) (sp string, inferred bool, err error) {
	if o.Space != "" {
		return o.Space, false, nil
	}
	refused := map[string]bool{}
	for _, id := range RefusedIDs(o.Reason) {
		refused[id] = true
	}
	for _, id := range citedSubjectRE.FindAllString(o.Reason, -1) {
		if refused[id] {
			continue
		}
		sub, err := lookup.Subject(id)
		if err != nil {
			if lookup.NotFound(err) {
				continue
			}
			return "", false, err
		}
		if !scope.CanRead(scope.Scope(o.Scope), scope.Scope(sub.Scope)) || sub.ThemeID == "" {
			continue
		}
		th, err := lookup.Theme(sub.ThemeID)
		if err != nil {
			if lookup.NotFound(err) {
				continue
			}
			return "", false, err
		}
		if sub.Space != "" && (lookup.KnownSpace == nil || lookup.KnownSpace(sub.Space, th.Scope)) {
			return sub.Space, true, nil
		}
		if th.Space != "" {
			return th.Space, true, nil
		}
	}
	return "", false, nil
}
