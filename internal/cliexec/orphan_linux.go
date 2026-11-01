package cliexec

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// readPPID reads pid's parent from /proc/<pid>/stat. The command name (field
// 2) may hold spaces and parentheses, so the fields are read after the last
// ')': state, then ppid. ENOENT means the process is gone.
func readPPID(pid int) (ppid int, alive bool, err error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, err
	}
	s := string(raw)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false, fmt.Errorf("unreadable /proc/%d/stat", pid)
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0, false, fmt.Errorf("short /proc/%d/stat", pid)
	}
	p, err := strconv.Atoi(f[1])
	if err != nil {
		return 0, false, err
	}
	return p, true, nil
}
