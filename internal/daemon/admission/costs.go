package admission

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

const (
	// ewmaAlpha weighs the newest peak. 0.3 follows a repo that grows over a
	// few runs without letting one outlier run rewrite the estimate.
	ewmaAlpha = 0.3
	// minObservations before a learned cost replaces the prior.
	minObservations = 3
	// costMargin covers run-to-run variance above the average peak.
	costMargin = 1.2
	// costsFileName sits next to the database, like rammon's state file.
	costsFileName = "admission-costs.json"
	// costsFormatVersion is the on-disk shape of the learned costs. 2 is the
	// first versioned one: peaks measured as physical footprint. The
	// unversioned (flat map) files before it hold ps-RSS peaks.
	costsFormatVersion = 2
)

// errLegacyCosts reports a learned-costs file from before costsFormatVersion.
var errLegacyCosts = fmt.Errorf("learned costs predate format %d (peaks measured as ps RSS, which leaves compressed memory out): discarded, relearning from the class priors", costsFormatVersion)

// errNewerCosts reports a learned-costs file from a later format — a
// downgrade. Wrapped with the version found (see load).
var errNewerCosts = errors.New("learned costs written by a newer pigtail")

// costsFile is the persisted shape. A legacy flat map decodes into it with
// Version 0 — its keys hold a \x00 and can never be "version".
type costsFile struct {
	Version int                   `json:"version"`
	Costs   map[string]*costEntry `json:"costs"`
}

// CostsPath returns where the learned costs persist for a daemon whose
// database is at dbPath; "" (no persistence) when dbPath is empty.
func CostsPath(dbPath string) string {
	if dbPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(dbPath), costsFileName)
}

type costEntry struct {
	EWMA float64 `json:"ewma_bytes"`
	N    int     `json:"n"`
}

// costTable learns what a (class, repo, workers) triple actually costs from
// the peak memory (physical footprint on darwin) of its process trees, so the
// controller stops reserving 3 GiB for a jest run that never passes 800 MB —
// and starts reserving 5 for one that does.
//
// workers is part of the key only for the runs whose cost is a function of it
// — a browser-mode vitest run carries one Chromium per worker, and pigtail
// itself picks that number, so folding a 2-worker run and an 8-worker one
// into the same EWMA would have it chase a target pigtail is moving. Every
// other class passes 0 and keeps the two-part key it already learned under:
// an upgrade must not throw away what the daemon knows.
//
// Its own mutex, not the controller's: observe runs on release, estimate on
// enqueue, and neither needs the queue.
type costTable struct {
	path string

	mu sync.Mutex
	m  map[string]*costEntry
}

func newCostTable(path string) *costTable {
	return &costTable{path: path, m: map[string]*costEntry{}}
}

func costKey(class, repo string, workers int) string {
	k := class + "\x00" + repo
	if workers > 0 {
		k += "\x00" + strconv.Itoa(workers)
	}
	return k
}

// estimate returns the cost to reserve: prior until minObservations peaks are
// known, then costMargin × their EWMA, never below floor (a run that happened
// to lint one file must not teach the controller that linting is free).
//
// floor is passed in rather than derived from prior because the two can come
// from different places: prior may be a client-computed estimate for this
// exact run shape, while the floor must stay anchored to what the *class* is
// known to cost — otherwise an over-generous client estimate would block its
// own run's measured peak from ever being believed.
func (t *costTable) estimate(class, repo string, workers int, prior, floor uint64) uint64 {
	t.mu.Lock()
	e, ok := t.m[costKey(class, repo, workers)]
	t.mu.Unlock()
	if !ok || e.N < minObservations {
		return prior
	}
	learned := uint64(costMargin * e.EWMA)
	if learned < floor {
		return floor
	}
	return learned
}

// observe folds one lease's peak memory (physical footprint on darwin) into
// the table.
func (t *costTable) observe(class, repo string, workers int, peak uint64) {
	if peak == 0 {
		return // never sampled: teaches nothing
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	k := costKey(class, repo, workers)
	e, ok := t.m[k]
	if !ok {
		t.m[k] = &costEntry{EWMA: float64(peak), N: 1}
		return
	}
	e.EWMA = ewmaAlpha*float64(peak) + (1-ewmaAlpha)*e.EWMA
	e.N++
}

// snapshot copies the table for Status.
func (t *costTable) snapshot() map[string]costEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]costEntry, len(t.m))
	for k, v := range t.m {
		out[k] = *v
	}
	return out
}

// load reads the persisted table. A missing file is a first start, not an
// error; an unreadable one, or one in another format (older: RSS peaks;
// newer: a downgrade, overwritten at the next save), is reported and the
// table starts empty — the priors are a safe place to fall back to.
func (t *costTable) load() error {
	if t.path == "" {
		return nil
	}
	data, err := os.ReadFile(t.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f costsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	switch {
	case f.Version > costsFormatVersion:
		return fmt.Errorf("%w (format %d, this binary reads %d): discarded, the next save will overwrite them with format %d",
			errNewerCosts, f.Version, costsFormatVersion, costsFormatVersion)
	case f.Version < costsFormatVersion:
		return errLegacyCosts
	}
	if f.Costs == nil {
		f.Costs = map[string]*costEntry{}
	}
	t.mu.Lock()
	t.m = f.Costs
	t.mu.Unlock()
	return nil
}

// save writes the table atomically (temp + rename), for the same reason as
// rammon's state file: a reader landing mid-write must never see half a file.
func (t *costTable) save() error {
	if t.path == "" {
		return nil
	}
	t.mu.Lock()
	data, err := json.Marshal(costsFile{Version: costsFormatVersion, Costs: t.m})
	t.mu.Unlock()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(t.path), filepath.Base(t.path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, t.path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
