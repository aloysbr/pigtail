// Package bgwork reads the background work of a Claude session that the
// native status does not show: the
// transcript's pending background tasks and agents, the Bash-tool shells of
// the process (Monitors included) minus the reader's own ancestors, and a
// background row on screen. Shared by `pigtail session standby` (which adds
// the session_crons record it alone keeps) and pigtaild's idle pause.
//
// Every reading that cannot be made is named: a caller refuses on work and
// on an unreadable reading alike (fail closed), never reads it as "free".
package bgwork

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/aloysbr/pigtail/internal/claudesession"
)

// Deps are the readings (seams).
type Deps struct {
	Native     func(pid int) (claudesession.Native, bool) // StartedAt bounds the transcript scan
	Transcript func(session string) (string, bool, error) // the session's transcript path
	Pending    func(path string, since time.Time) ([]string, error)
	Ancestors  func() ([]int, error) // the reader's own parents, excluded from the shells
	Shells     func(pid int) ([]int, error)
	Screen     func() (string, error)

	// AfterTranscript, optional, runs right after the transcript lookup
	// (the CLI reads its cron record there): its work comes first, its
	// reasons after the transcript's.
	AfterTranscript func(path string, ok bool) (work, reasons []string)
}

// Reading is what the readings saw: Work the background work found,
// Unreadable the readings that could not be made (deduplicated, in order).
type Reading struct {
	Work       []string
	Unreadable []string
}

// Read runs every reading for session and its Claude pid (pid <= 0 = the
// process is unknown: the pid-bound readings are skipped and named). A
// reading whose input failed is skipped: only the cause is named.
func Read(d Deps, session string, pid int) Reading {
	var work, reasons []string
	havePid := pid > 0
	if !havePid {
		reasons = append(reasons, "processus Claude appelant introuvable")
	}
	var native claudesession.Native
	haveNative := false
	if havePid {
		if native, haveNative = d.Native(pid); !haveNative {
			reasons = append(reasons, "état natif de la session introuvable")
		}
	}
	path, haveTr, err := d.Transcript(session)
	switch {
	case err != nil:
		reasons = append(reasons, "transcript illisible")
		haveTr = false
	case !haveTr:
		reasons = append(reasons, "transcript de la session introuvable")
	}
	if d.AfterTranscript != nil {
		w, r := d.AfterTranscript(path, haveTr)
		work = append(work, w...)
		reasons = append(reasons, r...)
	}
	if haveTr && haveNative {
		if ids, err := d.Pending(path, time.UnixMilli(native.StartedAt)); err != nil {
			reasons = append(reasons, "transcript illisible")
		} else {
			for _, id := range ids {
				work = append(work, "tâche "+CleanText(id))
			}
		}
	}
	anc, err := d.Ancestors()
	if err != nil {
		reasons = append(reasons, "processus parents illisibles")
	}
	if havePid && err == nil {
		mine := map[int]bool{}
		for _, a := range anc {
			mine[a] = true
		}
		if sh, err := d.Shells(pid); err != nil {
			reasons = append(reasons, "shells Bash illisibles")
		} else {
			for _, p := range sh {
				if !mine[p] {
					work = append(work, fmt.Sprintf("shell Bash %d", p))
				}
			}
		}
	}
	if screen, err := d.Screen(); err != nil {
		reasons = append(reasons, "écran de la session illisible")
	} else if claudesession.BackgroundWorkShown(screen) {
		work = append(work, "tâche de fond affichée à l'écran")
	}
	return Reading{Work: work, Unreadable: dedupe(reasons)}
}

// Work is Read for a caller that refuses on either: the work found, and an
// error naming every unreadable reading (nil only when all were read).
func Work(d Deps, session string, pid int) ([]string, error) {
	r := Read(d, session, pid)
	if len(r.Unreadable) > 0 {
		return r.Work, errors.New(strings.Join(r.Unreadable, " ; "))
	}
	return r.Work, nil
}

// CleanText drops control characters and quotes from harness text and
// folds its blanks.
func CleanText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '"' {
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
