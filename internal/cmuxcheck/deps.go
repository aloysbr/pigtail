package cmuxcheck

import (
	"os"
	"path/filepath"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/inputprobe"
)

// Deps are the check's seams. Tests replace every one; DefaultDeps touches
// ps, cmux and the transcripts.
type Deps struct {
	Procs      func() ([]claudesession.Proc, error)
	Environ    func(pid int) (string, error)
	Native     func(cfg string, pid int) (claudesession.Native, bool)
	Screen     func(surfaceID string) (string, error)
	Shells     func(pid int) ([]int, error)
	Transcript func(cfg, sid string) (string, bool)
	// TranscriptFound: does the account hold a transcript of sid (D2). An
	// error is a lookup failure, never "absent".
	TranscriptFound func(cfg, sid string) (bool, error)
	ModTime         func(path string) (time.Time, error)
	PS              func() (string, error)            // ps -axo pid,ppid,pgid,tpgid,tty,comm (shellIdle, D22)
	SurfaceTTYs     func() (map[string]string, error) // surface → tty from cmux tree, read only when a pane shell's env is unreadable (D22)
	DefaultMode     func(cfg string) (string, error)  // permissions.defaultMode of an account (F10)
	Now             func() time.Time
	// Focused names the surface holding cmux's keyboard focus (cmux identify,
	// bounded at 3 s): never typed into (T-b, D1). nil or an error = nothing typed.
	Focused func() (string, error)
	// ProbeInput probes one session's input (sub_x000000000000024): it writes
	// a terminal status query to the session's own tty, never a keystroke.
	// nil = no probe.
	ProbeInput func(inputprobe.Target) inputprobe.Result
	// TTYListing is one `ps -axo pid=,pgid=,tpgid=,stat=,tty=` for every claude
	// of a pass (inputprobe.ParseProcs reads it); TTYOf reads the terminal of
	// one pid and whether it alone reads it, again right before each write.
	// Either nil = no probe target: a tty is never guessed. Deps.SurfaceTTYs
	// (cmux tree, stale) is never read here.
	TTYListing func() ([]byte, error)
	TTYOf      func(pid int) (inputprobe.TTYInfo, error)
}

func DefaultDeps() Deps {
	return Deps{
		Procs:   claudesession.ClaudeProcesses,
		Environ: cmux.ProcessEnviron,
		Native:  claudesession.ReadNative,
		Screen:  cmux.ReadScreen,
		Shells:  claudesession.BashToolShells,
		Transcript: func(cfg, sid string) (string, bool) {
			t, ok, err := claudesession.Find(sid, []string{cfg})
			if err != nil || !ok {
				return "", false
			}
			return t.JSONLPath(), true
		},
		TranscriptFound: func(cfg, sid string) (bool, error) {
			ts, err := claudesession.FindAll(sid, []string{cfg})
			return len(ts) > 0, err
		},
		ModTime: func(p string) (time.Time, error) {
			st, err := os.Stat(p)
			if err != nil {
				return time.Time{}, err
			}
			return st.ModTime(), nil
		},
		PS:          psAll,
		SurfaceTTYs: cmux.SurfaceTTYs,
		DefaultMode: claudesession.DefaultPermissionMode,
		Now:         time.Now,
		Focused:     cmux.FocusedSurfaceID,
		ProbeInput: func(t inputprobe.Target) inputprobe.Result {
			return inputprobe.Probe(inputprobe.Deps{
				Counter: inputprobe.DefaultCounter,
				TTYOf:   inputprobe.TTYOf,
				Write:   inputprobe.WriteTTY,
				Sleep:   time.Sleep,
			}, inputprobe.DefaultParams(), t)
		},
		TTYListing: inputprobe.ListProcs,
		TTYOf:      inputprobe.TTYOf,
	}
}

func defaultConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}
