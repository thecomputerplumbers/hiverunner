//go:build !darwin && !linux

package host

import (
	"log/slog"
	"os/exec"
)

const awakeSignals = false

func awakeCommand() *exec.Cmd { return nil }

func stopAwake(cmd *exec.Cmd) { _ = cmd.Process.Kill() }

func allowAwake(*slog.Logger) bool { return true }
