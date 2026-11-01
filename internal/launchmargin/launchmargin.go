// Package launchmargin is the one launch margin guard (sub_x000000000000011,
// D1/D5): a new interactive Claude launch never lands silently on an account
// past its account's ceiling (claudeusage.Thresholds). The requested cell
// is kept when its account is under the margin or unread; it is switched to a
// free account of the same scope otherwise; when every account of the scope
// is over the margin the launch is refused with the first release time,
// unless the caller forces it (--over-margin). When the guard cannot measure
// (rotation off, no candidate) it fails open, loudly (unmeasured).
//
// Every dependency is injected (Deps): no global, no os call, so the CLI
// (usage pick, spawn, respawn) and the daemon (dispatch, run) share it and
// their tests stub everything. Every text a Decision carries is built here,
// already bounded for the caller (Deps.Viewer, D10): a caller that may not
// name the requested cell's scope never reads another cell, account or scope
// name in a warning, a refusal, a trace or a journal row (D19/D23).
package launchmargin

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/scope"
)

// EnvRotation switches the account rotation (and so the guard's measure) off
// everywhere: PIGTAIL_ROTATION=off.
const EnvRotation = "PIGTAIL_ROTATION"

// ErrRotationOff: the rotation is switched off by the environment.
var ErrRotationOff = errors.New("off (" + EnvRotation + "=off)")

// RotationOff reports PIGTAIL_ROTATION=off.
func RotationOff(getenv func(string) string) bool {
	return strings.EqualFold(strings.TrimSpace(getenv(EnvRotation)), "off")
}

// Actions of a Decision.
const (
	ActionOK         = "ok"
	ActionSwitched   = "switched"
	ActionForced     = "forced"
	ActionUnmeasured = "unmeasured"
	ActionRefused    = "refused"
)

// ReasonLauncherProto: a respawn refused because its launcher cannot start
// a fresh session on another cell (D8).
const ReasonLauncherProto = "launcher-proto"

// Viewer is the caller the texts are bounded for (D10): the master sees every
// name; any other caller only names its own scope's cells and accounts.
type Viewer struct {
	Master bool
	Scope  string
}

// Deps is everything the guard touches.
type Deps struct {
	Getenv   func(string) string
	ReadFile func(string) ([]byte, error)
	Now      func() time.Time
	Cells    *claudeprofile.Table
	Viewer   Viewer
}

// Decision is what the guard decided for one launch. Warning, Margin and
// Summary are precomputed and bounded for Deps.Viewer: renderers and Record
// only copy them.
type Decision struct {
	Action    string
	Requested claudeprofile.Cell
	Final     claudeprofile.Cell
	Choice    *claudeusage.Choice
	FreesAt   *time.Time
	// Warning is the line to show the caller (without "warning: "); "" for ok.
	Warning string
	// Margin is the spawns.jsonl margin value: switched <req>-><final>,
	// forced <cell>, unmeasured <why>, refused <scope|cell> frees <t|unknown>,
	// refused <cell> launcher-proto.
	Margin string
	// Summary is the journal row's one line.
	Summary string
	// Reason: empty except a caller-side refusal (ReasonLauncherProto).
	Reason string
	// At is when the guard decided (the trace's timestamp).
	At time.Time
	// SubjectID is set by the caller when the launch is a subject's (trace only).
	SubjectID string
}

// OverMarginError is the refusal: every account of the scope is over the
// margin. Its message is bounded for the caller like the Decision's texts.
type OverMarginError struct {
	Scope, Account string
	FreesAt        *time.Time
	msg            string
	api            string // msg without the CLI's --over-margin advice
}

func (e *OverMarginError) Error() string { return e.msg }

// APIMessage is the refusal as the API answers it: without the
// --over-margin advice, a CLI flag the API does not offer (Q2 of
// sub_x000000000000015). Error keeps the advice for the CLI.
func (e *OverMarginError) APIMessage() string {
	if e.api == "" {
		return e.msg
	}
	return e.api
}

// OverBudgetError is Staffing's refusal: no account of the scope can run a
// launch, at least one of them because of its weekly token budget. Its message
// names the scope, never an account nor a cell. Mixed: another account is held
// by its margin alone.
type OverBudgetError struct {
	Scope   string
	FreesAt *time.Time
	Mixed   bool
}

func (e *OverBudgetError) Error() string { return e.APIMessage() }

// APIMessage is the refusal as the API and the staffing journal say it.
func (e *OverBudgetError) APIMessage() string {
	what := "weekly token budget"
	if e.Mixed {
		what = "margin or weekly budget"
	}
	if e.FreesAt == nil {
		return fmt.Sprintf("every account of scope %s is out of its %s; reset time unknown", e.Scope, what)
	}
	return fmt.Sprintf("every account of scope %s is out of its %s; first release at %s", e.Scope, what, e.FreesAt.Format("Mon 15:04"))
}

// UnmeasuredBudgetError is Staffing's refusal when no account of the scope is
// choosable and none is known over its budget, at least one being unmeasured
// (see claudeusage.BudgetFor: reading absent, too old, a window missing). Why is the first
// unmeasured account's reason; it never carries a label.
type UnmeasuredBudgetError struct {
	Scope, Why string
}

func (e *UnmeasuredBudgetError) Error() string { return e.APIMessage() }

// APIMessage is the refusal as the API and the staffing journal say it.
func (e *UnmeasuredBudgetError) APIMessage() string { return "unmeasured: " + e.Why }

// CellInputs is the cell table as claudeusage reads it, in config-dir order —
// the CLI's CellsFrom and the daemon's usageCellInputs, which this package
// cannot import.
func CellInputs(tbl *claudeprofile.Table) []claudeusage.CellInput {
	dirs := tbl.ConfigDirs()
	out := make([]claudeusage.CellInput, 0, len(dirs))
	for _, dir := range dirs {
		c, _ := tbl.ByConfigDir(dir)
		out = append(out, c.UsageInput())
	}
	return out
}

// PickIn picks the cell a launch in scope sc runs on (claudeusage.Pick over
// the cell table). limited: accountUuid → limited until. Rotation off →
// ErrRotationOff.
func PickIn(tbl *claudeprofile.Table, sc string, limited map[string]time.Time,
	getenv func(string) string, readFile func(string) ([]byte, error), now time.Time) (claudeprofile.Cell, claudeusage.Choice, error) {
	if RotationOff(getenv) {
		return claudeprofile.Cell{}, claudeusage.Choice{}, ErrRotationOff
	}
	return pickSnaps(tbl, claudeusage.ReadTable(CellInputs(tbl), readFile), sc, limited, false, now)
}

func pickSnaps(tbl *claudeprofile.Table, snaps []claudeusage.Snapshot, sc string, limited map[string]time.Time, budgeted bool, now time.Time) (claudeprofile.Cell, claudeusage.Choice, error) {
	s, err := scope.Parse(sc)
	if err != nil {
		return claudeprofile.Cell{}, claudeusage.Choice{}, err
	}
	ch, err := claudeusage.Pick(snaps, claudeusage.PickOptions{Scope: s, Limited: limited, Budgeted: budgeted}, now)
	if err != nil {
		return claudeprofile.Cell{}, ch, err
	}
	c, ok := tbl.Cell(ch.Cell.ID)
	if !ok { // Pick only returns table cells: a broken invariant, said
		return claudeprofile.Cell{}, ch, fmt.Errorf("picked cell %s is not in the cell table", ch.Cell.ID)
	}
	return c, ch, nil
}

// Bounded: may the viewer NOT name the scope of a cell of scope sc?
func (v Viewer) Bounded(sc string) bool {
	if v.Master {
		return false
	}
	return v.Scope == "" || string(scope.Canonical(v.Scope)) != string(scope.Canonical(sc))
}

func (d Deps) now() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

func (d Deps) getenv(k string) string {
	if d.Getenv == nil {
		return ""
	}
	return d.Getenv(k)
}

// accountOf is the account a cell is logged into, from its own .claude.json
// snapshot (never the table's letter); "" = unread or not logged in.
func accountOf(snaps []claudeusage.Snapshot, id string) string {
	for _, s := range snaps {
		if s.Cell.ID == id && s.Err == nil {
			return s.AccountUUID
		}
	}
	return ""
}

// overIn: the account's candidate in the ranking, when it is over margin.
func overIn(ch *claudeusage.Choice, uuid string) (claudeusage.Candidate, bool) {
	if ch == nil || uuid == "" {
		return claudeusage.Candidate{}, false
	}
	for _, c := range ch.Ranking {
		if c.AccountUUID == uuid {
			return c, c.Group == claudeusage.GroupOverMargin
		}
	}
	return claudeusage.Candidate{}, false // absent = under the margin
}

func (d Deps) snaps() []claudeusage.Snapshot {
	return claudeusage.ReadTable(CellInputs(d.Cells), d.ReadFile)
}

// Guard decides the cell a launch requested on cell requested runs on.
// force (--over-margin) imposes the requested cell without reading anything.
// The error is an *OverMarginError on a refusal, nil otherwise.
func Guard(d Deps, requested claudeprofile.Cell, force bool) (claudeprofile.Cell, Decision, error) {
	dec := Decision{Requested: requested, Final: requested, At: d.now()}
	if force {
		return requested, forced(d, dec, requested), nil
	}
	if d.Cells == nil {
		return requested, unmeasured(d, dec, "no-cell-table", errors.New("no cell table")), nil
	}
	if RotationOff(d.getenv) {
		return requested, unmeasured(d, dec, "rotation-off", ErrRotationOff), nil
	}
	snaps := d.snaps()
	acct := accountOf(snaps, requested.ID)
	if acct == "" {
		dec.Action = ActionOK // no reading, not logged in: nothing to measure against
		return requested, dec, nil
	}
	final, ch, err := pickSnaps(d.Cells, snaps, requested.Scope, nil, false, dec.At)
	if err != nil {
		return requested, unmeasured(d, dec, whyToken(err), err), nil
	}
	dec.Choice = &ch
	mine, over := overIn(&ch, acct)
	if !over {
		dec.Action = ActionOK
		return requested, dec, nil
	}
	if ch.Group != claudeusage.GroupOverMargin {
		dec.Action, dec.Final = ActionSwitched, final
		dec.Margin = fmt.Sprintf("switched %s->%s", requested.ID, final.ID)
		if d.Viewer.Bounded(requested.Scope) {
			dec.Warning = fmt.Sprintf("launch margin: cell %s is over the margin — launching on %s instead", requested.ID, final.ID)
			dec.Summary = fmt.Sprintf("launch margin: cell %s over the margin — switched to %s", requested.ID, final.ID)
		} else {
			dec.Warning = fmt.Sprintf("launch margin: cell %s (account %s) is over the margin — launching on %s (account %s) instead", requested.ID, mine.Label, final.ID, ch.Label)
			dec.Summary = fmt.Sprintf("launch margin: cell %s (account %s) over the margin — switched to %s (account %s)", requested.ID, mine.Label, final.ID, ch.Label)
		}
		return final, dec, nil
	}
	refusal := refuse(d, &dec, requested, ch)
	return requested, dec, refusal
}

// GuardScope is the no-cell form (a launch picked in scope sc): the same
// refusal rule, no switch — the Pick choice is the cell. A Pick that cannot
// measure (rotation off, no candidate) returns its error with an unmeasured
// Decision and no cell: the caller keeps its own fallback and says so.
func GuardScope(d Deps, sc string, force bool) (claudeprofile.Cell, Decision, error) {
	dec := Decision{Requested: claudeprofile.Cell{Scope: sc}, At: d.now()}
	if d.Cells == nil {
		err := errors.New("no cell table")
		return claudeprofile.Cell{}, unmeasured(d, dec, "no-cell-table", err), err
	}
	if RotationOff(d.getenv) {
		return claudeprofile.Cell{}, unmeasured(d, dec, "rotation-off", ErrRotationOff), ErrRotationOff
	}
	cell, ch, err := pickSnaps(d.Cells, d.snaps(), sc, nil, false, dec.At)
	if err != nil {
		return claudeprofile.Cell{}, unmeasured(d, dec, whyToken(err), err), err
	}
	dec.Choice = &ch
	dec.Requested, dec.Final = cell, cell
	if force {
		return cell, forced(d, dec, cell), nil
	}
	if ch.Group != claudeusage.GroupOverMargin {
		dec.Action = ActionOK
		return cell, dec, nil
	}
	refusal := refuse(d, &dec, cell, ch)
	return cell, dec, refusal
}

// GuardScopeStaffing is GuardScope for Staffing's autonomous launches: the pick
// also holds back an account that is not within its weekly token budget, and
// there is no force. A refusal returns no cell: *OverBudgetError when an account
// is over its budget, else *UnmeasuredBudgetError when one is unmeasured, else
// the plain *OverMarginError. No table, rotation off or no candidate return
// GuardScope's error, with no cell: it never falls back, what is not measured
// closes. Only Staffing calls it (D6).
func GuardScopeStaffing(d Deps, sc string) (claudeprofile.Cell, Decision, error) {
	dec := Decision{Requested: claudeprofile.Cell{Scope: sc}, At: d.now()}
	if d.Cells == nil {
		err := errors.New("no cell table")
		return claudeprofile.Cell{}, unmeasured(d, dec, "no-cell-table", err), err
	}
	if RotationOff(d.getenv) {
		return claudeprofile.Cell{}, unmeasured(d, dec, "rotation-off", ErrRotationOff), ErrRotationOff
	}
	cell, ch, err := pickSnaps(d.Cells, d.snaps(), sc, nil, true, dec.At)
	if err != nil {
		return claudeprofile.Cell{}, unmeasured(d, dec, whyToken(err), err), err
	}
	dec.Choice = &ch
	dec.Requested, dec.Final = cell, cell
	if ch.Group != claudeusage.GroupOverMargin {
		dec.Action = ActionOK
		return cell, dec, nil
	}
	return claudeprofile.Cell{}, dec, refuseStaffing(d, &dec, sc, cell, ch)
}

// refuseStaffing fills the Decision of a scope with no choosable account and
// says which refusal it is, reading the budgets Pick put on the ranking.
func refuseStaffing(d Deps, dec *Decision, sc string, cell claudeprofile.Cell, ch claudeusage.Choice) error {
	over, marginOnly, why := false, false, ""
	for _, c := range ch.Ranking {
		switch {
		case c.Budget == nil:
		case c.Budget.State == claudeusage.BudgetOver:
			over = true
		case c.Budget.State == claudeusage.BudgetWithin:
			marginOnly = true
		case why == "":
			why = c.Budget.Why
		}
	}
	var refusal error
	stamp := "unknown"
	switch {
	case over:
		e := &OverBudgetError{Scope: sc, Mixed: marginOnly}
		if ch.FreesAt != nil {
			t := ch.FreesAt.In(dec.At.Location())
			e.FreesAt = &t
			stamp = ch.FreesAt.UTC().Format(time.RFC3339)
		}
		dec.FreesAt = ch.FreesAt
		dec.Margin = fmt.Sprintf("refused %s budget frees %s", sc, stamp)
		refusal = e
	case why != "":
		refusal = &UnmeasuredBudgetError{Scope: sc, Why: why}
		dec.Margin = fmt.Sprintf("refused %s budget frees %s", sc, stamp)
	default:
		return refuse(d, dec, cell, ch)
	}
	dec.Action, dec.Warning = ActionRefused, refusal.Error()
	dec.Summary = clip("launch margin: staffing refused — " + refusal.Error())
	return refusal
}

// overMarginAdvice ends the CLI's refusal; the API has no such flag.
const overMarginAdvice = " — pass --over-margin to launch anyway"

// refuse fills a refusal Decision and its error, bounded for the viewer.
func refuse(d Deps, dec *Decision, requested claudeprofile.Cell, ch claudeusage.Choice) *OverMarginError {
	dec.Action, dec.Final, dec.FreesAt = ActionRefused, requested, ch.FreesAt
	when, stamp := "reset time unknown", "unknown"
	if ch.FreesAt != nil {
		when = "at " + ch.FreesAt.In(dec.At.Location()).Format("Mon 15:04")
		stamp = ch.FreesAt.UTC().Format(time.RFC3339)
	}
	e := &OverMarginError{FreesAt: ch.FreesAt}
	margins := "(" + claudeusage.OverMarginRule + ")"
	if d.Viewer.Bounded(requested.Scope) {
		e.api = fmt.Sprintf("every account that can run cell %s is over the margin %s; first release %s", requested.ID, margins, when)
		e.msg = e.api + overMarginAdvice
		dec.Margin = fmt.Sprintf("refused %s frees %s", requested.ID, stamp)
		dec.Summary = fmt.Sprintf("launch margin: refused cell %s — every account over the margin, first release %s", requested.ID, when)
	} else {
		e.Scope, e.Account = requested.Scope, ch.Label
		e.api = fmt.Sprintf("every account of scope %s is over the margin %s; first release: %s %s", requested.Scope, margins, ch.Label, when)
		e.msg = e.api + overMarginAdvice
		dec.Margin = fmt.Sprintf("refused %s frees %s", requested.Scope, stamp)
		dec.Summary = fmt.Sprintf("launch margin: refused cell %s — every account of scope %s over the margin, first release: %s %s", requested.ID, requested.Scope, ch.Label, when)
	}
	dec.Warning = e.msg
	return e
}

func forced(_ Deps, dec Decision, cell claudeprofile.Cell) Decision {
	dec.Action, dec.Final = ActionForced, cell
	dec.Margin = "forced " + cell.ID
	dec.Warning = "launch margin: --over-margin — launching on cell " + cell.ID + " without the margin check"
	dec.Summary = "launch margin: --over-margin on cell " + cell.ID
	return dec
}

// unmeasured: the guard could not measure; the launch goes on, said. The
// full reason (which may name the scope's cells) only reaches a viewer that
// may name them.
func unmeasured(d Deps, dec Decision, token string, err error) Decision {
	dec.Action = ActionUnmeasured
	why := token
	if !d.Viewer.Bounded(dec.Requested.Scope) && err != nil {
		why = err.Error()
	}
	on := ""
	if dec.Requested.ID != "" {
		on = " — launching on cell " + dec.Requested.ID
	}
	dec.Warning = "launch margin: not measured (" + why + ")" + on
	dec.Margin = "unmeasured " + token
	dec.Summary = clip("launch margin: not measured (" + why + ")" + on)
	return dec
}

// Unmeasured is the decision for a launch the guard cannot link to a cell
// (the daemon's profile with no config dir, or one matching no cell): the
// launch goes on, traced unmeasured.
func Unmeasured(d Deps, requested claudeprofile.Cell, token string) Decision {
	return unmeasured(d, Decision{Requested: requested, Final: requested, At: d.now()}, token, nil)
}

// whyToken is the short, nameless reason an unmeasured trace carries.
func whyToken(err error) string {
	switch {
	case errors.Is(err, ErrRotationOff):
		return "rotation-off"
	case errors.Is(err, claudeusage.ErrNoCandidate):
		return "no-candidate"
	}
	return "pick-error"
}

func clip(s string) string {
	if r := []rune(s); len(r) > 280 {
		return string(r[:280])
	}
	return s
}

// AccountOver reports whether the account cell c is logged into is over the
// margin in the guard's own ranking (dec.Choice), read, never recomputed: the
// AccountUUID of c's snapshot matched against Ranking[i].AccountUUID (never
// Candidate.Cell.ID, the account's lowest-rank cell). Absent = under.
func AccountOver(d Deps, c claudeprofile.Cell, dec Decision) bool {
	if d.Cells == nil {
		return false
	}
	_, over := overIn(dec.Choice, accountOf(d.snaps(), c.ID))
	return over
}

// LauncherProtoRefusal turns dec into the D8 refusal of a respawn whose c
// launcher cannot switch account while the caller's account is over the
// margin. The caller's own cell id is the only name it carries.
func LauncherProtoRefusal(dec Decision, caller claudeprofile.Cell) (Decision, error) {
	dec.Action, dec.Reason, dec.Final = ActionRefused, ReasonLauncherProto, caller
	msg := "respawn refused: launcher too old to switch account — re-source c"
	dec.Warning = msg
	dec.Margin = "refused " + caller.ID + " " + ReasonLauncherProto
	dec.Summary = "launch margin: respawn refused on cell " + caller.ID + " — launcher too old to switch account"
	return dec, errors.New(msg)
}

// ForcedOn is the --over-margin override of a caller-side refusal (D8): the
// launch goes on cell, decision forced.
func ForcedOn(d Deps, dec Decision, cell claudeprofile.Cell) Decision {
	return forced(d, dec, cell)
}

// Render is the line a caller shows for a decision: the refusal message when
// err is one, else the Decision's warning ("" for ok). It only copies.
func Render(dec Decision, err error) string {
	if err != nil {
		return err.Error()
	}
	return dec.Warning
}
