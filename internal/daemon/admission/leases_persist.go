package admission

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	// leasesFileName sits next to the database, like the learned costs.
	leasesFileName = "admission-leases.json"
	// leasesFormatVersion is the on-disk shape of the persisted leases.
	leasesFormatVersion = 1
	// restoredComm is what a restored lease's PID must still run: the client
	// of a lease is `pigtail admit` itself.
	restoredComm = "pigtail"
)

// errNewerLeases reports a leases file from a later format — a downgrade.
var errNewerLeases = errors.New("admission leases written by a newer pigtail")

// LeasesPath returns where the leases in flight persist for a daemon whose
// database is at dbPath; "" (no persistence) when dbPath is empty.
func LeasesPath(dbPath string) string {
	if dbPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(dbPath), leasesFileName)
}

// leaseRec is one lease on the books, as persisted.
type leaseRec struct {
	LeaseID    string    `json:"lease_id"`
	Class      string    `json:"class"`
	Repo       string    `json:"repo"`
	Group      string    `json:"group"`
	Label      string    `json:"label"`
	PID        int       `json:"pid"`
	CostBytes  uint64    `json:"cost_bytes"`
	GrantedAt  time.Time `json:"granted_at"`
	EnqueuedAt time.Time `json:"enqueued_at"`
	WaitedMS   int64     `json:"waited_ms"`
}

// leasesFile is the persisted shape: the leases held, and the sequence the
// next id continues from (so an id is never reused across a restart).
type leasesFile struct {
	Version int        `json:"version"`
	Seq     int        `json:"seq"`
	Leases  []leaseRec `json:"leases"`
}

// writeLeasesFile writes f atomically (temp + rename): a reader, or a daemon
// dying mid-write, never sees half a file.
func writeLeasesFile(path string, f leasesFile) error {
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
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
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// readLeasesFile reads the persisted leases. A missing file is a first start.
func readLeasesFile(path string) (leasesFile, error) {
	var f leasesFile
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return leasesFile{}, err
	}
	if f.Version > leasesFormatVersion {
		return leasesFile{}, fmt.Errorf("%w (format %d, this binary reads %d): ignored, the next write overwrites it",
			errNewerLeases, f.Version, leasesFormatVersion)
	}
	return f, nil
}

// pidAlive reports whether pid names a live process (kill -0; EPERM is a
// process of someone else, so alive).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// pidComm returns `ps -o comm=` of pid: the executable's path on macOS, its
// name on Linux. "" when it cannot be read.
func pidComm(pid int) string {
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // pid is an int
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// WithLeasesPath persists the leases in flight at path ("" keeps them in
// memory) and reloads them in New, before the controller serves anything.
func WithLeasesPath(path string) Option { return func(c *Controller) { c.leasesPath = path } }

// WithProcessProbe overrides how a restored lease's client is checked: alive
// says whether the PID lives, comm what it runs (tests pass fakes).
func WithProcessProbe(alive func(pid int) bool, comm func(pid int) string) Option {
	return func(c *Controller) { c.alive, c.comm = alive, comm }
}

// persistLocked writes the leases on the books and the id sequence. Caller
// holds mu — on purpose: a grant is written before its ticket is closed, so a
// daemon dying between the two never under-counts the run at the next start,
// and writes are ordered by the lock. The file is a few hundred bytes. A
// failure is logged once and never blocks the controller.
func (c *Controller) persistLocked() {
	if c.leasesPath == "" {
		return
	}
	f := leasesFile{Version: leasesFormatVersion, Seq: c.seq, Leases: make([]leaseRec, 0, len(c.leases))}
	for l := range c.leases {
		f.Leases = append(f.Leases, leaseRec{
			LeaseID: l.id, Class: l.req.Class, Repo: l.req.Repo, Group: l.req.Group, Label: l.req.Label,
			PID: l.req.PID, CostBytes: l.cost, GrantedAt: l.since, EnqueuedAt: l.enqueuedAt,
			WaitedMS: l.waited.Milliseconds(),
		})
	}
	write := c.writeLeases
	if write == nil {
		write = writeLeasesFile
	}
	if err := write(c.leasesPath, f); err != nil {
		if !c.persistErrLogged {
			c.persistErrLogged = true
			c.logf("admission: persisting the leases in flight: %v (a restart will forget them)", err)
		}
		return
	}
	c.persistErrLogged = false
}

// loadLeases reloads the leases a previous daemon left. A lease is kept only
// if its client still lives, still runs `pigtail`, and is younger than
// MaxHold; the rest are dropped. An unreadable file starts the controller
// empty, logged once. Called by New, single-threaded.
func (c *Controller) loadLeases() {
	if c.leasesPath == "" || c.cfg.MaxJobs <= 0 {
		return
	}
	removeStaleLeaseTemps(c.leasesPath)
	f, err := readLeasesFile(c.leasesPath)
	if err != nil {
		c.logf("admission: ignoring the persisted leases: %v", err)
		return
	}
	c.seq = f.Seq
	now := c.now()
	kept := 0
	for _, r := range f.Leases {
		if n, ok := leaseSeq(r.LeaseID); ok && n > c.seq {
			c.seq = n
		}
		switch {
		case !c.alive(r.PID):
			c.logf("admission: dropping restored lease %s (%s): pid %d is gone", r.LeaseID, r.Class, r.PID)
			continue
		case filepath.Base(c.comm(r.PID)) != restoredComm:
			c.logf("admission: dropping restored lease %s (%s): pid %d is not a pigtail client", r.LeaseID, r.Class, r.PID)
			continue
		case c.cfg.MaxHold > 0 && now.Sub(r.GrantedAt) >= c.cfg.MaxHold:
			c.logf("admission: dropping restored lease %s (%s): held past MaxHold", r.LeaseID, r.Class)
			continue
		}
		c.leases[&lease{
			id:   r.LeaseID,
			req:  Request{Group: r.Group, Class: r.Class, Label: r.Label, Repo: r.Repo, PID: r.PID},
			cost: r.CostBytes, since: r.GrantedAt, waited: time.Duration(r.WaitedMS) * time.Millisecond,
			enqueuedAt: r.EnqueuedAt, restored: true,
		}] = struct{}{}
		kept++
	}
	if kept > 0 || len(f.Leases) > 0 {
		c.logf("admission: restored %d lease(s) of %d from the previous daemon", kept, len(f.Leases))
		c.persistLocked()
	}
}

// removeStaleLeaseTemps deletes the `<leases file>.tmp-*` files a daemon killed
// between CreateTemp and Rename left behind. Called at load, single-threaded,
// before any write of this daemon exists.
func removeStaleLeaseTemps(path string) {
	stale, err := filepath.Glob(path + ".tmp-*")
	if err != nil {
		return
	}
	for _, p := range stale {
		_ = os.Remove(p)
	}
}

// leaseSeq parses the n of an "adm-<n>" id.
func leaseSeq(id string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "adm-"))
	return n, err == nil && strings.HasPrefix(id, "adm-")
}

// dropRestoredByPIDLocked removes the restored leases of pid: a client that
// asks again lost its "granted" and reconnected, so its old place must not
// count twice. Caller holds mu; the dropped leases are returned.
func (c *Controller) dropRestoredByPIDLocked(pid int) []lease {
	if pid <= 0 {
		return nil
	}
	var dropped []lease
	for l := range c.leases {
		if l.restored && l.req.PID == pid {
			dropped = append(dropped, *l)
			delete(c.leases, l)
		}
	}
	if len(dropped) > 0 {
		c.persistLocked()
	}
	return dropped
}

// clientStillThere reports whether pid is alive and still runs `pigtail`: a
// PID reused by another program is as gone as a dead one.
func (c *Controller) clientStillThere(pid int) bool {
	return c.alive(pid) && filepath.Base(c.comm(pid)) == restoredComm
}

// releaseGoneRestored releases the restored leases whose client has died (or
// whose PID now belongs to another program). It
// is their only way out: nobody holds their stream, so no Release comes. They
// teach the learned costs nothing (no peak was watched). Called by Tick.
func (c *Controller) releaseGoneRestored() {
	c.mu.Lock()
	var gone []lease
	for l := range c.leases {
		if l.restored && !c.clientStillThere(l.req.PID) {
			gone = append(gone, *l)
			delete(c.leases, l)
		}
	}
	if len(gone) == 0 {
		c.mu.Unlock()
		return
	}
	c.persistLocked()
	granted := c.admitLocked()
	c.mu.Unlock()
	now := c.now()
	for _, l := range gone {
		c.publishReleased(l)
		c.appendHistory(leaseRecord(l, pigtail.AdmissionOutcomeReleased, now))
	}
	c.publishGranted(granted)
	c.fireNotify()
}
