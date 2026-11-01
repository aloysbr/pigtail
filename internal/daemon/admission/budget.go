package admission

// gibPerJob is the memory one concurrent heavy job is budgeted when sizing
// the default job ceiling: 64 GB → 6 jobs. Raised from the 16 GiB first
// planned, on the maintainer's call (2026-09-22): the ceiling is a backstop
// for a wrong cost estimate, not the primary gate — memory is.
const gibPerJob = 10

// DefaultMaxJobs is the job ceiling when -admit-max-jobs is left at its
// platform default: min(performance cores, physical memory / 10 GiB), floored
// at 1. It is always >= 1: zero is how the controller is *disabled*, never
// how it is sized.
func DefaultMaxJobs() int {
	cores, mem := platformShape()
	return maxJobsFor(cores, mem)
}

// maxJobsFor is the pure rule behind DefaultMaxJobs.
func maxJobsFor(cores int, memBytes uint64) int {
	n := cores
	byMem := memBytes / (gibPerJob << 30)
	if n <= 0 || byMem < uint64(n) {
		n = int(byMem) //nolint:gosec // G115: bounded by the core count, or by RAM/10 GiB — a few dozen at most
	}
	if n < 1 {
		return 1
	}
	return n
}
