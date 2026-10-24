//go:build linux

package host

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// awakeCommand takes a logind inhibitor lock against sleep and idle for as
// long as it runs. What it runs under the lock waits for hived, so the lock
// ends with hived even if nothing stops it (Pdeathsig covers the inhibitor
// itself). A machine without systemd has no lock to take, and is left as it
// is.
func awakeCommand() *exec.Cmd {
	inhibit, err := exec.LookPath("systemd-inhibit")
	if err != nil {
		return nil
	}
	cmd := exec.Command(inhibit, "--what=sleep:idle", "--who=hived", "--why=running a GitHub Actions job", "--mode=block",
		"tail", "--pid="+strconv.Itoa(os.Getpid()), "-f", "/dev/null")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	return cmd
}

// stopAwake ends the inhibitor and what it runs, which share a group.
func stopAwake(cmd *exec.Cmd) {
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
