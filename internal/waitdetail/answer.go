package waitdetail

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Typed answers (sub_x000000000000026, Task 8, spec § 4). ParseAnswer checks
// that an answer takes one form the wait's type accepts, and that no free
// text carries a secret; RenderAnswer writes the French body the session
// reads. Pure: the answer's time is set by the caller.

// Answer forms, as the request names them.
const (
	formChoice = "choice"
	formText   = "text"
	formItems  = "items"
	formSeen   = "seen"
)

// ParseAnswer validates req against w and returns the answer to keep. The
// error names the field at fault (text, items[<n>].note, reason).
func ParseAnswer(w pigtail.Wait, req pigtail.WaitAnswerRequest) (pigtail.WaitAnswer, error) {
	var a pigtail.WaitAnswer
	typ := waittype.Type(w.Type)
	switch typ {
	case waittype.Done, waittype.Wrapped:
		msg := fmt.Sprintf("a %s wait takes no answer: the session is over — close it", typ)
		if cmd := waittype.DoneHint(); cmd != "" {
			msg += " with " + cmd // the user's done command (settings.yaml session.done_command)
		}
		return a, errors.New(msg)
	case waittype.Working:
		return a, errors.New("a working wait takes no answer: it is the agent's turn, not yours")
	case waittype.Permission:
		return a, errors.New("answer it in the terminal")
	}
	if !waittype.Answerable(typ) {
		return a, fmt.Errorf("a %s wait takes no answer", typ)
	}
	a.Normalize(string(typ))
	if err := checkAnswerSecrets(req); err != nil {
		return a, err
	}
	form, err := oneForm(req)
	if err != nil {
		return a, err
	}
	accepted := acceptedForms(typ, w.Details)
	if !contains(accepted, form) {
		return a, fmt.Errorf("a %s wait takes no %s answer: use %s", typ, form, joinForms(accepted))
	}
	a.Reason = strings.TrimSpace(req.Reason)
	switch form {
	case formChoice:
		id, err := resolveChoice(w.Details, strings.TrimSpace(req.Choice))
		if err != nil {
			return a, err
		}
		a.Choice = id
	case formText:
		a.Text = strings.TrimSpace(req.Text)
	case formItems:
		items, err := parseItems(len(w.Details.Items), req.Items)
		if err != nil {
			return a, err
		}
		a.Items = items
	case formSeen:
		a.Seen = true
	}
	return a, nil
}

// acceptedForms lists the forms a wait of type typ with content d takes.
func acceptedForms(typ waittype.Type, d *pigtail.WaitDetails) []string {
	switch typ {
	case waittype.Arbitrage:
		return []string{formChoice, formText}
	case waittype.Question:
		if d != nil && len(d.Options) > 0 {
			return []string{formChoice, formText}
		}
		return []string{formText}
	case waittype.Review:
		if d != nil && len(d.Items) > 0 {
			return []string{formItems}
		}
		return []string{formSeen, formText}
	case waittype.Blocked:
		return []string{formSeen, formText}
	}
	return nil
}

func oneForm(req pigtail.WaitAnswerRequest) (string, error) {
	var forms []string
	if strings.TrimSpace(req.Choice) != "" {
		forms = append(forms, formChoice)
	}
	if strings.TrimSpace(req.Text) != "" {
		forms = append(forms, formText)
	}
	if len(req.Items) > 0 {
		forms = append(forms, formItems)
	}
	if req.Seen {
		forms = append(forms, formSeen)
	}
	if len(forms) != 1 {
		return "", fmt.Errorf("an answer takes one form exactly — choice, text, items or seen (got %d)", len(forms))
	}
	return forms[0], nil
}

func checkAnswerSecrets(req pigtail.WaitAnswerRequest) error {
	if name := relay.SecretShape(req.Text); name != "" {
		return fmt.Errorf("text looks like it carries a secret (%s) — never send credentials", name)
	}
	if name := relay.SecretShape(req.Reason); name != "" {
		return fmt.Errorf("reason looks like it carries a secret (%s) — never send credentials", name)
	}
	for _, it := range req.Items {
		if name := relay.SecretShape(it); name != "" {
			n, _, _ := strings.Cut(it, "=")
			return fmt.Errorf("items[%s].note looks like it carries a secret (%s) — never send credentials", strings.TrimSpace(n), name)
		}
	}
	return nil
}

// resolveChoice returns the option id choice names; "reco" names the
// recommended option.
func resolveChoice(d *pigtail.WaitDetails, choice string) (string, error) {
	var opts []pigtail.WaitOption
	if d != nil {
		opts = d.Options
	}
	if choice == "reco" {
		for _, o := range opts {
			if o.Reco {
				return o.ID, nil
			}
		}
	}
	for _, o := range opts {
		if o.ID == choice {
			return o.ID, nil
		}
	}
	if len(opts) == 0 {
		return "", fmt.Errorf("choice %q: this wait has no options — answer with text", choice)
	}
	ids := make([]string, 0, len(opts))
	for _, o := range opts {
		ids = append(ids, o.ID)
	}
	return "", fmt.Errorf("choice %q is no option of this wait (options: %s)", choice, strings.Join(ids, ", "))
}

// parseItems reads "n=ok|ko[:note]" for each item 1..total, each once; a ko
// needs a note.
func parseItems(total int, raw []string) ([]pigtail.WaitItemVerdict, error) {
	seen := map[int]pigtail.WaitItemVerdict{}
	for _, s := range raw {
		left, right, ok := strings.Cut(s, "=")
		if !ok {
			return nil, fmt.Errorf("item %q: want n=ok|ko[:note]", s)
		}
		n, err := strconv.Atoi(strings.TrimSpace(left))
		if err != nil || n < 1 || n > total {
			return nil, fmt.Errorf("item %s: no such item (1..%d)", strings.TrimSpace(left), total)
		}
		if _, dup := seen[n]; dup {
			return nil, fmt.Errorf("item %d judged twice", n)
		}
		verdict, note, _ := strings.Cut(right, ":")
		v := pigtail.WaitItemVerdict{N: n, Note: strings.TrimSpace(note)}
		switch strings.TrimSpace(verdict) {
		case "ok":
			v.OK = true
		case "ko":
			if v.Note == "" {
				return nil, fmt.Errorf("item %d is ko: a ko needs a note (%d=ko:<note>)", n, n)
			}
		default:
			return nil, fmt.Errorf("item %d: verdict %q, want ok|ko", n, verdict)
		}
		seen[n] = v
	}
	out := make([]pigtail.WaitItemVerdict, 0, total)
	for n := 1; n <= total; n++ {
		v, ok := seen[n]
		if !ok {
			return nil, fmt.Errorf("item %d not judged: each item 1..%d takes one verdict", n, total)
		}
		out = append(out, v)
	}
	return out, nil
}

// RenderAnswer writes the body the session reads, at most
// relay.MaxBodyBytes: a longer one is clipped with a pointer to
// `pigtail wait show <id>`, where the whole answer stays.
func RenderAnswer(w pigtail.Wait, a pigtail.WaitAnswer, name string) string {
	if name == "" {
		name = "the user"
	}
	var b strings.Builder
	b.WriteString("Answer from " + name + " to your wait " + w.Type)
	if w.Reason != "" {
		b.WriteString(" « " + w.Reason + " »")
	}
	b.WriteString(" (" + w.ID + "): ")
	switch {
	case a.Choice != "":
		b.WriteString(a.Choice)
		if label := optionLabel(w.Details, a.Choice); label != "" {
			b.WriteString(" « " + label + " »")
		}
	case a.Text != "":
		b.WriteString(a.Text)
	case len(a.Items) > 0:
		b.WriteString(renderItems(a.Items))
	case a.Seen:
		b.WriteString("seen")
	}
	if a.Reason != "" {
		b.WriteString("\nreason=" + a.Reason)
	}
	return clipBody(b.String(), w.ID)
}

func optionLabel(d *pigtail.WaitDetails, id string) string {
	if d == nil {
		return ""
	}
	for _, o := range d.Options {
		if o.ID == id {
			return o.Label
		}
	}
	return ""
}

// renderItems: "n ok, m ko — 2 : <note> ; 3 : <note>", then the notes left on
// an ok item, marked "(ok)".
func renderItems(items []pigtail.WaitItemVerdict) string {
	ok, ko := 0, 0
	var notes []string
	for _, v := range items {
		if v.OK {
			ok++
		} else {
			ko++
		}
	}
	for _, v := range items {
		if !v.OK {
			notes = append(notes, fmt.Sprintf("%d: %s", v.N, v.Note))
		}
	}
	for _, v := range items {
		if v.OK && v.Note != "" {
			notes = append(notes, fmt.Sprintf("%d (ok): %s", v.N, v.Note))
		}
	}
	s := fmt.Sprintf("%d ok, %d ko", ok, ko)
	if len(notes) > 0 {
		s += " — " + strings.Join(notes, " ; ")
	}
	return s
}

// clipBody cuts s to relay.MaxBodyBytes on a rune boundary, ending with "…"
// and the pointer to the whole answer.
func clipBody(s, id string) string {
	if len(s) <= relay.MaxBodyBytes {
		return s
	}
	tail := "…\ndetail: pigtail wait show " + id
	cut := relay.MaxBodyBytes - len(tail)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + tail
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func joinForms(forms []string) string {
	return strings.Join(forms, " or ")
}
