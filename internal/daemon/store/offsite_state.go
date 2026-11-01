package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// `pigtail db restore` and `db list` run with the daemon stopped — that is what
// the command group is for — so they cannot ask the daemon how the offsite copy
// is doing. The daemon leaves the answer in a file beside the database instead,
// on the model of rammon-state.json.
//
// This matters more than it looks: without it, a NAS unplugged three weeks ago
// is one log line an hour that nobody reads, while `db list` keeps showing
// three healthy generations as though everything were protected. The whole
// point of the offsite copy is a guarantee, and a guarantee nobody can check is
// back to being an intention.
//
// It is a report, not a record. Every read failure degrades to "not
// configured" rather than to an error: nothing about a missing or corrupt
// report should make it harder to look at a database during an incident.

const offsiteStateVersion = 1

const offsiteStateFileName = "offsite-state.json"

// OffsiteState is the last thing the daemon knows about the offsite copy.
type OffsiteState struct {
	Version int    `json:"version"`
	DestDir string `json:"dest_dir"`
	// LastOKAt is when a copy last landed. Zero means never — which, with a
	// non-empty DestDir, is itself worth showing.
	LastOKAt time.Time `json:"last_ok_at"`
	// LastTryAt is when the daemon last attempted one, successful or not.
	LastTryAt time.Time `json:"last_try_at"`
	// LastErr is the most recent failure, "" when the last attempt succeeded.
	// Kept verbatim so "unreachable" (a laptop away from its NAS) stays
	// distinguishable from a write that actually failed.
	LastErr string `json:"last_err,omitempty"`
	// SameDevice records that the destination shares a filesystem with the
	// database. Not a refusal — see store.SameDevice — but the copy then
	// survives nothing the local generations don't, which is worth saying.
	SameDevice bool `json:"same_device,omitempty"`
	// AttemptStartedAt is set while a copy is in flight and cleared when it
	// finishes, either way.
	//
	// It exists because of what happened on the first real use of the offsite
	// copy: the state was only ever written *after* the copy
	// returned, so a copy that never returned reported nothing at all — no
	// error, no file, and therefore no row in `pigtail db list`, which kept
	// showing three healthy local generations as though everything were fine.
	// Silence was indistinguishable from health. Recording the attempt before
	// running it is what makes a stuck copy a visible fact rather than an
	// absence.
	AttemptStartedAt time.Time `json:"attempt_started_at,omitempty"`
	// ConsecutiveFailures counts attempts that failed back-to-back, reset to
	// zero by any success. It replaces offsiteBackupper.failing, an in-RAM
	// bool that the daemon carried before this field existed: a restart during
	// an outage erased it, silently dropping the alarm the outage deserved.
	// Persisting it here is what a restart mid-outage no longer loses.
	ConsecutiveFailures int `json:"consecutive_failures,omitempty"`

	// DeclaredAt is when the daemon first learned it should back up to
	// DestDir: set once, by declare(), and carried forward across restarts
	// as long as DestDir does not change. attempt() does not own it — it is
	// the structural guard on attempt()'s carry-forward discipline (see
	// TestAttemptCarriesForwardFieldsItDoesNotOwn), the same role the two
	// scaffolding fields task 2 added played before this one existed.
	//
	// It exists to give no_attempt and never_succeeded a real age to
	// escalate on: neither carries a last_ok_at (there has never been a
	// successful copy), so the 26h "no success in a while" rule has nothing
	// to measure for them without it. Omitted entirely on a state file
	// written before this field existed, which is what lets
	// classifyOffsiteSeverity tell "never declared" apart from "declared
	// just now" instead of inventing an age.
	DeclaredAt time.Time `json:"declared_at,omitempty"`

	// The six fields below are the fallback exercise's own report, kept on
	// this same struct rather than a second file: they are written by the
	// same updateState read-modify-write cycle as everything above (task 8),
	// and a second file would be a second place to get that cycle wrong.
	// ClassifyOffsiteFallback (offsite_fallback.go) is their one reader.

	// FallbackDest is the pinned SSH alias the exercise probes — typically
	// the same [user@]host:/path as DestDir, but through a ~/.ssh/config
	// Host that is not allowed to fall back to the LAN. Empty means no
	// exercise is configured, mirroring DestDir/Configured() above.
	FallbackDest string `json:"fallback_dest,omitempty"`
	// FallbackLastOKAt is when the fallback route last answered. Zero means
	// never — including "configured, but no exercise has landed yet".
	FallbackLastOKAt time.Time `json:"fallback_last_ok_at,omitempty"`
	// FallbackLastTryAt is when the exercise was last attempted, successful
	// or not. Zero means the fallback has never been exercised at all,
	// which is its own status (not_yet_exercised) rather than a failure.
	FallbackLastTryAt time.Time `json:"fallback_last_try_at,omitempty"`
	// FallbackLastErr is the most recent exercise failure, "" when the last
	// attempt succeeded. Kept verbatim, on the same model as LastErr.
	FallbackLastErr string `json:"fallback_last_err,omitempty"`
	// FallbackConsecutiveFailures counts exercises that failed back-to-back,
	// reset to zero by any success — the fallback's own ConsecutiveFailures,
	// counted independently of the primary route's.
	FallbackConsecutiveFailures int `json:"fallback_consecutive_failures,omitempty"`
	// FallbackVia is the source address the destination reported seeing on
	// the last successful exercise (task 7's probe): a discriminant between
	// the pinned route and an LAN fallback, not a value to alarm on by
	// itself. On a host running a VPN in userspace networking, a probe
	// that genuinely went over the pinned VPN alias reports
	// "127.0.0.1" — the connection arrives relayed through the loopback
	// interface, because the VPN address is never bound to a kernel
	// interface. That is the *healthy* reading for such a host, not a bug:
	// see docs/wire-protocol.md's fallback section before "fixing" it.
	FallbackVia string `json:"fallback_via,omitempty"`
}

// Attempting reports whether a copy was in flight when this state was written.
//
// A stale true is possible — the daemon was killed mid-copy — which is why the
// daemon clears it when it declares its destination at startup: whatever
// goroutine it described died with the previous process.
func (s OffsiteState) Attempting() bool { return !s.AttemptStartedAt.IsZero() }

// Configured reports whether an offsite destination is set at all. Callers use
// it to decide whether to show a row: no destination, nothing to report.
func (s OffsiteState) Configured() bool { return s.DestDir != "" }

// OffsiteStatePath returns the state file path for a daemon whose database is
// dbPath. Empty for an unnamed or in-memory database, which has no home to
// write beside.
func OffsiteStatePath(dbPath string) string {
	if !isFileBacked(dbPath) {
		return ""
	}
	return filepath.Join(filepath.Dir(dbPath), offsiteStateFileName)
}

// WriteOffsiteState persists the report. A database with no home on disk is a
// silent no-op rather than an error: an ephemeral daemon has nowhere to put it
// and nothing that would read it.
//
// The write lands through a temp file and a rename, on the model of
// copyAtomic in offsite.go, rather than in place with os.WriteFile: WriteFile
// truncates before writing, and this file is read far more often than it is
// written (every `db list`, every daemon declare()), so a reader landing in
// that truncation window sees an empty file — which ReadOffsiteState turns
// into the zero value, which Configured() reads as "no destination". Losing
// the file outright degrades the same way and is fine, on purpose: this is a
// report a restart regenerates, not a record. But truncation-then-write
// produces that same "nothing to say" state while the report is actually
// mid-write, which is a lie, not a degradation — and downstream it resets
// ConsecutiveFailures, the exact alarm this file exists to carry. The rename
// itself is not fsynced to its directory for the same reason: nothing here
// needs to survive a crash, only to never appear half-written.
func WriteOffsiteState(dbPath string, s OffsiteState) error {
	path := OffsiteStatePath(dbPath)
	if path == "" {
		return nil
	}
	s.Version = offsiteStateVersion
	body, err := json.Marshal(s)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), offsiteStateFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create a temporary state file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write the state file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("flush the state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close the state file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("set the state file's mode: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("publish the state file: %w", err)
	}
	return nil
}

// ReadOffsiteState loads the report, or the zero value when there is none, it
// cannot be read, it is not valid JSON, or it was written by another version.
// It never returns an error: an unreadable report is "nothing to say", and the
// caller has no better move than showing that.
func ReadOffsiteState(dbPath string) OffsiteState {
	path := OffsiteStatePath(dbPath)
	if path == "" {
		return OffsiteState{}
	}
	body, err := os.ReadFile(path) //nolint:gosec // path is derived from the daemon's own database path
	if err != nil {
		return OffsiteState{}
	}
	var s OffsiteState
	if err := json.Unmarshal(body, &s); err != nil {
		return OffsiteState{}
	}
	if s.Version != offsiteStateVersion {
		return OffsiteState{}
	}
	return s
}
