// Package sessbudget measures the live and busy Claude sessions against the
// machine's memory (the sampler, this file) and, in later lots, grants launch
// slots from those samples. It sits beside internal/daemon/admission and reads
// the same compressor / available-memory sensor.
package sessbudget

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/sysmem"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// rateWindow is the span the compressor and swap-out rates are averaged over.
const rateWindow = 60 * time.Second

// Deps are the sampler's seams: the daemon wires the real ones, tests fakes.
type Deps struct {
	// Peers lists the registered, not ended, sessions.
	Peers func() ([]pigtail.PeerSession, error)
	// Memory is the memory sensor (the one admission reads).
	Memory func() (sysmem.Memory, error)
	// Procs is the process table (footprint and CPU of a session's tree).
	Procs func() (map[int]sysmem.Proc, error)
	// ReadNative reads <cfg>/sessions/<pid>.json: the seam the relay wake uses.
	ReadNative func(cfg string, pid int) (claudesession.Native, bool)
	// SessionAlive: the pid runs and its native file still names the session.
	SessionAlive func(cfg string, pid int, sessionID string) bool
	// WorkspaceProject names the project of a pigtail workspace ("" unknown).
	WorkspaceProject func(workspaceID string) string
	Now              func() time.Time
	// Wait blocks for d, or until ctx is done.
	Wait func(ctx context.Context, d time.Duration)
	Logf func(format string, args ...any)
}

// DefaultDeps wires the production seams around the given peer and workspace
// readers.
func DefaultDeps(peers func() ([]pigtail.PeerSession, error), workspaceProject func(string) string) Deps {
	return Deps{
		Peers:            peers,
		Memory:           sysmem.ReadMemory,
		Procs:            sysmem.Procs,
		ReadNative:       claudesession.ReadNative,
		SessionAlive:     claudesession.SessionAlive,
		WorkspaceProject: workspaceProject,
		Now:              time.Now,
		Wait: func(ctx context.Context, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		},
		Logf: log.Printf,
	}
}

type counters struct {
	at       time.Time
	compress uint64 // compressions + decompressions, cumulative bytes
	swapouts uint64
}

// Sampler takes one Sample per tick and appends it to its History.
type Sampler struct {
	deps     Deps
	history  *History
	interval time.Duration

	// OnSample, when non-nil, is called with the record just appended, after
	// the append, on the sampler's goroutine. It is the only way samples reach
	// the controller. Set it before Run starts.
	OnSample func(Sample)

	win []counters // only touched by Sample's caller (one goroutine)
}

// New returns a Sampler ticking every interval.
func New(deps Deps, h *History, interval time.Duration) *Sampler {
	if deps.Logf == nil {
		deps.Logf = func(string, ...any) {}
	}
	return &Sampler{deps: deps, history: h, interval: interval}
}

// History returns the log the sampler appends to.
func (s *Sampler) History() *History { return s.history }

// Run samples at once, then every interval, until ctx is done.
func (s *Sampler) Run(ctx context.Context) {
	if s.interval <= 0 {
		return
	}
	for {
		if _, err := s.Sample(ctx); err != nil {
			s.deps.Logf("sessbudget: sampling: %v", err)
		}
		s.deps.Wait(ctx, s.interval)
		if ctx.Err() != nil {
			return
		}
	}
}

// Sample takes one sample now, appends it and calls OnSample.
func (s *Sampler) Sample(_ context.Context) (Sample, error) {
	now := s.deps.Now().UTC()
	out := Sample{TS: now, Sessions: []pigtail.SessionSampleRow{}}

	if mem, err := s.deps.Memory(); err != nil {
		s.deps.Logf("sessbudget: reading memory: %v", err)
	} else {
		out.AvailableBytes, out.CompressorBytes = mem.Available, mem.CompressorBytes
		out.CompressBytesPerSec, out.SwapoutBytesPerSec = s.rates(now, mem)
	}

	peers, err := s.deps.Peers()
	if err != nil {
		return out, err
	}
	var procs map[int]sysmem.Proc
	if s.deps.Procs != nil {
		if procs, err = s.deps.Procs(); err != nil {
			s.deps.Logf("sessbudget: reading processes: %v", err)
			procs = nil
		}
	}
	for _, p := range peers {
		// A session counts only if its process runs and its native file still
		// names it; one that cannot be checked counts nothing.
		if p.PID <= 0 || p.ConfigDir == "" || !s.deps.SessionAlive(p.ConfigDir, p.PID, p.SessionID) {
			continue
		}
		row := pigtail.SessionSampleRow{
			SessionID: p.SessionID, PID: p.PID, Cwd: p.Cwd, SubjectID: p.SubjectID,
			Scope:  rowScope(p.Scope),
			Repo:   s.repo(p),
			Status: "idle",
		}
		// The native status, never the stored column. Unreadable: live, not busy.
		if n, ok := s.deps.ReadNative(p.ConfigDir, p.PID); ok && n.SessionID == p.SessionID {
			switch n.Status {
			case "busy", "waiting":
				row.Status = n.Status
			}
			// idle, shell (idle with a background shell) and anything else: idle.
		}
		if procs != nil {
			row.FootprintBytes = sysmem.SumTree(procs, []int{p.PID})
			row.CPUPercent = sysmem.SumTreeCPU(procs, []int{p.PID})
		}
		out.Live++
		if row.Status == "busy" {
			out.Busy++
		}
		out.Sessions = append(out.Sessions, row)
	}

	if err := s.history.Append(out); err != nil {
		s.deps.Logf("sessbudget: appending a sample: %v", err)
	}
	if s.OnSample != nil {
		s.OnSample(out)
	}
	return out, nil
}

// rowScope is the scope a row is recorded under: canonical, "" when the
// session registered none (the master sees it alone, D17).
func rowScope(v string) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	return string(scope.Canonical(v))
}

// repo is the session's repository: its workspace's project first, else the
// git toplevel of its cwd, found without exec.
func (s *Sampler) repo(p pigtail.PeerSession) string {
	if p.WorkspaceID != "" && s.deps.WorkspaceProject != nil {
		if name := s.deps.WorkspaceProject(p.WorkspaceID); name != "" {
			return name
		}
	}
	return repoFromCwd(p.Cwd)
}

// rates averages the compressor traffic and the swap-outs over the last
// rateWindow. The first reading has nothing to compare with: 0.
func (s *Sampler) rates(now time.Time, mem sysmem.Memory) (compress, swap float64) {
	cur := counters{at: now, compress: mem.Compressions + mem.Decompressions, swapouts: mem.Swapouts}
	s.win = append(s.win, cur)
	cut := 0
	for cut < len(s.win)-1 && now.Sub(s.win[cut+1].at) >= rateWindow {
		cut++ // keep one reading at or just before the window's start
	}
	s.win = s.win[cut:]
	old := s.win[0]
	dt := now.Sub(old.at).Seconds()
	if dt <= 0 {
		return 0, 0
	}
	if cur.compress >= old.compress {
		compress = float64(cur.compress-old.compress) / dt
	}
	if cur.swapouts >= old.swapouts {
		swap = float64(cur.swapouts-old.swapouts) / dt
	}
	return compress, swap
}
