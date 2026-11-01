// Package watchdog detects and kills runaway dev servers: processes stuck
// pegging the CPU (e.g. a Turbopack recompile loop) under a port pigtail
// allocated to a workspace.
//
// Detection is based on the delta of accumulated CPU time over a wall-clock
// window — the real instantaneous load regardless of process age — sustained
// across multiple windows separated by a confirmation gap, so an idle server
// being browsed (~0% CPU) or a short legitimate HMR recompile is never killed.
package watchdog
