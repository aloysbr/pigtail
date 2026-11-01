//go:build linux

package admission

import (
	"runtime"

	"github.com/aloysbr/pigtail/internal/sysmem"
)

// platformShape: Linux has no performance/efficiency split to read, so every
// logical CPU counts; memory is MemTotal.
func platformShape() (cores int, mem uint64) {
	m, _ := sysmem.ReadMemory()
	return runtime.NumCPU(), m.Total
}
