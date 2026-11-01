package constant

import "os/exec"

const bin = "/bin/" + "launchctl"

func run() error { return exec.CommandContext(nil, bin, "list").Run() }
