package host

import (
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

// sleeping is an Awake whose inhibitor is a plain sleep, counting what it
// started.
func sleeping() (*Awake, func() []*exec.Cmd) {
	var mu sync.Mutex
	var started []*exec.Cmd
	a := &Awake{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		command: func() *exec.Cmd {
			cmd := exec.Command("sleep", "60")
			mu.Lock()
			started = append(started, cmd)
			mu.Unlock()
			return cmd
		},
		stop: func(cmd *exec.Cmd) { _ = cmd.Process.Kill() },
	}
	return a, func() []*exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		return append([]*exec.Cmd(nil), started...)
	}
}

func alive(cmd *exec.Cmd) bool {
	return cmd.ProcessState == nil && cmd.Process.Signal(syscall.Signal(0)) == nil
}

func TestOneInhibitorLastsFromTheFirstHoldToTheLastRelease(t *testing.T) {
	a, started := sleeping()
	a.Hold()
	a.Hold()
	if len(started()) != 1 {
		t.Fatalf("two holds started %d inhibitors, want 1", len(started()))
	}
	inhibitor := started()[0]
	a.Release()
	if !alive(inhibitor) {
		t.Fatal("one release of two let the machine sleep")
	}
	a.Release()
	if inhibitor.ProcessState == nil {
		t.Fatal("the last release left the inhibitor running")
	}
	// A release with nothing held does nothing.
	a.Release()
	a.Hold()
	defer a.Release()
	if len(started()) != 2 || !alive(started()[1]) {
		t.Fatal("a hold after the last release did not keep the machine awake again")
	}
}

func TestAnInhibitorThatEndsIsStartedAgainByTheNextHold(t *testing.T) {
	a, started := sleeping()
	a.Hold()
	defer a.Release()
	_ = started()[0].Process.Kill()
	deadline := time.Now().Add(3 * time.Second)
	for {
		a.mu.Lock()
		gone := a.cmd == nil
		a.mu.Unlock()
		if gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the inhibitor's end was not noticed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.Hold()
	defer a.Release()
	if len(started()) != 2 || !alive(started()[1]) {
		t.Fatal("the next hold did not start the inhibitor again")
	}
}

func TestNoInhibitorIsNoHarm(t *testing.T) {
	a := &Awake{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a.Hold()
	a.Release()
	a.Release()
}
