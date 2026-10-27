//go:build darwin

package host

import (
	"log/slog"
	"os"
	"os/exec"
	"strconv"
)

// awakeSignals: caffeinate asserts once it starts.
const awakeSignals = false

// awakeCommand asserts against idle sleep (-i), and against sleep at all
// on AC power (-s), until hived itself exits (-w).
func awakeCommand() *exec.Cmd {
	return exec.Command("/usr/bin/caffeinate", "-i", "-s", "-w", strconv.Itoa(os.Getpid()))
}

func stopAwake(cmd *exec.Cmd) { _ = cmd.Process.Kill() }

// allowAwake: any process may make caffeinate's assertions.
func allowAwake(*slog.Logger) bool { return true }
