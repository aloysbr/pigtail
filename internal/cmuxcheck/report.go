package cmuxcheck

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/scope"
)

// Fixable: a remaining defect `pigtail cmux check --fix` can lift (D15) —
// actions planned, nothing blocks, no repair tried yet.
func (r Row) Fixable() bool {
	return r.Remaining && r.Blocked == "" && r.Result == "" && len(r.Actions) > 0
}

// Stuck: a remaining defect only a hand lifts (D15) — blocked, or its repair
// refused or failed. A remaining row neither Fixable nor Stuck has no action
// at all (background work lost, D9): reported only, no pill (D16).
func (r Row) Stuck() bool {
	return r.Remaining && (r.Blocked != "" || r.Result != "")
}

// busyBlocked: one segment of Blocked starts "busy:" — an occupation, read
// even on a healthy row (Remaining false: the pilot message, revue 1 #4).
func busyBlocked(blocked string) bool {
	for _, seg := range strings.Split(blocked, "; ") {
		if strings.HasPrefix(seg, "busy:") {
			return true
		}
	}
	return false
}

// BusyLeft: a remaining row blocked by an occupation — what the second pass
// (D6) looks at again.
func (r Row) BusyLeft() bool {
	return r.Remaining && busyBlocked(r.Blocked)
}

// BusyLeft lists the surfaces the second pass (D6) looks at again for the
// launch cmuxPID: rows left busy that this mode repairs — the pilot's alone
// when pilotOnly (revue 1 #5) —, and the pilot whose message is pending.
// Another launch's report, or one that could not look, lists nothing.
func (r Report) BusyLeft(cmuxPID int, pilotOnly bool) []string {
	if r.Fatal != "" || r.Launch.PID != cmuxPID {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, row := range r.Rows {
		if pilotOnly && (r.Pilot.SurfaceID == "" || !sameSurface(row.SurfaceID, r.Pilot.SurfaceID)) {
			continue
		}
		if row.BusyLeft() && !seen[surfaceKey(row.SurfaceID)] {
			seen[surfaceKey(row.SurfaceID)] = true
			out = append(out, row.SurfaceID)
		}
	}
	if r.Pilot.MessagePending && r.Pilot.SurfaceID != "" && !seen[surfaceKey(r.Pilot.SurfaceID)] {
		out = append(out, r.Pilot.SurfaceID)
	}
	return out
}

// Defects counts the rows `--fix` can lift or only a hand can (D15). D17
// (decided 2026-10-08): a reported-only row (background work lost, D9/D16)
// is no defect — it stays on the report line, the exit code is 0 for it.
func (r Report) Defects() int {
	fix, left, _ := r.Split()
	return fix + left
}

// Split shares the remaining rows as the pill does (D15, D16, É21); the exit
// code follows it too (D17). It counts by Category, so a redacted report's
// masked rows (D18) are counted as the full report counts them.
func (r Report) Split() (fix, left, reported int) {
	for _, row := range r.Rows {
		switch row.Category() {
		case CatFix:
			fix++
		case CatLeft:
			left++
		case CatReported:
			reported++
		}
	}
	return fix, left, reported
}

// Category is a row's class, as the report line, the exit code and the pill
// share the rows (D15–D17); the only content a masked row keeps (D18).
type Category string

const (
	CatOK       Category = "ok"
	CatRepaired Category = "repaired"
	CatFix      Category = "to fix"
	CatLeft     Category = "left"
	CatReported Category = "reported only"
)

// Category: Masked on a row the reader cannot read (Redacted, D18), else from
// Fixable / Stuck / Remaining / Done (Global Constraints: never Remaining alone).
func (r Row) Category() Category {
	switch {
	case r.Masked != "":
		return r.Masked
	case r.Fixable():
		return CatFix
	case r.Stuck():
		return CatLeft
	case r.Remaining:
		return CatReported // D16: background work lost only
	case len(r.Done) > 0:
		return CatRepaired
	}
	return CatOK
}

// ClosedOrphans counts the bound surfaces closed since the relaunch whose
// conversation runs nowhere the check sees (D10): info, never a defect.
func (r Report) ClosedOrphans() int {
	n := 0
	for _, row := range r.Rows {
		if row.ClosedOrphan {
			n++
		}
	}
	return n
}

func (r Report) repaired() int {
	n := 0
	for _, row := range r.Rows {
		if row.Category() == CatRepaired {
			n++
		}
	}
	return n
}

// ExitCode: 2 could not look, 1 a row to fix or left remains, 0 none — a
// reported-only row included (D17).
func (r Report) ExitCode() int {
	switch {
	case r.Fatal != "":
		return 2
	case r.Defects() > 0:
		return 1
	}
	return 0
}

func (r Report) Line() string {
	if r.Fatal != "" {
		return "cmux check: " + r.Fatal + " — nothing checked"
	}
	end := "clean quit"
	if r.Launch.Previous == nil {
		end = "no earlier launch in the logs"
	} else if !r.Launch.Previous.CleanQuit() {
		end = "crash"
	}
	s := fmt.Sprintf("cmux check: launch %s (%s) · %d sessions", r.Launch.Start.Local().Format("15:04:05"), end, len(r.Rows))
	fix, left, reported := r.Split()
	if r.Fix {
		s += fmt.Sprintf(" · %d repaired", r.repaired())
	}
	if fix > 0 { // É21: invite to --fix only when it can lift something
		s += fmt.Sprintf(" · %d to fix — run `pigtail cmux check --fix`", fix)
	}
	if left > 0 || r.Fix {
		s += fmt.Sprintf(" · %d left", left)
	}
	if reported > 0 { // D16: never "to fix", never a pill
		s += fmt.Sprintf(" · %d reported only", reported)
	}
	if !r.Fix && fix+left+reported == 0 {
		s += " · all fine"
	}
	return s
}

func (r Report) Write(w io.Writer) {
	fmt.Fprintln(w, r.Line())
	if f := r.LastFailure; f != nil {
		fmt.Fprintf(w, "  note: a later partial check at %s could not look (%s) — this report kept\n", f.At.Local().Format("15:04:05"), f.Fatal)
	}
	if n := r.ClosedOrphans(); n > 0 { // D10: a header line, the exit code unchanged
		fmt.Fprintf(w, "  %d bound surfaces closed, conversation alive nowhere\n", n)
	}
	for _, n := range r.Notes {
		fmt.Fprintln(w, "  note: "+n)
	}
	if r.Pilot.SurfaceID != "" { // D3–D5: the master only (Redacted never copies Pilot)
		fmt.Fprintf(w, "  pilot: %s (%s)%s\n", maintenance.ShortID(r.Pilot.SurfaceID), r.Pilot.Source, pilotMsg(r.Pilot))
	}
	for _, row := range r.Rows {
		if row.Masked != "" { // D18: another scope's row, its short id and class only
			fmt.Fprintf(w, "  %-40.40s %s — details from the master cell\n", row.SurfaceID, row.Masked)
			continue
		}
		state := "ok"
		switch {
		case row.Fixable(): // D15: "left" is a stuck row only
			state = "TO FIX: " + strings.Join(row.Problems, "; ")
		case row.Remaining && !row.Stuck(): // D16
			state = "reported only: " + strings.Join(row.Problems, "; ")
		case row.Remaining:
			why := row.Blocked
			if row.Result != "" {
				why = row.Result
			}
			state = "LEFT: " + strings.Join(row.Problems, "; ")
			if why != "" {
				state += " — " + why
			}
		case len(row.Done) > 0:
			state = "repaired: " + joinActions(row.Done)
		case row.Result != "": // nothing done, not left (a resume: « input back, nothing done »)
			state = row.Result
		}
		fmt.Fprintf(w, "  %-40.40s %s\n", row.Name(), state)
		for _, i := range row.Info {
			fmt.Fprintf(w, "  %-40s info: %s\n", "", i)
		}
	}
	for _, i := range r.Info {
		fmt.Fprintln(w, "  info: "+i)
	}
	for _, i := range r.Machine { // D18: lines that name no session
		fmt.Fprintln(w, "  info: "+i)
	}
}

// pilotMsg: the message part of the pilot line (lot 3), "" before any.
func pilotMsg(p PilotInfo) string {
	if p.Message == "" {
		return ""
	}
	return " — message " + p.Message
}

// Name is what the report calls a row: its workspace's title (the master's
// reading: what the sidebar shows), else the pane's own, else its short
// surface id. Redacted drops the workspace title for any other reader (D23).
func (r Row) Name() string {
	switch {
	case r.WorkspaceTitle != "":
		return r.WorkspaceTitle
	case r.Title != "":
		return r.Title
	}
	return maintenance.ShortID(r.SurfaceID)
}

func joinActions(as []Action) string {
	s := make([]string, len(as))
	for i, a := range as {
		s[i] = string(a)
	}
	return strings.Join(s, ", ")
}

// Redacted is the report as reader may print it — an allow-list (D18,
// decided 2026-10-08; D23 of the repo). The master reads everything. For any
// other reader it builds a new Report: the pass's own facts (time, launch,
// photo source, fatal, partial) and its Machine lines, plus its Info, Notes
// and PassScope only when the pass ran in reader's scope — another pass's
// lines name its sessions or its scope (machineInfo, peersScopeNote). A row
// whose scope reader cannot read (rowScope: its account's cell, and the
// running process's; unresolved = masked) becomes a new Row holding the short
// surface id and its class (Masked), nothing else: a field added to Row later
// stays zero until it is added here. last.json keeps the full report (its
// reader, pigtaild, is master).
func (r Report) Redacted(reader scope.Scope, scopeOf func(configDir string) (string, bool)) Report {
	if scope.SeesAll(reader) {
		return r
	}
	out := Report{At: r.At, Fix: r.Fix, Launch: r.Launch, PhotoSource: r.PhotoSource,
		Fatal: r.Fatal, Partial: r.Partial, Machine: r.Machine, LastFailure: r.LastFailure}
	if r.PassScope != "" && scope.Canonical(r.PassScope) == scope.Canonical(string(reader)) {
		out.PassScope, out.Notes, out.Info = r.PassScope, r.Notes, r.Info
	}
	out.Rows = make([]Row, len(r.Rows))
	for i, row := range r.Rows {
		if sc := rowScope(row, scopeOf); sc != "" && scope.CanRead(reader, scope.Scope(sc)) {
			// D23: the row's scope is its account's; its workspace's title
			// is a bound subject's, of a scope nothing proves — never shown
			// to a non-master reader, the pane's own title (or the short id)
			// names the row instead.
			row.WorkspaceTitle = ""
			out.Rows[i] = row
			continue
		}
		row.Masked = "" // the class comes from the row's own fields, never an input's Masked
		out.Rows[i] = Row{SurfaceID: maintenance.ShortID(row.SurfaceID), Masked: row.Category()}
	}
	return out
}

// rowScope: the scope of the row's account, "" when it does not resolve or
// when the running process's account (LiveConfigDir) resolves to another.
func rowScope(row Row, scopeOf func(string) (string, bool)) string {
	sc, ok := scopeOf(row.ConfigDir)
	if !ok {
		return ""
	}
	if row.LiveConfigDir != "" && row.LiveConfigDir != row.ConfigDir {
		if live, ok := scopeOf(row.LiveConfigDir); !ok || live != sc {
			return ""
		}
	}
	return sc
}

// PartialPass: the pass looked at part of the surfaces only — --surface
// narrowed it, or its photo was rebuilt from one scope's peers (D7,
// peersScopeNote). Its report must not erase the other surfaces' rows (É25).
//
// A non-master cell's pass that could not look (fatal) counts as partial
// too: nothing says what its photo would have covered.
func PartialPass(o Options, r Report) bool {
	return len(o.Surfaces) > 0 || peersScopeNote(r.PhotoSource, o.PeersScope) != "" ||
		(r.Fatal != "" && o.PeersScope != "")
}

// mergePartial (É25, B2): a partial pass keeps, from the last report of the
// same cmux launch, the rows of the surfaces it did not look at — their pills
// stay (Task 9); the surfaces it looked at take their new row, so `--surface
// S1 --fix` clears S1's pill. A fatal partial pass looked at nothing: it
// never replaces a good report of the same launch (or of a launch it could
// not tell), which keeps its rows and names the failure (LastFailure); its
// exit 2 and stderr say it too. A full pass, a fatal last.json, another
// launch or an unreadable last.json: the new report alone. Called under Lock
// (the command), so no other check writes in between.
func mergePartial(dir string, r Report) Report {
	if !r.Partial {
		return r
	}
	prev, err := LoadLast(dir)
	if err != nil || prev.Fatal != "" {
		return r
	}
	if r.Fatal != "" {
		if r.Launch.PID != 0 && r.Launch.PID != prev.Launch.PID {
			return r
		}
		prev.LastFailure = &Failure{At: r.At, Fatal: r.Fatal}
		return prev
	}
	if prev.Launch.PID != r.Launch.PID {
		return r
	}
	seen := map[string]bool{}
	for _, row := range r.Rows {
		seen[surfaceKey(row.SurfaceID)] = true
	}
	rows := append([]Row(nil), r.Rows...)
	for _, row := range prev.Rows {
		if !seen[surfaceKey(row.SurfaceID)] {
			rows = append(rows, row)
		}
	}
	r.Rows = rows
	return r
}

// StateDir is $XDG_STATE_HOME/pigtail/cmux-check (else ~/.local/state/…).
func StateDir() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "pigtail", "cmux-check")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "pigtail", "cmux-check")
}

// SaveLast writes last.json atomically; a partial pass is merged into the
// previous report of the same launch first (mergePartial, É25).
func SaveLast(dir string, r Report) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	r = mergePartial(dir, r)
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".last.%d.json", time.Now().UnixNano()))
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "last.json"))
}

func LoadLast(dir string) (Report, error) {
	b, err := os.ReadFile(filepath.Join(dir, "last.json"))
	if err != nil {
		return Report{}, err
	}
	var r Report
	return r, json.Unmarshal(b, &r)
}
