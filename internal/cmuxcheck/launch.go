package cmuxcheck

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Windows of tolerance, each from a measurement (F2, F3, peers snapshot 30 s).
const (
	cleanQuitGap   = 2 * time.Second // window.closed → last event of a clean quit
	previousSlack  = 5 * time.Second // -previous written up to this before the last event
	previousLate   = 2 * time.Minute // … or up to this after the new launch
	peersFreshness = 2 * time.Minute // a peer seen this close to the end was alive
)

func DefaultSentinel() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "cmux", "lifecycle", "com.cmuxterm.app", "running.sentinel")
}

// DefaultEvents lists the cmux event logs, oldest first.
func DefaultEvents() []string {
	home, _ := os.UserHomeDir()
	d := filepath.Join(home, ".cmuxterm")
	return []string{filepath.Join(d, "events.jsonl.1"), filepath.Join(d, "events.jsonl")}
}

// Boot is one cmux process lifetime, as its event log shows it.
type Boot struct {
	ID       string
	First    time.Time
	Last     time.Time
	LastName string
	ClosedAt time.Time // last window.closed
}

// CleanQuit: the boot ended on its window closing (F3), not on a crash.
func (b Boot) CleanQuit() bool {
	return !b.ClosedAt.IsZero() && b.Last.Sub(b.ClosedAt) <= cleanQuitGap
}

// Launch is the running cmux and the boot before it.
type Launch struct {
	PID      int
	Start    time.Time
	Current  Boot
	Previous *Boot
}

// ReadSentinel parses "pid=<n>".
func ReadSentinel(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(b))
	n, err := strconv.Atoi(strings.TrimPrefix(s, "pid="))
	if err != nil || !strings.HasPrefix(s, "pid=") || n <= 0 {
		return 0, fmt.Errorf("sentinel %s: want pid=<n>, got %q", path, s)
	}
	return n, nil
}

// ReadBoots reads the event logs (oldest first; a missing one is skipped,
// none at all is an error) into boots ordered by their first event. Lines
// that do not parse (a line cut by rotation) are skipped.
func ReadBoots(paths ...string) ([]Boot, error) {
	idx := map[string]int{}
	var boots []Boot
	read := 0
	for _, p := range paths {
		f, err := os.Open(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		read++
		br := bufio.NewReader(f)
		for {
			line, rerr := br.ReadBytes('\n')
			var e struct {
				BootID     string    `json:"boot_id"`
				OccurredAt time.Time `json:"occurred_at"`
				Name       string    `json:"name"`
			}
			if len(line) > 0 && json.Unmarshal(line, &e) == nil && e.BootID != "" {
				i, ok := idx[e.BootID]
				if !ok {
					i = len(boots)
					idx[e.BootID] = i
					boots = append(boots, Boot{ID: e.BootID, First: e.OccurredAt})
				}
				b := &boots[i]
				if e.OccurredAt.Before(b.First) {
					b.First = e.OccurredAt
				}
				if !e.OccurredAt.Before(b.Last) {
					b.Last, b.LastName = e.OccurredAt, e.Name
				}
				if e.Name == "window.closed" && e.OccurredAt.After(b.ClosedAt) {
					b.ClosedAt = e.OccurredAt
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				_ = f.Close()
				return nil, fmt.Errorf("read %s: %w", p, rerr)
			}
		}
		_ = f.Close()
	}
	if read == 0 {
		return nil, fmt.Errorf("no cmux event log among %v", paths)
	}
	sort.SliceStable(boots, func(i, j int) bool { return boots[i].First.Before(boots[j].First) })
	return boots, nil
}

// DetectLaunch names the running cmux (sentinel pid) and its boot (the last
// one in the logs), with the boot before it when the logs still hold it.
func DetectLaunch(sentinel string, events []string) (Launch, error) {
	pid, err := ReadSentinel(sentinel)
	if err != nil {
		return Launch{}, err
	}
	boots, err := ReadBoots(events...)
	if err != nil {
		return Launch{}, err
	}
	if len(boots) == 0 {
		return Launch{}, fmt.Errorf("cmux event logs hold no boot")
	}
	cur := boots[len(boots)-1]
	l := Launch{PID: pid, Start: cur.First, Current: cur}
	if len(boots) > 1 {
		prev := boots[len(boots)-2]
		l.Previous = &prev
	}
	return l, nil
}

// ChooseBefore picks the photo of the sessions before the relaunch. The
// -previous file counts when its mtime belongs to this relaunch (F2). After
// a crash its content is not proven (H1): the pigtail peers alive at the
// end of the previous boot complete it — or replace it when stale.
func ChooseBefore(l Launch, prev Photo, prevErr error, peers []pigtail.PeerSession) (Photo, []string) {
	var notes []string
	if l.Previous == nil {
		if prevErr != nil {
			return Photo{}, []string{"no earlier cmux launch in the event logs and no -previous file: " + prevErr.Error()}
		}
		return prev, []string{"no earlier cmux launch in the event logs: -previous taken as is"}
	}
	fresh := prevErr == nil &&
		!prev.TakenAt.Before(l.Previous.Last.Add(-previousSlack)) &&
		!prev.TakenAt.After(l.Start.Add(previousLate))
	if fresh && l.Previous.CleanQuit() {
		return prev, nil
	}
	out := Photo{Source: "peers", TakenAt: l.Previous.Last}
	if fresh {
		out = prev
		out.Source = "previous+peers"
	}
	if !l.Previous.CleanQuit() {
		notes = append(notes, "cmux ended without a clean quit: the -previous file is not proven after a crash (H1), the pigtail peers snapshot completes it")
	} else {
		notes = append(notes, "the -previous file belongs to another launch: photo rebuilt from the pigtail peers snapshot")
	}
	for _, p := range peers {
		if p.CmuxSurfaceID == "" || p.SessionID == "" || p.UpdatedAt.Before(l.Previous.Last.Add(-peersFreshness)) {
			continue
		}
		if p.EndedAt != nil && p.EndedAt.Before(l.Previous.Last.Add(-peersFreshness)) {
			continue
		}
		if _, ok := out.Panel(p.CmuxSurfaceID); ok {
			continue
		}
		out.Panels = append(out.Panels, Panel{SurfaceID: p.CmuxSurfaceID, WorkspaceID: p.CmuxWorkspaceID, WorkspaceTitle: p.CmuxTitle, // the peer's workspace title (D23)

			Binding: &Binding{AutoResume: true, CheckpointID: p.SessionID, Cwd: p.Cwd, Env: map[string]string{"CLAUDE_CONFIG_DIR": p.ConfigDir}}})
	}
	return out, notes
}
