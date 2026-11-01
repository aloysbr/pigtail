package rammon

import "github.com/aloysbr/pigtail/internal/sysmem"

// memStat is one process's memory at a point in time.
type memStat = sysmem.Proc

// memSampler reads system + per-process memory. Implemented by the platform
// reader in internal/sysmem, faked in tests.
type memSampler interface {
	available() (uint64, error)      // system memory available to reclaim
	procs() (map[int]memStat, error) // every visible process's memory + ppid
}

func newPlatformMemSampler() memSampler { return platformMem{} }

// platformMem adapts internal/sysmem's package functions to memSampler.
type platformMem struct{}

func (platformMem) available() (uint64, error)      { return sysmem.Available() }
func (platformMem) procs() (map[int]memStat, error) { return sysmem.Procs() }

// sumTree returns the total memory of the given root PIDs plus all descendants.
func sumTree(snap map[int]memStat, roots []int) uint64 { return sysmem.SumTree(snap, roots) }
