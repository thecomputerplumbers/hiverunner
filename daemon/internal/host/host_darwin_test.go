//go:build darwin

package host

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPmsetValue(t *testing.T) {
	settings := "System-wide power settings:\n SleepDisabled\t\t1\nCurrently in use:\n standby              1\n sleep                10 (sleep prevented by powerd)\n displaysleep         2\n"
	for name, want := range map[string]string{"SleepDisabled": "1", "sleep": "10", "displaysleep": "2", "autorestart": ""} {
		if got := pmsetValue(settings, name); got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
}

// caffeinate holds the assertions for as long as hived lives, and no longer.
func TestTheMacIsKeptAwakeUntilHivedExits(t *testing.T) {
	cmd := awakeCommand()
	if cmd.Path != "/usr/bin/caffeinate" {
		t.Fatalf("inhibitor is %s", cmd.Path)
	}
	want := []string{"/usr/bin/caffeinate", "-i", "-s", "-w", strconv.Itoa(os.Getpid())}
	if !slices.Equal(cmd.Args, want) {
		t.Fatalf("args = %v, want %v", cmd.Args, want)
	}
}

// The real thing: while held, macOS lists caffeinate's assertion against
// idle sleep; once released, it is gone.
func TestMacOSSeesTheAssertionOnlyWhileHeld(t *testing.T) {
	asserted := func(pid int) bool {
		out, err := exec.Command("/usr/bin/pmset", "-g", "assertions").Output()
		if err != nil {
			t.Skipf("pmset would not say: %v", err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "pid "+strconv.Itoa(pid)+"(caffeinate)") && strings.Contains(line, "PreventUserIdleSystemSleep") {
				return true
			}
		}
		return false
	}
	a := NewAwake(slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.Hold()
	a.mu.Lock()
	cmd := a.cmd
	a.mu.Unlock()
	if cmd == nil {
		t.Fatal("no caffeinate was started")
	}
	pid := cmd.Process.Pid
	// caffeinate takes a moment to make its assertion.
	for i := 0; i < 100 && !asserted(pid); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if !asserted(pid) {
		t.Fatal("macOS does not list caffeinate's assertion while a job is held")
	}
	a.Release()
	if asserted(pid) {
		t.Fatal("the assertion outlived the last release")
	}
}
