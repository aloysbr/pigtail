// Package tmpsweep removes the temporary directories pigtail's own tests left
// behind (a test killed before its cleanup, an old build of the helper). On
// 2026-10-31 such leftovers (87 + 69 GB, read-only module caches) filled the
// disk. The fix is at the source (testutil.MkdirTemp, hermeticEnv); this is
// the net under it.
//
// It touches only directories directly under os.TempDir() and /tmp that
// belong to the current user, whose name is a known test prefix followed by
// digits, and whose whole tree has been idle for MinAge. It never follows a
// link and changes the mode of nothing outside the tree it deletes.
package tmpsweep

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/aloysbr/pigtail/internal/fsutil"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Mode is what the Sweeper does with a leftover.
type Mode string

const (
	ModeOn      Mode = "on"      // delete, journal tmp-swept
	ModeObserve Mode = "observe" // journal tmp-would-sweep, never delete
	ModeOff     Mode = "off"     // the daemon runs no Sweeper
)

// ParseMode reads a -tmp-sweep value.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case ModeOn, ModeObserve, ModeOff:
		return m, nil
	}
	return "", fmt.Errorf("tmp sweep mode %q: want on, observe or off", s)
}

const (
	// DefaultMinAge is how long a whole tree must have been idle.
	DefaultMinAge = 2 * time.Hour
	// DefaultInterval paces the sweeps; the first one comes after it, never
	// at start.
	DefaultInterval = 10 * time.Minute
)

// legacyPrefixes are the names tests used before testutil.MkdirTemp: frozen,
// matched only as prefix + digits (the suffix os.MkdirTemp adds). Generic
// prefixes (pa, lp, h2-…) are never swept.
var legacyPrefixes = []string{
	"pigtail-cli-test-", "pgt-home", "pgt-md", "pgt-xs", "pgt-xc", "pgt-state", "pigtail-state-test-",
}

var (
	currentRE = regexp.MustCompile(`^` + regexp.QuoteMeta(fsutil.TestTempPrefix) + `[a-z0-9-]*[0-9]+$`)
	legacyRE  = func() *regexp.Regexp {
		alt := make([]string, 0, len(legacyPrefixes)+1)
		for _, p := range legacyPrefixes {
			alt = append(alt, regexp.QuoteMeta(p))
		}
		alt = append(alt, "pgt") // socketpath_test.go: pgt + digits
		return regexp.MustCompile(`^(?:` + strings.Join(alt, "|") + `)[0-9]+$`)
	}()
)

// Matches reports whether a directory name is one a pigtail test makes.
func Matches(name string) bool { return currentRE.MatchString(name) || legacyRE.MatchString(name) }

// Sweeper is the periodic cleaner. Build it with New; the fields are seams.
type Sweeper struct {
	Mode     Mode
	Dirs     []string
	MinAge   time.Duration
	Interval time.Duration

	Now     func() time.Time
	Journal func(pigtail.JournalEntry) error // nil: no journal
	Logf    func(format string, args ...any)
	Remove  func(dir string) error
	Newest  func(dir string) (time.Time, int64, error)
	OwnedBy func(info os.FileInfo) bool

	lastWould string // fingerprint of the last candidate set journaled in observe
}

// New returns a Sweeper over os.TempDir() and /tmp.
func New(mode Mode) *Sweeper {
	uid := uint32(os.Getuid()) //nolint:gosec // a uid is small and non-negative
	return &Sweeper{
		Mode:     mode,
		Dirs:     []string{os.TempDir(), "/tmp"},
		MinAge:   DefaultMinAge,
		Interval: DefaultInterval,
		Now:      time.Now,
		Logf:     log.Printf,
		Remove:   fsutil.RemoveTree,
		Newest:   fsutil.NewestMTime,
		OwnedBy: func(info os.FileInfo) bool {
			st, ok := info.Sys().(*syscall.Stat_t)
			return ok && st.Uid == uid
		},
	}
}

// Run logs its start, then sweeps every Interval until ctx is done. The first
// sweep comes after one Interval, never at start.
func (s *Sweeper) Run(ctx context.Context) {
	s.Logf("tmp sweeper: mode %s, removes test leftovers idle for %s, sweeps every %s", s.Mode, s.MinAge, s.Interval)
	if ctx.Err() != nil || s.Mode == ModeOff {
		return
	}
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick(s.Now())
		}
	}
}

type candidate struct {
	path  string
	age   time.Duration
	bytes int64
}

// dirs returns Dirs deduplicated after EvalSymlinks.
func (s *Sweeper) dirs() []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range s.Dirs {
		r, err := filepath.EvalSymlinks(d)
		if err != nil || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

// Tick runs one sweep at now.
func (s *Sweeper) Tick(now time.Time) {
	if s.Mode == ModeOff {
		return
	}
	var cands []candidate
	for _, dir := range s.dirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			s.Logf("tmp sweeper: read %s: %v", dir, err)
			continue
		}
		for _, e := range entries {
			if !Matches(e.Name()) {
				continue
			}
			p := filepath.Join(dir, e.Name())
			info, err := os.Lstat(p) // never a link
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !s.OwnedBy(info) {
				continue
			}
			newest, size, err := s.Newest(p)
			if err != nil {
				s.Logf("tmp sweeper: walk %s: %v", p, err)
				continue
			}
			if age := now.Sub(newest); age > s.MinAge {
				cands = append(cands, candidate{p, age, size})
			}
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].path < cands[j].path })
	if len(cands) == 0 {
		s.lastWould = ""
		return
	}
	if s.Mode == ModeObserve {
		s.would(now, cands)
		return
	}
	var n int
	var bytes int64
	var oldest time.Duration
	for _, c := range cands {
		if err := s.Remove(c.path); err != nil {
			s.Logf("tmp sweeper: remove %s: %v", c.path, err)
			continue
		}
		n++
		bytes += c.bytes
		oldest = max(oldest, c.age)
	}
	if n > 0 {
		s.journal("tmp-swept", now, n, bytes, oldest)
	}
}

func (s *Sweeper) would(now time.Time, cands []candidate) {
	var fp strings.Builder
	var bytes int64
	var oldest time.Duration
	for _, c := range cands {
		fp.WriteString(c.path + "\n")
		bytes += c.bytes
		oldest = max(oldest, c.age)
	}
	if fp.String() == s.lastWould {
		return
	}
	s.lastWould = fp.String()
	s.journal("tmp-would-sweep", now, len(cands), bytes, oldest)
}

func (s *Sweeper) journal(kind string, now time.Time, n int, bytes int64, oldest time.Duration) {
	if s.Journal == nil {
		return
	}
	e := pigtail.JournalEntry{
		ID:        fmt.Sprintf("%s:%d", kind, s.Now().UnixNano()),
		Timestamp: now,
		Kind:      kind,
		Scope:     string(scope.Master()),
		Summary:   fmt.Sprintf("n=%d bytes=%d oldest=%s", n, bytes, oldest.Round(time.Minute)),
		Source:    "proc:pigtaild",
	}
	if err := s.Journal(e); err != nil {
		s.Logf("tmp sweeper: journal %s: %v", e.ID, err)
	}
}
