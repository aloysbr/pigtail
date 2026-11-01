package claudesession

import (
	"errors"
	"testing"
)

func stubPsInfo(t *testing.T, table map[int]struct {
	ppid int
	args string
}, fail int) {
	t.Helper()
	saved := psInfo
	t.Cleanup(func() { psInfo = saved })
	psInfo = func(pid int) (int, string, error) {
		if pid == fail {
			return 0, "", errors.New("ps boom")
		}
		e, ok := table[pid]
		if !ok {
			return 0, "", errors.New("no such process")
		}
		return e.ppid, e.args, nil
	}
}

func TestCallerClaude(t *testing.T) {
	type e = struct {
		ppid int
		args string
	}
	table := map[int]e{
		100: {50, "pigtail session spawn --subject sub_x --prompt /example-skill"},
		50:  {40, "/bin/zsh -c source /u/.claude/shell-snapshots/s.sh && eval 'pigtail session spawn'"},
		40:  {30, "/u/.nvm/versions/node/v22/bin/claude --settings /t/cmux-claude-settings.x --permission-mode auto"},
		30:  {1, "/bin/zsh -l"},
	}
	stubPsInfo(t, table, 0)
	p, ok, err := CallerClaude(100)
	if err != nil || !ok || p.PID != 40 {
		t.Fatalf("CallerClaude = %+v %v %v, want pid 40", p, ok, err)
	}
	if PermissionMode(p.Args) != "auto" {
		t.Fatalf("args not carried: %q", p.Args)
	}

	delete(table, 40)
	table[50] = e{30, table[50].args}
	stubPsInfo(t, table, 0)
	if _, ok, err := CallerClaude(100); err != nil || ok {
		t.Fatalf("no claude ancestor = %v %v, want not found, nil", ok, err)
	}

	stubPsInfo(t, table, 50)
	if _, _, err := CallerClaude(100); err == nil {
		t.Fatal("a failing ps must be an error, not 'no caller'")
	}
}

// Ancestors lists every parent of pid (excluded) up to init, for the Bash-tool
// shells /standby must not count: its own launching shell is one of them.
func TestAncestors(t *testing.T) {
	type e = struct {
		ppid int
		args string
	}
	stubPsInfo(t, map[int]e{100: {50, "pigtail"}, 50: {40, "/bin/zsh -c x"}, 40: {1, "claude"}}, 0)
	got, err := Ancestors(100)
	if err != nil || len(got) != 2 || got[0] != 50 || got[1] != 40 {
		t.Fatalf("Ancestors = %v, %v", got, err)
	}
	stubPsInfo(t, map[int]e{100: {50, "pigtail"}}, 50)
	if _, err := Ancestors(100); err == nil {
		t.Fatal("a ps failure is an error, never a shorter chain")
	}
	loop := map[int]e{}
	for i := 2; i < 200; i++ {
		loop[i] = e{i + 1, "x"}
	}
	stubPsInfo(t, loop, 0)
	if _, err := Ancestors(2); err == nil {
		t.Fatal("a chain deeper than 64 is an error")
	}
}
