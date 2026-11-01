package clean

import (
	"os/exec"

	"github.com/aloysbr/pigtail/internal/lid"
)

var _ = lid.ExecRunner{}

func run() error { return exec.Command("ls").Run() }
