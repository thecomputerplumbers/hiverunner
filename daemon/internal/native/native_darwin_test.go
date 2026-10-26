//go:build darwin

package native

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// A clean's list of what to stop leaves out the clean itself, and the clean
// signals by pid alone: kill(-1) on a Mac signals the caller too, which is
// how a clean once stopped itself and never finished (#132). Nothing here
// is signalled but -1 and 0, which are refused before any kill.
func TestDarwinProcessesLeavesOutTheClean(t *testing.T) {
	sleep := exec.Command("/bin/sleep", "30")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleep.Process.Kill(); _ = sleep.Wait() })
	system := darwinProcesses{uid: uint32(os.Getuid()), self: os.Getpid()}
	left, err := system.Left()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range left {
		if p.PID == os.Getpid() {
			t.Fatal("the clean is on its own list")
		}
		found = found || p.PID == sleep.Process.Pid
	}
	if !found {
		t.Fatalf("the child %d is not on the list", sleep.Process.Pid)
	}
	for _, pid := range []int{-1, 0} {
		if err := system.Signal(pid, syscall.Signal(0)); err == nil {
			t.Fatalf("signalled %d", pid)
		}
	}
}
