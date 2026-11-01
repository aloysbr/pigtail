package pigtail

import "time"

// AdmissionRequest is the body of POST /admission/leases: a heavy command
// asking to start.
type AdmissionRequest struct {
	// Group is the fairness unit — conventionally the worktree name.
	Group string `json:"group"`
	// Class is the heavy-command family (tsc, eslint, jest, vitest, vitest-browser, nx,
	// storybook-build, build, precommit, go-test, generic). It picks the prior cost and keys what the
	// daemon learns.
	Class string `json:"class"`
	// Label names the command, for diagnostics only.
	Label string `json:"label,omitempty"`
	// Repo keys learned costs together with Class.
	Repo string `json:"repo,omitempty"`
	// PID is the client process; the lease is charged with its tree's memory.
	PID int `json:"pid,omitempty"`
	// CostHintBytes overrides the daemon's estimate (floored at half the
	// class prior). It is the user's own `--cost-mb`, and it wins over what
	// the daemon learned.
	CostHintBytes uint64 `json:"cost_hint_bytes,omitempty"`
	// CostPriorBytes is what the *client* computed this run should cost
	// before anything is known about it — a browser-mode run's cost follows
	// the worker count pigtail itself picked, which the daemon cannot see.
	// Unlike CostHintBytes it does not override what the daemon learned: it
	// only replaces the class prior it starts from.
	CostPriorBytes uint64 `json:"cost_prior_bytes,omitempty"`
	// Workers is how many workers the run will start, when the cost scales
	// with it. It keys the learned cost alongside class and repo, so a
	// 2-worker run never teaches the estimate for an 8-worker one.
	Workers int `json:"workers,omitempty"`
	// SessionID, CmuxWorkspaceID and Cwd identify the caller, so the daemon
	// can tell whether it is in focus (its subject's or theme's open focus,
	// or a pinned cmux workspace). All optional: an older client sends none
	// and is served in the normal order.
	SessionID       string `json:"session_id,omitempty"`
	CmuxWorkspaceID string `json:"cmux_workspace_id,omitempty"`
	Cwd             string `json:"cwd,omitempty"`
	// ParentLease is the id of the lease held by the generic envelope this
	// command runs under (`pigtail admit -- git push`): the daemon does not
	// count it against this request, so parent and child cannot wait on each
	// other. An unknown id is ignored.
	ParentLease string `json:"parent_lease,omitempty"`
}

// Admission update states.
const (
	AdmissionQueued  = "queued"
	AdmissionGranted = "granted"
)

// AdmissionUpdate is one NDJSON line streamed by POST /admission/leases:
// "queued" lines while waiting (one at once, then periodically), a single
// "granted" line when admitted — after which the connection stays open and
// *is* the lease.
type AdmissionUpdate struct {
	State string `json:"state"`

	// Queued fields.
	Position       int    `json:"position,omitempty"`
	Queued         int    `json:"queued,omitempty"`
	Running        int    `json:"running,omitempty"`
	AvailableBytes uint64 `json:"available_bytes,omitempty"`
	EffectiveBytes uint64 `json:"effective_bytes,omitempty"`
	NeedBytes      uint64 `json:"need_bytes,omitempty"`
	FloorBytes     uint64 `json:"floor_bytes,omitempty"`
	// DiskFreeBytes and DiskFloorBytes are set while Reason is "disk".
	DiskFreeBytes  uint64 `json:"disk_free_bytes,omitempty"`
	DiskFloorBytes uint64 `json:"disk_floor_bytes,omitempty"`
	// Reason is what holds the queue's head back: memory, pressure,
	// swapout, compress, compress_calm, max_jobs, class_limit, disk — or empty for
	// a waiter behind the head.
	Reason string `json:"reason,omitempty"`

	// Granted fields.
	LeaseID   string `json:"lease_id,omitempty"`
	CostBytes uint64 `json:"cost_bytes,omitempty"`
	WaitedMS  int    `json:"waited_ms,omitempty"`
	Degraded  bool   `json:"degraded,omitempty"`
}

// AdmissionLeaseView is one admitted command in AdmissionStatus.
type AdmissionLeaseView struct {
	ID        string `json:"id"`
	Group     string `json:"group"`
	Class     string `json:"class"`
	Label     string `json:"label,omitempty"`
	CostBytes uint64 `json:"cost_bytes"`
	// RSSBytes is the lease's process-tree memory: physical footprint on
	// darwin (top's MEM, compressed pages included), VmRSS on Linux. Named
	// and keyed rss_bytes for wire compatibility (Pigtail, older CLIs); it
	// held ps RSS before 2026-10.
	RSSBytes   uint64 `json:"rss_bytes"`
	PeakBytes  uint64 `json:"peak_bytes"`
	AgeSeconds int    `json:"age_seconds"`
	// WaitedMS is how long this command sat in the queue before it started.
	WaitedMS int `json:"waited_ms"`
}

// AdmissionWaiterView is one queued command in AdmissionStatus.
type AdmissionWaiterView struct {
	Group      string `json:"group"`
	Class      string `json:"class"`
	Label      string `json:"label,omitempty"`
	CostBytes  uint64 `json:"cost_bytes"`
	AgeSeconds int    `json:"age_seconds"`
	Reason     string `json:"reason,omitempty"`
	// Focus: the caller is in focus and is considered before the others.
	// A boolean only: no focus name, subject or title leaves the daemon.
	Focus bool `json:"focus,omitempty"`
}

// AdmissionCostView is one learned cost.
type AdmissionCostView struct {
	Class string `json:"class"`
	Repo  string `json:"repo"`
	// Workers is 0 for the classes whose cost does not scale with one.
	Workers   int    `json:"workers,omitempty"`
	EWMABytes uint64 `json:"ewma_bytes"`
	Samples   int    `json:"samples"`
}

// AdmissionStatus is the body of GET /admission. MaxJobs 0 means the
// controller is disabled (-admit-max-jobs=0).
type AdmissionStatus struct {
	MaxJobs    int    `json:"max_jobs"`
	FloorBytes uint64 `json:"floor_bytes"`
	// DiskFreeBytes is the free space of the tightest volume pigtail's heavy
	// commands write to (home or temp); meaningful only when DiskKnown. The
	// three disk fields are always present, so a client can tell "0 free"
	// from "not reported". DiskFloorBytes is the admission floor (0 = off):
	// below it nothing is admitted, whatever the memory says.
	DiskFreeBytes  uint64 `json:"disk_free_bytes"`
	DiskKnown      bool   `json:"disk_known"`
	DiskFloorBytes uint64 `json:"disk_floor_bytes"`
	// PressureGate is where kernel pressure starts braking admissions — a
	// tighter swap gate and PressureFloorFactor times the floor — not where
	// it stops them; only critical does that.
	PressureGate        int     `json:"pressure_gate"`
	PressureFloorFactor float64 `json:"pressure_floor_factor,omitempty"`
	// MaxStarveSeconds is how long a waiter may be held back before only the
	// memory floor still applies to it. 0 means the guarantee is off.
	MaxStarveSeconds       int     `json:"max_starve_seconds,omitempty"`
	SwapoutGateBytesPerSec float64 `json:"swapout_gate_bytes_per_sec"`
	TotalBytes             uint64  `json:"total_bytes"`
	AvailableBytes         uint64  `json:"available_bytes"`
	EffectiveBytes         uint64  `json:"effective_bytes"`
	Pressure               int     `json:"pressure"`
	SwapoutBytesPerSec     float64 `json:"swapout_bytes_per_sec"`
	// CompressorBytes is the RAM the memory compressor holds, UnusedBytes the
	// pages the kernel lists free (available also counts inactive ones);
	// CompressRateBytesPerSec the compressions + decompressions rate over the
	// recent window and CompressGateBytesPerSec the rate that holds the queue
	// (0 = gate off).
	CompressorBytes         uint64  `json:"compressor_bytes"`
	UnusedBytes             uint64  `json:"unused_bytes"`
	CompressRateBytesPerSec float64 `json:"compress_rate_bytes_per_sec"`
	CompressGateBytesPerSec float64 `json:"compress_gate_bytes_per_sec"`
	// The calm rule (2026-10-28): CompressCalmClasses start only once the
	// compressor rate is under CompressGateBytesPerSec over ten seconds and
	// over CompressCalmWindowSeconds, whose current rate is
	// CompressCalmRateBytesPerSec; past CompressCalmMaxStarveSeconds a wait
	// lifts it (0 = never). All absent when the rule is off.
	CompressCalmWindowSeconds    int                   `json:"compress_calm_window_seconds,omitempty"`
	CompressCalmRateBytesPerSec  float64               `json:"compress_calm_rate_bytes_per_sec,omitempty"`
	CompressCalmClasses          []string              `json:"compress_calm_classes,omitempty"`
	CompressCalmMaxStarveSeconds int                   `json:"compress_calm_max_starve_seconds,omitempty"`
	ReadAgeSeconds               int                   `json:"read_age_seconds"`
	Leases                       []AdmissionLeaseView  `json:"leases"`
	Queue                        []AdmissionWaiterView `json:"queue"`
	Costs                        []AdmissionCostView   `json:"costs"`
	// ClassLimits caps concurrent leases per class, for the classes whose
	// scarce resource is not memory.
	ClassLimits map[string]int `json:"class_limits,omitempty"`
}

// Outcomes of an AdmissionRecord.
const (
	// AdmissionOutcomeReleased: the command ran and its client let go.
	AdmissionOutcomeReleased = "released"
	// AdmissionOutcomeRevoked: held past MaxHold, its accounting was dropped
	// while the command kept running — held_ms is a lower bound.
	AdmissionOutcomeRevoked = "revoked"
	// AdmissionOutcomeCancelled: the client gave up while queued (its wait
	// budget ran out, Ctrl-C): it never ran under a lease.
	AdmissionOutcomeCancelled = "cancelled"
)

// AdmissionRecord is one ended command, as the daemon appends it to
// admission-history.jsonl. It exists to put a price on memory scarcity: the
// time spent waiting for the machine, and the time spent running on a machine
// that was swapping.
type AdmissionRecord struct {
	TS      time.Time `json:"ts"` // when it ended
	Group   string    `json:"group"`
	Repo    string    `json:"repo,omitempty"`
	Class   string    `json:"class"`
	Label   string    `json:"label,omitempty"`
	Outcome string    `json:"outcome"`
	// Focus: the caller was in focus when it enqueued (boolean only).
	Focus bool `json:"focus,omitempty"`

	EnqueuedAt time.Time `json:"enqueued_at"`
	WaitedMS   int64     `json:"waited_ms"`
	// WaitByReasonMS splits WaitedMS by what held the command back (the
	// admission reasons: memory, pressure, swapout, compress, max_jobs, class_limit;
	// "other" for the rare pass that stopped on none of them).
	WaitByReasonMS map[string]int64 `json:"wait_by_reason_ms,omitempty"`
	// HeldMS is how long it ran under its lease; 0 when cancelled.
	HeldMS    int64  `json:"held_ms"`
	PeakBytes uint64 `json:"peak_bytes"`
	CostBytes uint64 `json:"cost_bytes"`
	// AvailAtEnqueue is the available memory the controller last read when
	// the command asked.
	AvailAtEnqueue uint64 `json:"avail_at_enqueue"`
	// MaxPressure is the highest kernel pressure seen while it ran (1/2/4, 0
	// unknown); SwapoutBytesDuring what the whole machine swapped out
	// meanwhile.
	MaxPressure        int    `json:"max_pressure"`
	SwapoutBytesDuring uint64 `json:"swapout_bytes_during"`
	// CompressBytesDuring is the compressor traffic (bytes compressed plus
	// decompressed) of the whole machine while it ran, like SwapoutBytesDuring;
	// MaxCompressRate the highest rate in bytes/s read meanwhile.
	CompressBytesDuring uint64 `json:"compress_bytes_during"`
	MaxCompressRate     uint64 `json:"max_compress_rate"`
	// AdmitCompressRate and AdmitCompressCalmRate are the compressor traffic
	// (bytes/s) read when the command was admitted, over the 10 s window and
	// over the calm window (0 when the calm window is 0): what recalibrating the
	// compressor gates needs. Both 0 on a cancelled command, never admitted.
	AdmitCompressRate     uint64 `json:"admit_compress_rate"`
	AdmitCompressCalmRate uint64 `json:"admit_compress_calm_rate"`
	// Degraded: admitted by the MaxStarve guarantee, past every gate but the
	// plain memory floor.
	Degraded bool `json:"degraded,omitempty"`
	// ExitCode is the command's exit status, as its client reported it
	// before releasing; absent when no client reported one — an older
	// client, a revoked or cancelled lease.
	ExitCode *int `json:"exit_code,omitempty"`
}

// AdmissionHistory is the body of GET /admission/history.
type AdmissionHistory struct {
	// FloorBytes is the floor in force now — what a simulated extra amount of
	// memory is weighed against.
	FloorBytes uint64            `json:"floor_bytes"`
	Records    []AdmissionRecord `json:"records"`
}

// Outcomes of a CPUSlotRecord.
const (
	// CPUSlotOutcomeReleased: the hold ended with its client letting go (a
	// grant racing a cancellation counts here, with a hold of about nothing).
	CPUSlotOutcomeReleased = "released"
	// CPUSlotOutcomeRevoked: held past MaxHold, its accounting was dropped.
	CPUSlotOutcomeRevoked = "revoked"
	// CPUSlotOutcomeCancelled: the client gave up while queued; held_ms is 0.
	CPUSlotOutcomeCancelled = "cancelled"
)

// CPUSlotRecord is one line of cpu-slots-history.jsonl: one ended CPU slot
// hold (or a queued wait that never became one). It puts a price on CPU slot
// waits before anyone decides on a CPU priority.
type CPUSlotRecord struct {
	TS         time.Time `json:"ts"` // when it ended
	Group      string    `json:"group"`
	Label      string    `json:"label,omitempty"`
	EnqueuedAt time.Time `json:"enqueued_at"`
	WaitedMS   int64     `json:"waited_ms"`
	HeldMS     int64     `json:"held_ms"` // 0 when cancelled
	Outcome    string    `json:"outcome"`
}

// CPUSlotHistory is the body of GET /cpu/slots/history.
type CPUSlotHistory struct {
	Records []CPUSlotRecord `json:"records"`
}

// AdmissionExit is the body of POST /admission/leases/{id}/exit: the exit
// status of the command a lease admitted, reported before the client hangs
// up. ExitCode is a pointer so that a missing field is a 400, not a zero.
type AdmissionExit struct {
	ExitCode *int `json:"exit_code"`
}
