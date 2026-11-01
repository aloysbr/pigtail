package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/launchmargin"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/waittype"
)

// The Stop hook's side of the anticipatory switch (sub_x000000000000023): at
// the end of an interactive main-session turn, when the session's account is
// at or above its switch threshold and a fresh account of the scope is under
// its ceiling, start `pigtail session rotate --ahead` detached. The free
// checks come first, then one reading of the account's own cells, the whole
// table last; every failure fails open (nothing started, nothing printed).

// Seams — tests replace them; no test reads a real .claude.json nor forks.
var (
	// aheadReadCell reads one cell's cached reading.
	aheadReadCell = func(c claudeusage.CellInput) claudeusage.Snapshot { return claudeusage.ReadCell(c, os.ReadFile) }
	// aheadOwn: the table, the session's cell, the freshest reading of its
	// account among the cells logged into that accountUuid (M5, m3), and
	// the account's thresholds (field-wise minimum over those cells).
	aheadOwn = func(getenv func(string) string) (*claudeprofile.Table, claudeprofile.Cell, claudeusage.Snapshot, claudeusage.Thresholds, error) {
		tbl, err := rotLoadTable()
		if err != nil {
			return nil, claudeprofile.Cell{}, claudeusage.Snapshot{}, claudeusage.Thresholds{}, err
		}
		cell, ok := tbl.ByConfigDir(claudeConfigDir(getenv))
		if !ok {
			return nil, claudeprofile.Cell{}, claudeusage.Snapshot{}, claudeusage.Thresholds{}, errors.New("session outside the cell table")
		}
		own := aheadReadCell(cell.UsageInput())
		if own.Err != nil || own.AccountUUID == "" {
			return tbl, cell, own, cell.UsageInput().Thresholds(), nil
		}
		best, ths := own, []claudeusage.Thresholds{cell.UsageInput().Thresholds()}
		// Twins are the cells logged into the same accountUuid, as Pick groups
		// them — never the account letter, which can name another account.
		for _, id := range tbl.IDs() {
			c, _ := tbl.Cell(id)
			if c.ID == cell.ID {
				continue
			}
			s := aheadReadCell(c.UsageInput())
			if s.Err != nil || s.AccountUUID != own.AccountUUID {
				continue
			}
			ths = append(ths, c.UsageInput().Thresholds())
			if s.HasReading && (!best.HasReading || s.FetchedAt.After(best.FetchedAt)) {
				best = s
			}
		}
		th := ths[0]
		for _, t := range ths[1:] {
			th.SelectPct = min(th.Ceiling(), t.Ceiling())
			th.SwitchPct = min(th.SwitchAt(), t.SwitchAt())
		}
		return tbl, cell, best, th, nil
	}
	// aheadPick: the pick of the cell's scope over the cached readings of the
	// whole table, and the accountUuid of the cell's own reading. No daemon,
	// no network.
	aheadPick = func(tbl *claudeprofile.Table, cell claudeprofile.Cell, now time.Time) (claudeusage.Choice, string, error) {
		sc, err := scope.Parse(cell.Scope)
		if err != nil {
			return claudeusage.Choice{}, "", err
		}
		snaps := claudeusage.ReadTable(launchmargin.CellInputs(tbl), os.ReadFile)
		acct := ""
		for _, s := range snaps {
			if s.Cell.ID == cell.ID {
				acct = s.AccountUUID
			}
		}
		ch, err := claudeusage.Pick(snaps, claudeusage.PickOptions{Scope: sc}, now)
		return ch, acct, err
	}
	// startRotationAhead starts the detached anticipatory worker.
	startRotationAhead = func(sid, surface string, afterPID int, turn string, size int64) error {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		_, err = startDetachedWorkerCmd(self, rotateAheadArgv(sid, surface, afterPID, turn, size), nil, rotateLogPath(sid))
		return err
	}
)

// stampWithin: the stamp at path is younger than d.
func stampWithin(path string, d time.Duration, now time.Time) bool {
	at, ok := readStamp(path)
	return ok && now.Sub(at) < d
}

// maybeRotateAhead is called by the Stop hook of a main session, after the
// turn end is posted. It never prints and never fails the hook.
func maybeRotateAhead(in waittype.HookInput, turn claudesession.Turn, haveTurn, working bool, getenv func(string) string) {
	sid := in.SessionID
	surface := getenv("CMUX_SURFACE_ID")
	if in.HookEventName != "Stop" || in.AgentID != "" || !interactiveTurn(turn, haveTurn) || working ||
		surface == "" || rotationOff(getenv) || inStandby(getenv, sid) || !validMarkerKey(sid) {
		return
	}
	if wt := rotWaitType(getenv, sid); slices.Contains(aheadSkipWaits, wt) || slices.Contains(aheadLimitWaits, wt) {
		return
	}
	if _, err := os.Stat(filepath.Join(inboxMarkerDir(getenv), sid)); err == nil {
		return
	}
	now := time.Now()
	if e, ok := claudesession.LastAPIError(in.TranscriptPath); ok && e.Error == "rate_limit" &&
		e.ResetsAt.After(now) && !e.At.Before(turn.Start) {
		return // the limit mode's (StopFailure) business
	}
	if stampWithin(rotateAheadPath(sid), rotateOncePer, now) || stampWithin(rotateLastPath(sid), rotateOncePer, now) ||
		stampWithin(rotateAheadRefusedPath(sid), rotateAheadRefusedFor, now) {
		return
	}

	tbl, cell, snap, th, err := aheadOwn(getenv)
	if err != nil {
		return
	}
	window, pct, over := claudeusage.SnapshotOverSwitch(snap, th, now)
	if !over {
		return
	}
	ch, acct, err := aheadPick(tbl, cell, now)
	if err != nil {
		return
	}
	log := hookLogger{hook: "rotate", getenv: getenv, now: time.Now, sid: sid, cwd: in.Cwd}
	detail := fmt.Sprintf("window=%s pct=%s switch=%s", window, fmtPct(pct), fmtPct(th.SwitchAt()))
	target, why := claudeusage.AheadTarget(ch, acct)
	if target == nil {
		// The account is over its switch (own reading) yet no target: said once
		// per backoff window (M1), whatever the reason; the stamp stops the next
		// Stops before any reading.
		if err := writeAheadRefused(sid, now); err != nil {
			detail += " stamp=failed: " + err.Error()
		}
		log.write("ahead-held", "reason="+why+" "+detail)
		return
	}
	st, err := os.Stat(in.TranscriptPath)
	if err != nil {
		return
	}
	if err := startRotationAhead(sid, surface, os.Getpid(), turn.ID, st.Size()); err != nil {
		log.write("ahead-start-failed", err.Error())
		return
	}
	log.write("ahead-started", fmt.Sprintf("%s to=%s ms=%d", detail, target.Label, time.Since(now).Milliseconds()))
}
