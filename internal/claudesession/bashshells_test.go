package claudesession

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/testutil"
)

func TestBashToolShells(t *testing.T) {
	psTreeRun = func() ([]byte, error) {
		return []byte("  100     1 /usr/local/bin/claude --permission-mode plan\n" +
			"  200   100 /bin/zsh -c source /u/.claude-x/shell-snapshots/snapshot-zsh-1-a.sh 2>/dev/null || true && sleep 600\n" +
			"  300   100 node /mcp/server.js\n" +
			"  400   999 /bin/zsh -c source /u/.claude-x/shell-snapshots/snapshot-zsh-2-b.sh\n"), nil
	}
	defer func() { psTreeRun = defaultPsTree }()
	got, err := BashToolShells(100)
	if err != nil || len(got) != 1 || got[0] != 200 {
		t.Fatalf("shells = %v %v, want [200]", got, err)
	}
	psTreeRun = func() ([]byte, error) { return nil, errors.New("ps: boom") }
	if _, err := BashToolShells(100); err == nil {
		t.Fatal("a failed listing must be an error")
	}
}

// realBashToolLine is a real `ps -ww -o pid=,ppid=,etime=,command=` line of a
// Bash-tool shell, taken 2026-10-13 (pid, ppid and etime set to the fixture's,
// home shortened): Claude Code quotes a ' inside the command as '"'"'.
const realBashToolLine = `  230   100       03:15 /bin/zsh -c source /Users/u/.claude-home/shell-snapshots/snapshot-zsh-1791897729353-nd36ru.sh 2>/dev/null || true && setopt NO_EXTENDED_GLOB NO_BARE_GLOB_QUAL 2>/dev/null || true && { \builtin unalias -- 'unsetenv'; \builtin unset -f -- 'unsetenv'; } >/dev/null 2>&1 || true && eval 'ps -ww -o pid=,ppid=,etime=,command= -p $$ | cut -c1-900 # it'"'"'s '"'"'quoted'"'"' with awk -F'"'"'\t'"'"'' < /dev/null && pwd -P >| /tmp/claude-1875-cwd`

func TestBashToolShellInfo(t *testing.T) {
	psAgeRun = func() ([]byte, error) {
		return []byte("  100     1    01:00:00 /usr/local/bin/claude --permission-mode plan\n" +
			"  200   100       12:05 /bin/zsh -c source /u/.c/shell-snapshots/snapshot-zsh-1-a.sh 2>/dev/null || true && eval 'npm run dev' < /dev/null && pwd -P >| /tmp/claude-1-cwd\n" +
			"  210   100  1-02:03:04 /bin/zsh -c source /u/.c/shell-snapshots/snapshot-zsh-1-a.sh 2>/dev/null || true && eval 'echo '\\''hi'\\'' && sleep 9' < /dev/null && pwd -P >| /tmp/x\n" +
			"  220   100       00:07 /bin/zsh -c source /u/.c/shell-snapshots/snapshot-zsh-1-a.sh 2>/dev/null || true && eval 'tail -f /very/long/path\n" +
			realBashToolLine + "\n" +
			"  300   100       00:01 node /mcp/server.js\n" +
			"  400   999       00:01 /bin/zsh -c source /u/.c/shell-snapshots/snapshot-zsh-2-b.sh\n"), nil
	}
	defer func() { psAgeRun = defaultPsAge }()
	got, err := BashToolShellInfo(100)
	if err != nil {
		t.Fatal(err)
	}
	want := []ShellInfo{
		{PID: 200, Age: 12*time.Minute + 5*time.Second, Command: "npm run dev"},
		{PID: 210, Age: 26*time.Hour + 3*time.Minute + 4*time.Second, Command: "echo 'hi' && sleep 9"}, // '\'' form
		{PID: 220, Age: 7 * time.Second, Command: "tail -f /very/long/path"},                           // cut line: no closing quote
		{PID: 230, Age: 3*time.Minute + 15*time.Second, // '"'"' form, the one Claude Code writes
			Command: `ps -ww -o pid=,ppid=,etime=,command= -p $$ | cut -c1-900 # it's 'quoted' with awk -F'\t'`},
	}
	if len(got) != len(want) {
		t.Fatalf("shells = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("shell %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	psAgeRun = func() ([]byte, error) { return nil, errors.New("ps: boom") }
	if _, err := BashToolShellInfo(100); err == nil {
		t.Fatal("a failed listing must be an error, never none")
	}
}

// The real ps, on a real child shell shaped like the Bash tool's: the
// parsing holds against the macOS output, not only against a fixture.
func TestBashToolShellInfoRealPs(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("ps -axww -o etime= is checked on macOS, where pigtail runs")
	}
	snap := filepath.Join(t.TempDir(), "shell-snapshots", "snapshot-zsh-test.sh")
	cmd := exec.Command("/bin/zsh", "-c", "source "+snap+" 2>/dev/null || true && eval 'sleep 30' < /dev/null")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	testutil.Eventually(t, 30*time.Second, "the child shell listed as sleep 30", func() bool {
		got, err := BashToolShellInfo(os.Getpid())
		if err != nil {
			return false
		}
		for _, s := range got {
			if s.PID == cmd.Process.Pid && s.Command == "sleep 30" {
				return true
			}
		}
		return false
	})
	got, err := BashToolShellInfo(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got {
		if s.PID == cmd.Process.Pid {
			if s.Command != "sleep 30" || s.Age > time.Minute {
				t.Fatalf("shell = %+v, want command %q and a young age", s, "sleep 30")
			}
			return
		}
	}
	t.Fatalf("shell pid %d not found in %+v", cmd.Process.Pid, got)
}
