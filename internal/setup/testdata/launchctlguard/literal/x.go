package literal

import "os/exec"

func run() error { return exec.Command("launchctl", "list").Run() }
