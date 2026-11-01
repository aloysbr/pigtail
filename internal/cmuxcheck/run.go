package cmuxcheck

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Report is one check run.
type Report struct {
	At          time.Time `json:"at"`
	Fix         bool      `json:"fix"`
	Launch      Launch    `json:"launch"`
	PhotoSource string    `json:"photo_source"`
	Notes       []string  `json:"notes,omitempty"`
	Info        []string  `json:"info,omitempty"` // machine-wide facts, filled by the CLI
	Rows        []Row     `json:"rows"`
	Fatal       string    `json:"fatal,omitempty"`
	// Partial: this pass looked at part of the surfaces only (PartialPass,
	// set by the command); SaveLast keeps the other surfaces' rows of the
	// same launch.
	Partial bool `json:"partial,omitempty"`
	// PassScope: the only scope this pass's peers covered (the command's
	// partial), "" for a pass over every scope. Redacted keeps Info and Notes
	// for a reader of that scope only (D18).
	PassScope string `json:"pass_scope,omitempty"`
	// Machine: machine-wide lines that name no session, title, path or scope
	// (pigtaild unreachable, dev servers, sidebar warnings) — the only lines
	// a reader of another scope's pass keeps (D18).
	Machine []string `json:"machine,omitempty"`
	// LastFailure: a later partial pass of the same launch that could not
	// look (fatal). It did not replace this report (mergePartial); --last
	// names it beside it. Never set on a fresh pass.
	LastFailure *Failure `json:"last_failure,omitempty"`
	// Pilot (D3–D5): the session this pass repairs alone (--fix-pilot) and
	// its message. Printed to the master only: Redacted never copies it.
	Pilot PilotInfo `json:"pilot,omitempty"`
}

// Failure is a fatal pass kept beside the report it did not replace.
type Failure struct {
	At    time.Time `json:"at"`
	Fatal string    `json:"fatal"`
}

// peersScopeNote is the note a photo rebuilt from the pigtail peers carries
// when those peers covered one scope only (Options.PeersScope): the sessions
// of the other scopes are missing from it, and the check cannot tell them
// from sessions that never existed (D7: every account and scope).
func peersScopeNote(source, peersScope string) string {
	if peersScope == "" || !strings.Contains(source, "peers") {
		return ""
	}
	return "photo rebuilt from the pigtail peers of scope " + peersScope + " only: sessions of the other scopes are missing — run from the master cell, or outside any cell with --scope all"
}

// Run is the check: cmux reachable, which launch, which photo before, what
// runs, then (Fix) the repairs one row at a time. Lot 2 calls it at the end
// of `cmux restart` with Options.Before set to the photo it took.
func Run(d Deps, r *Repairer, o Options) Report {
	rep := Report{At: d.Now(), Fix: o.Fix}
	if o.Ping != nil {
		if err := o.Ping(); err != nil {
			rep.Fatal = "cmux unreachable (socket): " + err.Error()
			return rep
		}
	}
	l, err := DetectLaunch(o.Sentinel, o.Events)
	if err != nil {
		rep.Fatal = "cmux launch unknown: " + err.Error()
		return rep
	}
	rep.Launch = l
	now, err := LoadPhoto(filepath.Join(o.Dir, CurrentFile))
	if err != nil {
		rep.Fatal = "current cmux session file unreadable: " + err.Error()
		return rep
	}
	var before Photo
	if o.Before != nil {
		before = *o.Before
	} else {
		prev, perr := LoadPhoto(filepath.Join(o.Dir, PreviousFile))
		var peers []pigtail.PeerSession
		if o.Peers != nil {
			ps, err := o.Peers()
			if err != nil {
				rep.Notes = append(rep.Notes, "pigtail peers unreadable: "+err.Error())
			}
			peers = ps
		}
		var notes []string
		before, notes = ChooseBefore(l, prev, perr, peers)
		rep.Notes = append(rep.Notes, notes...)
		if n := peersScopeNote(before.Source, o.PeersScope); n != "" {
			rep.Notes = append(rep.Notes, n)
		}
	}
	rep.PhotoSource = before.Source
	// No photo before: the relaunch repairs cannot be planned; the input
	// probe still runs (sub_x000000000000024), it needs no photo before.
	probeOnly := len(before.Panels) == 0
	if probeOnly && !o.Probe {
		rep.Fatal = "no photo of the sessions before the relaunch"
		return rep
	}
	if probeOnly {
		rep.Notes = append(rep.Notes, noteProbeOnly)
	}
	if bad := unknownSurfaces(o, before, now); len(bad) > 0 {
		rep.Fatal = "--surface " + strings.Join(bad, ", ") + ": in neither the photo before nor the current one"
		return rep
	}
	pilot, source, pnotes := resolvePilot(o, before, now, l)
	o.Pilot = pilot
	rep.Pilot = PilotInfo{SurfaceID: pilot, Source: source}
	rep.Notes = append(rep.Notes, pnotes...)
	if o.PilotOnly && pilot == "" {
		rep.Notes = append(rep.Notes, "no pilot: nothing repaired, report only")
	}
	live, err := Live(d)
	if err != nil {
		rep.Fatal = "process listing: " + err.Error()
		return rep
	}
	if !probeOnly {
		rep.Rows = Classify(before, now, live, l, o, d)
	}
	rep.Rows = probeInput(rep.Rows, before, now, live, l, o, d)
	// T-i: the pilot message goes once per launch — an earlier pass of this
	// launch typed it (pilot-msg.json, revue PR B I2): the pilot's continue is
	// the plain one (D2), whatever surface this pass resolves.
	already := o.PilotMsg != nil && o.PilotMsg.For(l)
	if already && pilot != "" {
		rep.Pilot.Message, rep.Pilot.MessageTyped = "already typed by an earlier pass", true
	}
	// D13 (revue PR B I3): only a pass pigtaild starts (--unattended) types
	// the pilot message — a human's or an agent's --fix repairs the pilot
	// but never types it, hours after the relaunch it was meant for.
	message := pilot != "" && !already && o.Unattended
	if o.Fix {
		r.CmuxPID = l.PID
		if message {
			r.Pilot = pilot
		}
		for i := range rep.Rows {
			isPilot := pilot != "" && sameSurface(rep.Rows[i].SurfaceID, pilot)
			if o.PilotOnly && !isPilot {
				continue // D3: report only
			}
			if o.Heartbeat != nil {
				o.Heartbeat()
			}
			r.Fix(&rep.Rows[i])
			if isPilot && strings.Contains(rep.Rows[i].Blocked, "busy: "+focusReason) {
				rep.Notes = append(rep.Notes, focusNote(o)) // D9
			}
		}
		switch {
		case message: // D5: the message to the pilot, after its repairs
			for i := range rep.Rows {
				if sameSurface(rep.Rows[i].SurfaceID, pilot) {
					rep.Pilot.Message, rep.Pilot.MessageTyped, rep.Pilot.MessagePending = r.PilotMessage(&rep.Rows[i])
					break
				}
			}
			if rep.Pilot.Message == "" {
				rep.Pilot.Message = "not typed: no row for the pilot surface"
			}
		case pilot != "" && !already:
			rep.Pilot.Message = "not typed: only a pass pigtaild starts types it (D13)"
		}
	}
	return rep
}

// focusNote (D9): the pilot's repair waits for the keyboard focus. Only a
// first pigtaild pass (--unattended, no --surface) is followed by a second
// one (D6); any other pass claims no retry (revue PR B M1).
func focusNote(o Options) string {
	const n = "pilot holds keyboard focus: click another pane"
	if o.Unattended && len(o.Surfaces) == 0 {
		return n + " — the second pass retries in " + SecondPassDelay.String()
	}
	return n
}
