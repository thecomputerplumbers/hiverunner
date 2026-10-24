//go:build darwin

package host

import (
	"os"
	"os/exec"
	"strconv"
)

// awakeCommand asserts against idle sleep (-i), and against sleep at all
// on AC power (-s), until hived itself exits (-w).
func awakeCommand() *exec.Cmd {
	return exec.Command("/usr/bin/caffeinate", "-i", "-s", "-w", strconv.Itoa(os.Getpid()))
}

func stopAwake(cmd *exec.Cmd) { _ = cmd.Process.Kill() }
