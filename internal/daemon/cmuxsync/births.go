package cmuxsync

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"
)

// The birth clock (D31, D35, sub_x000000000000041): when the loop first saw
// each workspace, kept across a daemon restart in cmux-births.json next to
// the database. A workspace is « young » for Config.BirthGrace after its
// birth: the folder arrangement leaves it where it was born. An id the clock
// does not know is old (the zero time), never young: a lost or unreadable
// file files everything in « Inbox », as before the grace existed.

// LoadBirthsFile reads the birth clock; the boolean says the file exists. An
// absent file is an empty map; an unreadable or invalid one is an error.
func LoadBirthsFile(path string) (map[string]time.Time, bool, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the daemon's own state file
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]time.Time{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var raw map[string]string
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, false, fmt.Errorf("cmux births state %s: %w", path, err)
	}
	out := make(map[string]time.Time, len(raw))
	for id, v := range raw {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return nil, false, fmt.Errorf("cmux births state %s: %s: %w", path, id, err)
		}
		out[strings.ToUpper(id)] = t
	}
	return out, true, nil
}

// BirthsFileModTime is when the file was last written.
func BirthsFileModTime(path string) (time.Time, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}

// SaveBirthsFile writes the birth clock atomically: {"<WS UUID>": "<RFC3339>"},
// the zero time written as is (= old).
func SaveBirthsFile(path string, m map[string]time.Time) error {
	raw := make(map[string]string, len(m))
	for id, t := range m {
		raw[id] = t.UTC().Format(time.RFC3339)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b)
}

// missedTwice counts the consecutive successful listings that lacked an id:
// present resets the count; the second miss in a row says forget. Shared by
// every « absent from two listings » rule.
func missedTwice(present bool, ticks int) (next int, forget bool) {
	if present {
		return 0, false
	}
	next = ticks + 1
	return next, next >= 2
}

// birthsOn: the clock is only kept while the folder arrangement is on.
func (r *Runner) birthsOn() bool { return r.cfg.Folders }

// loadBirths reads the file once. Absent, unreadable or no loader: first-run
// mode, where the first listing that shows an allowed workspace marks all of
// them old (D35).
func (r *Runner) loadBirths() {
	if r.birthsLoaded {
		return
	}
	r.birthsLoaded = true
	r.births = map[string]time.Time{}
	r.birthsMissing = map[string]int{}
	r.birthsFirst = true
	if r.deps.LoadBirths == nil {
		return
	}
	m, present, err := r.deps.LoadBirths()
	if err != nil {
		r.deps.Logf("cmuxsync: births: state unreadable, every workspace reads old: %v", err)
		return
	}
	if !present {
		return
	}
	for k, v := range m {
		r.births[strings.ToUpper(k)] = v
	}
	// The clock only ran while the daemon did: a file nobody wrote for longer
	// than the grace cannot tell the tabs opened since from the old ones, so
	// the unknown ids of the first listing read old (as an absent file).
	r.birthsFirst = false
	if r.deps.BirthsModTime != nil && r.cfg.BirthGrace > 0 {
		if mod, err := r.deps.BirthsModTime(); err == nil && r.deps.Now().Sub(mod) > r.cfg.BirthGrace {
			r.birthsFirst = true
		}
	}
}

// rearmBirths is the clock stopping: the loop was switched off and on again,
// so what opened meanwhile was never seen. The next allowed listing reads its
// unknown ids old (the first-run rule); the ids already known keep their date.
func (r *Runner) rearmBirths() {
	if r.birthsLoaded {
		r.birthsFirst = true
	}
}

func (r *Runner) saveBirths() {
	if r.deps.SaveBirths == nil {
		return
	}
	if err := r.deps.SaveBirths(r.births); err != nil {
		r.deps.Logf("cmuxsync: births: save: %v", err)
	}
}

// noteBirths runs at the slow listing: listed = the allowed windows' ids (they
// are born), all = every window's ids (the absences, as closeMissing). The
// only path that forgets.
func (r *Runner) noteBirths(listed, all []string, now time.Time) {
	if !r.birthsOn() {
		return
	}
	r.loadBirths()
	changed := false
	if r.birthsFirst {
		if len(listed) == 0 {
			return
		}
		for _, id := range listed {
			id = strings.ToUpper(id)
			if _, ok := r.births[id]; !ok {
				r.births[id] = time.Time{}
				changed = true
			}
		}
		r.birthsFirst = false
	} else {
		for _, id := range listed {
			id = strings.ToUpper(id)
			if _, ok := r.births[id]; !ok {
				r.births[id] = now
				changed = true
			}
		}
	}
	live := make(map[string]bool, len(all))
	for _, id := range all {
		live[strings.ToUpper(id)] = true
	}
	for id := range r.births {
		next, forget := missedTwice(live[id], r.birthsMissing[id])
		if forget {
			delete(r.births, id)
			delete(r.birthsMissing, id)
			changed = true
			continue
		}
		if next == 0 {
			delete(r.birthsMissing, id)
		} else {
			r.birthsMissing[id] = next
		}
	}
	if changed {
		r.saveBirths()
	}
}

// seeBirths runs at the round's own listing: an id the clock does not know is
// born now. It never forgets.
func (r *Runner) seeBirths(order []string, now time.Time) {
	if !r.birthsOn() || !r.birthsLoaded || r.birthsFirst {
		return
	}
	changed := false
	for _, id := range order {
		id = strings.ToUpper(id)
		if _, ok := r.births[id]; !ok {
			r.births[id] = now
			changed = true
		}
	}
	if changed {
		r.saveBirths()
	}
}

// markOld gives a workspace no grace (a generated anchor, D18).
func (r *Runner) markOld(ws string) {
	if !r.birthsOn() || !r.birthsLoaded {
		return
	}
	ws = strings.ToUpper(ws)
	if t, ok := r.births[ws]; ok && t.IsZero() {
		return
	}
	r.births[ws] = time.Time{}
	r.saveBirths()
}

// young: born less than BirthGrace ago. Unknown = old.
func (r *Runner) young(ws string, now time.Time) bool {
	born := r.births[strings.ToUpper(ws)]
	return !born.IsZero() && now.Sub(born) < r.cfg.BirthGrace
}
