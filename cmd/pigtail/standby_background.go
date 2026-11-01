package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/bgwork"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/setup"
)

// /standby refuses while background work runs (D25): its end would relaunch
// the agent, and no pigtail automaton can stop that. The sources (D42, D44,
// P17 in internal/cmux/testdata/PROBE.md):
//
//   - the session_crons list of the session's last Stop (crons and wakeups,
//     which nothing else sees), recorded by `pigtail hook claude-wait` in
//     bgwork/<session> next to the socket. The payload's background_tasks
//     is never kept: it also lists ambient tasks (the artifact monitor_ws
//     watcher, anthropics/claude-code#98816), which would refuse forever;
//   - the transcript's pending background tasks and agents;
//   - the Bash-tool shells of the session, minus the command's own ancestors;
//   - a background row on screen — a "busy" source only, never a "free" proof.
//
// The record is trusted only for this process (its pid) and the turn just
// before /standby; anything else, and any unreadable reading, refuses D33.

// bgCron is one entry of the Stop payload's session_crons, its prompt left out.
type bgCron struct {
	ID        string `json:"id"`
	Schedule  string `json:"schedule"`
	Recurring bool   `json:"recurring"`
}

// String is the cron's part of the D29 refusal: harness text, cleaned of
// control characters and quotes.
func (c bgCron) String() string {
	return "cron " + cleanHarnessText(c.ID) + " (" + cleanHarnessText(c.Schedule) + ")"
}

func cleanHarnessText(s string) string { return bgwork.CleanText(s) }

// bgRecord is bgwork/<session>: what the last main-session Stop listed.
// Crons nil = the payload carried no session_crons (≠ an empty list).
type bgRecord struct {
	SessionID string    `json:"session_id"`
	At        int64     `json:"at"`                   // ms
	PID       int       `json:"pid"`                  // the Claude process that ended the turn; 0 unknown
	Turn      string    `json:"turn"`                 // the promptId of the turn that ended; "" unknown
	TurnError string    `json:"turn_error,omitempty"` // why Turn is "" (claudesession.ReadLastTurn)
	Crons     *[]bgCron `json:"session_crons"`
}

var (
	errNoBgRecord    = errors.New("aucune fin de tour enregistrée (premier tour de la session, ou hook Stop de pigtail absent)")
	errBgUnreadable  = errors.New("liste des crons illisible")
	errStaleBgRecord = errors.New("dernière fin de tour enregistrée pour un autre tour ou un autre processus")
	errNoBgLists     = errors.New("le Stop ne porte pas la liste des crons")
	errBgTurnUnknown = errors.New("la dernière fin de tour n'a pas pu lire son tour au transcript")
)

// bgWorkDir holds the Stop records: PIGTAIL_BGWORK_DIR, else next to the
// socket, as waitMarkerDir.
func bgWorkDir(getenv func(string) string) string {
	if d := getenv("PIGTAIL_BGWORK_DIR"); d != "" {
		return d
	}
	if s := socketPath(); s != "" {
		return filepath.Join(filepath.Dir(s), "bgwork")
	}
	return filepath.Join(setup.ConfigDir(), "bgwork")
}

// recordStopBackground writes the session's record from a Stop payload,
// atomically (temp file + rename, 0600). Only session_crons is kept, without
// its prompts; an absent field is recorded as absent.
// turnErr is why turn is "" (recorded, so /standby names it).
func recordStopBackground(dir string, raw []byte, session string, pid int, turn string, turnErr error, now time.Time) error {
	if !validMarkerKey(session) {
		return fmt.Errorf("bad session id %q", session)
	}
	var in struct {
		Crons json.RawMessage `json:"session_crons"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return err
	}
	rec := bgRecord{SessionID: session, At: now.UnixMilli(), PID: pid, Turn: turn}
	if turn == "" && turnErr != nil {
		rec.TurnError = turnErr.Error()
	}
	if len(in.Crons) > 0 && string(in.Crons) != "null" {
		var cs []bgCron
		if json.Unmarshal(in.Crons, &cs) == nil {
			if cs == nil {
				cs = []bgCron{}
			}
			rec.Crons = &cs
		}
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+session+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, session)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// removeBgRecord drops the session's record (SessionEnd). Best effort.
func removeBgRecord(dir, session string) {
	if validMarkerKey(session) {
		_ = os.Remove(filepath.Join(dir, session))
	}
}

// readBgRecord returns the session's record when it was written by this
// Claude process (pid) at the end of the turn just before this one
// (prevTurn); every other state is one of the named errors.
func readBgRecord(dir, session string, pid int, prevTurn string) (bgRecord, error) {
	if !validMarkerKey(session) {
		return bgRecord{}, errBgUnreadable
	}
	b, err := os.ReadFile(filepath.Join(dir, session))
	if errors.Is(err, fs.ErrNotExist) {
		return bgRecord{}, errNoBgRecord
	}
	if err != nil {
		return bgRecord{}, errBgUnreadable
	}
	var r bgRecord
	if json.Unmarshal(b, &r) != nil {
		return bgRecord{}, errBgUnreadable
	}
	if r.Turn == "" && (r.SessionID == "" || r.SessionID == session) {
		// Never read as "another turn": the Stop says why it had none.
		why := r.TurnError
		if why == "" {
			why = "raison non enregistrée"
		}
		return bgRecord{}, fmt.Errorf("%w (%s)", errBgTurnUnknown, cleanHarnessText(why))
	}
	if r.PID == 0 || r.PID != pid || r.Turn != prevTurn || (r.SessionID != "" && r.SessionID != session) {
		return bgRecord{}, errStaleBgRecord
	}
	if r.Crons == nil {
		return bgRecord{}, errNoBgLists
	}
	return r, nil
}

// standbyBgDeps are the readings of standbyBackgroundWork (seams).
type standbyBgDeps struct {
	Caller     func() (int, bool, error)                  // the calling Claude process
	Native     func(pid int) (claudesession.Native, bool) // its native state (StartedAt)
	Transcript func(session string) (string, bool, error) // the session's transcript path
	PrevTurn   func(path string) (string, bool)           // the turn before this one
	Record     func(session string, pid int, prevTurn string) (bgRecord, error)
	Pending    func(path string, since time.Time) ([]string, error)
	Ancestors  func() ([]int, error) // this command's parents
	Shells     func(pid int) ([]int, error)
	Screen     func() (string, error)
}

// standbyBgDepsFor wires the real readings; tests replace it.
var standbyBgDepsFor = func(getenv func(string) string) standbyBgDeps {
	cfg := claudeConfigDir(getenv)
	return standbyBgDeps{
		Caller: func() (int, bool, error) {
			p, ok, err := claudesession.CallerClaude(os.Getpid())
			return p.PID, ok, err
		},
		Native: func(pid int) (claudesession.Native, bool) { return claudesession.ReadNative(cfg, pid) },
		Transcript: func(session string) (string, bool, error) {
			tr, ok, err := claudesession.Find(session, []string{cfg})
			if err != nil || !ok {
				return "", ok, err
			}
			return tr.JSONLPath(), true, nil
		},
		PrevTurn: claudesession.PreviousTurnID,
		Record: func(session string, pid int, prev string) (bgRecord, error) {
			return readBgRecord(bgWorkDir(getenv), session, pid, prev)
		},
		Pending:   claudesession.PendingBackground,
		Ancestors: func() ([]int, error) { return claudesession.Ancestors(os.Getpid()) },
		Shells:    claudesession.BashToolShells,
		Screen:    standbyScreenReader(getenv),
	}
}

// standbyScreenReader reads the session's cmux surface, bounded; an empty or
// invalid CMUX_SURFACE_ID is an error (checkUUID), never a quiet screen.
func standbyScreenReader(getenv func(string) string) func() (string, error) {
	return func() (string, error) { return cmux.ReadScreenBounded(getenv("CMUX_SURFACE_ID")) }
}

// standbyBackgroundWork lists the background work the readings see. No work
// and an unreadable reading: the reasons, joined, as the error. A reading
// whose input failed is skipped: only the cause is named. The readings other
// than the cron record live in internal/bgwork (shared with pigtaild's pause).
func standbyBackgroundWork(d standbyBgDeps, session string) ([]string, error) {
	pid, havePid, err := d.Caller()
	if err != nil || !havePid {
		pid = 0
		havePid = false
	}
	r := bgwork.Read(bgwork.Deps{
		Native:     d.Native,
		Transcript: d.Transcript,
		Pending:    d.Pending,
		Ancestors:  d.Ancestors,
		Shells:     d.Shells,
		Screen:     d.Screen,
		AfterTranscript: func(path string, haveTr bool) (work, reasons []string) {
			prev, havePrev := "", false
			if haveTr {
				if prev, havePrev = d.PrevTurn(path); !havePrev {
					reasons = append(reasons, "tour précédent introuvable au transcript")
				}
			}
			if havePid && havePrev {
				if rec, err := d.Record(session, pid, prev); err != nil {
					reasons = append(reasons, err.Error())
				} else {
					for _, c := range *rec.Crons {
						work = append(work, c.String())
					}
				}
			}
			return work, reasons
		},
	}, session, pid)
	if len(r.Work) > 0 {
		return r.Work, nil
	}
	if len(r.Unreadable) > 0 {
		return nil, errors.New(strings.Join(r.Unreadable, " ; "))
	}
	return nil, nil
}
