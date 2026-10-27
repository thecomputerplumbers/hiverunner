package host

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// said is a log that can be read while an Awake writes to it.
type said struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *said) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

// lines is how many lines say what.
func (s *said) lines(what string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Count(s.buf.String(), what)
}

// signalling is an Awake whose inhibitor is the shell script given, which
// says it holds the way the Linux one does: a line on its stdout.
func signalling(script string) (*Awake, *said) {
	log := &said{}
	return &Awake{
		log:     slog.New(slog.NewTextHandler(log, nil)),
		command: func() *exec.Cmd { return exec.Command("sh", "-c", script) },
		signals: true,
		stop:    func(cmd *exec.Cmd) { _ = cmd.Process.Kill() },
	}, log
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

const keeping = "keeping this machine awake while it runs jobs"

func TestTheMachineIsSaidToBeKeptAwakeOnlyOnceTheLockIsHeld(t *testing.T) {
	dir := t.TempDir()
	a, log := signalling("while [ ! -e " + dir + "/go ]; do sleep 0.01; done; echo holding; exec sleep 60")
	a.Hold()
	time.Sleep(100 * time.Millisecond)
	if log.lines(keeping) != 0 {
		t.Fatal("said it kept the machine awake before the lock was taken")
	}
	if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	eventually(t, "never said it kept the machine awake once the lock was taken", func() bool { return log.lines(keeping) == 1 })
	a.Release()
	if log.lines("may sleep again") != 1 {
		t.Fatal("the last release of a held lock did not say the machine may sleep again")
	}
}

// logind refusing the lock is said once, with what systemd-inhibit said,
// and not again for every job after it; a lock taken since starts over.
func TestARefusedLockIsSaidOnceWithWhy(t *testing.T) {
	dir := t.TempDir()
	a, log := signalling("if [ -e " + dir + "/allowed ]; then echo holding; exec sleep 60; fi; echo 'Failed to inhibit: Access denied' >&2; exit 1")
	const failed = "could not keep this machine awake"
	for job := 1; job <= 3; job++ {
		a.Hold()
		eventually(t, "a refused lock was not noticed", func() bool {
			a.mu.Lock()
			defer a.mu.Unlock()
			return a.cmd == nil
		})
		a.Release()
	}
	if n := log.lines(failed); n != 1 {
		t.Fatalf("three refused jobs said so %d times, want 1", n)
	}
	if log.lines("Failed to inhibit: Access denied") != 1 {
		t.Fatal("did not say what systemd-inhibit said")
	}
	if log.lines(keeping) != 0 || log.lines("may sleep again") != 0 {
		t.Fatal("a lock never taken was said to be held or let go")
	}

	if err := os.WriteFile(filepath.Join(dir, "allowed"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a.Hold()
	eventually(t, "the lock, once allowed, was not taken", func() bool { return log.lines(keeping) == 1 })
	a.Release()
	if err := os.Remove(filepath.Join(dir, "allowed")); err != nil {
		t.Fatal(err)
	}
	a.Hold()
	eventually(t, "a refusal after a lock held was not said", func() bool { return log.lines(failed) == 2 })
	a.Release()
}

// What Prepare found and said already is not said again by the first job.
func TestARefusalPrepareSaidIsNotSaidAgain(t *testing.T) {
	a, log := signalling("echo 'Failed to inhibit: Access denied' >&2; exit 1")
	asked := 0
	a.allow = func(*slog.Logger) bool { asked++; return false }
	a.Prepare()
	if asked != 1 {
		t.Fatalf("Prepare asked %d times, want 1", asked)
	}
	a.Hold()
	eventually(t, "a refused lock was not noticed", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.cmd == nil
	})
	a.Release()
	if n := log.lines("could not keep this machine awake"); n != 0 {
		t.Fatalf("the first job said again what Prepare had (%d times)", n)
	}
}

// A lock that was held and then lost is always said.
func TestALockLostWhileHeldIsSaid(t *testing.T) {
	a, log := signalling("echo holding; exec sleep 60")
	a.failing = true
	a.Hold()
	defer a.Release()
	eventually(t, "the lock was not taken", func() bool { return log.lines(keeping) == 1 })
	a.mu.Lock()
	cmd := a.cmd
	a.mu.Unlock()
	_ = cmd.Process.Kill()
	eventually(t, "a lock lost while held was not said", func() bool { return log.lines("stopped keeping this machine awake") == 1 })
}
