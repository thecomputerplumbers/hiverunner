//go:build linux

package vm

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestThePeakIsReadInKiB(t *testing.T) {
	for _, c := range []struct {
		usage any
		want  uint64
	}{
		{&syscall.Rusage{Maxrss: 2048}, 2 << 20},
		{&syscall.Rusage{}, 0},
		{&syscall.Rusage{Maxrss: -1}, 0},
		{(*syscall.Rusage)(nil), 0},
		{nil, 0},
		{"not a rusage", 0},
	} {
		if got := maxRSSBytes(c.usage); got != c.want {
			t.Errorf("maxRSSBytes(%#v) = %d, want %d", c.usage, got, c.want)
		}
	}
}

func TestAProcessThatNeverRanHasNoPeak(t *testing.T) {
	if got := peakBytes(nil); got != 0 {
		t.Fatalf("peakBytes(nil) = %d", got)
	}
	if got := peakBytes(&os.ProcessState{}); got != 0 {
		t.Fatalf("peakBytes(empty) = %d", got)
	}
}

// The kernel keeps an exited child's high-water mark for its parent.
func TestAnExitedProcessHasAPeak(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip("no true to run:", err)
	}
	if got := peakBytes(cmd.ProcessState); got == 0 {
		t.Fatal("an exited process reported no peak")
	}
}
