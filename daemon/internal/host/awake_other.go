//go:build !darwin && !linux

package host

import "os/exec"

func awakeCommand() *exec.Cmd { return nil }

func stopAwake(cmd *exec.Cmd) { _ = cmd.Process.Kill() }
