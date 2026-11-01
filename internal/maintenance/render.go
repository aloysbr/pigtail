package maintenance

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/waittype"
)

// HumanName is how the notices name the human who runs pigtail outside Claude
// Code (settings.yaml human.name). The daemon and the CLI set it at start;
// empty reads as "the user".
var HumanName = "the user"

// FreezeAlsoRefuses lists the gestures the user's own guards refuse to agents
// while a maintenance freezes (settings.yaml maintenance.freeze_also_refuses):
// the notice names them in the freeze line and in what the lift reopens.
// pigtail refuses none of them itself. The daemon and the CLI set it at start;
// empty names none.
var FreezeAlsoRefuses []string

// freezeAlso is the freeze line's tail for FreezeAlsoRefuses.
func freezeAlso() string {
	var b strings.Builder
	for _, g := range FreezeAlsoRefuses {
		b.WriteString(" ; `" + g + "` est refusé aux agents")
	}
	return b.String()
}

// reopenAlso is the reopen list's tail for FreezeAlsoRefuses.
func reopenAlso() string {
	var b strings.Builder
	for _, g := range FreezeAlsoRefuses {
		b.WriteString(", `" + g + "`")
	}
	return b.String()
}

// FreezeSummary is what a freeze refuses, in the CLI's words: pigtail's own
// refusals, then FreezeAlsoRefuses as the agents'.
func FreezeSummary() string {
	var b strings.Builder
	b.WriteString("subject writes, pigtail new, pigtail dispatch, pigtail session spawn")
	for _, g := range FreezeAlsoRefuses {
		b.WriteString(", agents' " + g)
	}
	return b.String()
}

func human() string {
	if HumanName == "" {
		return "the user"
	}
	return HumanName
}

// Render is the notice a session reads (the single delivery path, D12).
// The owner (the session that ran `on`, D16) gets its own variant (D18): it
// is not asked to pause, and is told to lift at the end of its gesture.
//
// reader is the scope of the session reading it (D22): a session label is
// shown only to a reader who can see that session (SessionName).
func Render(s State, now time.Time, session string, reader scope.Scope) string {
	var b strings.Builder
	if !s.On {
		lifted := ""
		if s.LiftedAt != nil {
			lifted = " à " + s.LiftedAt.Local().Format("15:04")
		}
		by := ""
		if l := s.LiftedByFR(reader); l != "" {
			by = " " + l
		}
		// The state file is gone at lift time: only the id, the hour and who
		// lifted remain. « Tout est rouvert » only when the lifted maintenance
		// had a freeze, or when that is unknown (prudence, maintenance.lifted).
		reopen := ""
		if s.Freeze || !s.LiftedFreezeKnown {
			reopen = " Tout est rouvert : écritures de sujets, `pigtail new`, `pigtail dispatch`, `pigtail session spawn`" + reopenAlso() + "."
		}
		fmt.Fprintf(&b, "Message système pigtail — la maintenance du system (%s) est levée%s%s.%s Reprends normalement, et dis-le à %s dans ta prochaine réponse.\n", s.ID, lifted, by, reopen, human())
		return b.String()
	}
	owner := session != "" && session == s.Owner
	fmt.Fprintf(&b, "Message système pigtail — le system est en maintenance depuis %s. Raison : %s.\n", s.Since.Local().Format("02/01 15:04"), s.Reason)
	if s.Until != nil {
		fmt.Fprintf(&b, "Fin prévue à %s", s.Until.Local().Format("15:04"))
		if s.Late(now) {
			b.WriteString(" — EN RETARD : la maintenance continue jusqu'à sa levée")
		}
		b.WriteString(".\n")
	}
	if s.Freeze {
		b.WriteString("GEL : le démon refuse les écritures de sujets (sujets, phases, log, thèmes, journal), `pigtail new` et `pigtail dispatch` ; `pigtail session spawn` est refusé" + freezeAlso() + ". Ne relance pas un geste refusé, ne le contourne pas.\n")
	}
	switch {
	case s.Exempt == "" || owner:
	case s.Owner == "" && session == s.Exempt:
		b.WriteString("Tu es la session exemptée qui mène le chantier : le gel ne s'applique pas à toi.\n")
	case s.Owner == "":
		fmt.Fprintf(&b, "Session exemptée qui mène le chantier : %s.\n", SessionName(s.Exempt, s.ExemptLabel, s.ExemptScope, reader))
	case session == s.Exempt:
		fmt.Fprintf(&b, "Tu es aussi exemptée du gel (la session %s mène le chantier).\n", s.OwnerNameFor(reader))
	default:
		fmt.Fprintf(&b, "Session aussi exemptée du gel : %s.\n", SessionName(s.Exempt, s.ExemptLabel, s.ExemptScope, reader))
	}
	if owner {
		if s.Freeze {
			b.WriteString("Le gel ne s'applique pas à toi. ")
		}
		b.WriteString("Tu as posé cette maintenance : c'est à toi de la lever (`pigtail maintenance off`) dès la fin du geste. ")
	} else {
		b.WriteString("Termine ou mets en pause ce qui touche le system. " + s.SetByFR(reader) + " ")
		if s.Owner != "" {
			b.WriteString("Ne la lève pas toi-même : pigtail refuse toute autre session. ")
		}
	}
	b.WriteString("Dis-lui dans ta prochaine réponse que tu as reçu cet avis.\n")
	return b.String()
}

// LabelVisible reports whether reader may read a session label recorded
// under labelScope (D22): the master reads every label; any other reader only
// a label of its own scope. An empty labelScope (an older state, or a label
// that is only the id) is the master's only.
func LabelVisible(label, labelScope string, reader scope.Scope) bool {
	return scope.SeesAll(reader) || (labelScope != "" && scope.CanRead(reader, scope.Scope(labelScope)))
}

// SessionName is how reader names a session the state records (D22): its
// label when reader can see that session, else its short id — never its
// title.
func SessionName(id, label, labelScope string, reader scope.Scope) string {
	if label != "" && LabelVisible(label, labelScope, reader) {
		return label
	}
	return ShortID(id)
}

// ShortID: the first 8 characters of a session id.
func ShortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// OwnerNameFor names the owner session as reader may see it (SessionName);
// empty when the human set the maintenance.
func (s State) OwnerNameFor(reader scope.Scope) string {
	if s.Owner == "" {
		return ""
	}
	return SessionName(s.Owner, s.OwnerLabel, s.OwnerScope, reader)
}

// SetByFR says who set the maintenance and who lifts it (the notice).
func (s State) SetByFR(reader scope.Scope) string {
	if s.Owner != "" {
		return "Posée par la session " + s.OwnerNameFor(reader) + " — levée par elle ou par " + human() + " (`pigtail maintenance off`)."
	}
	return "Posée par " + human() + " — levée par lui seul (`pigtail maintenance off` hors de Claude Code)."
}

// SetByEN is the same sentence for the refusals (freeze, already on); the
// owner is named as reader may see it (D23, SessionName).
func (s State) SetByEN(reader scope.Scope) string {
	if s.Owner != "" {
		return "set by session " + s.OwnerNameFor(reader) + " — lifted by it or by " + human() + " (`pigtail maintenance off`)"
	}
	return "set by " + human() + " — only they lift it: `pigtail maintenance off` outside Claude Code"
}

// lifterFor names the session that lifted, as reader may see it: "" when
// the older form gave a label without id that reader may not read.
func (s State) lifterFor(reader scope.Scope) string {
	return SessionName(s.LiftedByID, s.LiftedBy, s.LiftedByScope, reader)
}

// LiftedByFR says who lifted a lifted state ("" when maintenance.lifted
// does not say: never a wrong name).
func (s State) LiftedByFR(reader scope.Scope) string {
	switch {
	case s.LiftedByUser:
		return "par " + human()
	case s.LiftedBy != "" || s.LiftedByID != "":
		if n := s.lifterFor(reader); n != "" {
			return "par la session " + n
		}
		return "par une session"
	}
	return ""
}

// LiftedByEN: "lifted by …", or bare "lifted" when unknown.
func (s State) LiftedByEN(reader scope.Scope) string {
	switch {
	case s.LiftedByUser:
		return "lifted by " + human() + " outside Claude Code"
	case s.LiftedBy != "" || s.LiftedByID != "":
		if n := s.lifterFor(reader); n != "" {
			return "lifted by session " + n
		}
		return "lifted by a session"
	}
	return "lifted"
}

// PillValue is the sidebar pill (English labels, decision of 2026-10-01).
func PillValue(s State, now time.Time) (value, color string, ok bool) {
	if !s.On {
		return "", "", false
	}
	value, color = "Maintenance", waittype.Orange.Color()
	if s.Freeze {
		value += " · freeze"
	}
	if s.Late(now) { // D6: « en retard », in English like every pill
		value, color = value+" · late", waittype.Red.Color()
	}
	return value, color, true
}

// OwnerHere ends the pill on the workspace of the session that set the
// maintenance (D1, label D3, sub_x000000000000012). English, like every pill.
const OwnerHere = "set here"

// OwnerPillValue is PillValue as the owner's workspace shows it (D1): the
// same colour, " · set here" last. Over maxRunes, " · late" goes first (the
// red colour already says it), then " · freeze"; "Maintenance" and
// "set here" always stay. ok is false without maintenance or without an
// owner (D2: the human outside Claude Code marks no workspace).
func OwnerPillValue(s State, now time.Time, maxRunes int) (value, color string, ok bool) {
	if s.Owner == "" {
		return "", "", false
	}
	if _, color, ok = PillValue(s, now); !ok {
		return "", "", false
	}
	var opt []string
	if s.Freeze {
		opt = append(opt, " · freeze")
	}
	if s.Late(now) {
		opt = append(opt, " · late")
	}
	for {
		value = "Maintenance" + strings.Join(opt, "") + " · " + OwnerHere
		if len(opt) == 0 || utf8.RuneCountInString(value) <= maxRunes {
			return value, color, true
		}
		opt = opt[:len(opt)-1] // late first, then freeze
	}
}

func unreadableHead(dir string, err error) string {
	return "maintenance state unreadable (" + filepath.Join(dir, FileName) + ": " + err.Error() +
		") — everything the freeze covers stays frozen"
}

// UnreadableMessage is the one refusal text for an unreadable state (D8:
// closed everywhere): the path and the repair. Who set it is unreadable too,
// so the repair needs a caller with no session (T1).
func UnreadableMessage(dir string, err error) string {
	return unreadableHead(dir, err) +
		"; repair: " + human() + " runs `pigtail maintenance off` outside Claude Code (who set it is unreadable too) — it rewrites a clean state"
}

// UnreadableForSession is the CLI refusal to a Claude session that runs
// `on` or `off` on an unreadable state (S22): same head, no repair
// instruction — the repair is not the session's to make (T1).
func UnreadableForSession(dir string, err error) string {
	return unreadableHead(dir, err) + "; only " + human() + ", outside Claude Code, repairs it; do not run `off`"
}

// ErrorPill is shown while the state file exists but cannot be read: a
// broken state never makes the pill disappear silently.
func ErrorPill() (value, color string) { return "Maintenance · state error", waittype.Red.Color() }

// FrozenMessage is the one refusal text (daemon 423 and session spawn), read
// by reader (D23); the caller prefixes the code (the CLI prints
// "MAINTENANCE_FROZEN: " + message).
func FrozenMessage(s State, reader scope.Scope) string {
	return "system in maintenance (freeze) since " + s.Since.Local().Format("15:04") + ": " + s.Reason +
		" — " + s.SetByEN(reader)
}
