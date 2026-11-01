//go:build linux

package sysmem

// SelfCheckLine reports nothing on Linux: memory comes from /proc VmRSS, a
// stable interface, so there is no private ABI to watch at start (D7 is about
// darwin's proc_pid_rusage) and a permanent "n/a" line would be noise.
func SelfCheckLine() (string, bool) { return "", false }
