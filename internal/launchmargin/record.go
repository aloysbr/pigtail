package launchmargin

import (
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// JournalKind is the journal kind of a launch margin row
// (pigtail journal list --kind launch-margin).
const JournalKind = "launch-margin"

// Sinks are where a decision is traced: the spawn registry (its own margin
// entry) and the journal. JournalScope is the row's scope: "" on the CLI (the
// caller's scope), the requested cell's on the daemon.
type Sinks struct {
	Spawns       *spawnreg.Registry
	Journal      func(*pigtail.JournalEntry) error
	Logf         func(string, ...any)
	JournalScope string
}

// Record traces a switched, refused, forced or unmeasured decision: one
// margin entry in spawns.jsonl and one journal row. ok writes nothing. The
// texts are the Decision's, copied (bounded at the source, D10). A trace that
// fails goes to Logf, never blocks the launch.
func Record(s Sinks, d Decision) {
	if d.Action == "" || d.Action == ActionOK {
		return
	}
	at := d.At.UTC()
	if s.Spawns != nil {
		e := spawnreg.Entry{TS: at, Kind: spawnreg.KindMargin, SubjectID: d.SubjectID,
			Cell: d.Requested.ID, Margin: d.Margin, Result: d.Action}
		if d.Final.ID != "" && d.Final.ID != d.Requested.ID {
			e.FreshCell = d.Final.ID
		}
		if err := s.Spawns.Append(e); err != nil {
			s.logf("launch margin: spawn registry: %v", err)
		}
	}
	if s.Journal != nil {
		req := d.Requested.ID
		if req == "" {
			req = "-"
		}
		e := &pigtail.JournalEntry{ID: "launch-margin:" + at.Format("20060102T150405.000000000") + ":" + req,
			Timestamp: at, Kind: JournalKind, Scope: s.JournalScope,
			Summary: clip(d.Summary), Source: "pigtail:spawns"}
		if err := s.Journal(e); err != nil {
			s.logf("launch margin: journal: %v", err)
		}
	}
}

func (s Sinks) logf(format string, a ...any) {
	if s.Logf != nil {
		s.Logf(format, a...)
	}
}
