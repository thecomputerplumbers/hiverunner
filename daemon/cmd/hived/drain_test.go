package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
)

// quick is a drain that looks often and believes at once.
func quick(jobs func() int, said *[]string) drainOptions {
	return drainOptions{
		Limit: 5 * time.Second, Every: 5 * time.Millisecond,
		Jobs: jobs, Say: func(text string) { *said = append(*said, text) },
	}
}

func paused(t *testing.T) bool {
	t.Helper()
	current, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	return current.Paused
}

func TestADrainWaitsForTheJobsInHandAndTheNextDaemonLiftsThePause(t *testing.T) {
	t.Setenv("HIVE_CONFIG", filepath.Join(t.TempDir(), "machine.json"))
	var left atomic.Int32
	left.Store(2)
	var said []string
	done := make(chan error, 1)
	go func() {
		done <- drain(context.Background(), quick(func() int { return int(left.Load()) }, &said))
	}()
	// While a job runs the drain waits, with the machine taking no more.
	time.Sleep(60 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("returned with two jobs running: %v", err)
	default:
	}
	if !paused(t) {
		t.Fatal("the machine was still taking jobs while it was drained")
	}
	left.Store(1)
	time.Sleep(60 * time.Millisecond)
	left.Store(0)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Still paused: the daemon has yet to be replaced.
	if !paused(t) {
		t.Fatal("taking jobs again before the daemon was restarted")
	}
	text := strings.Join(said, "\n")
	for _, want := range []string{"Waiting for 2 jobs", "Waiting for 1 job ", "hived update --now", "No job is running now."} {
		if !strings.Contains(text, want) {
			t.Errorf("did not say %q:\n%s", want, text)
		}
	}
	// The daemon that starts next lifts it.
	if err := endDrain(); err != nil {
		t.Fatal(err)
	}
	if paused(t) {
		t.Fatal("left paused after the daemon started")
	}
}

func TestADrainThatGivesUpLeavesTheMachineAsItWas(t *testing.T) {
	t.Setenv("HIVE_CONFIG", filepath.Join(t.TempDir(), "machine.json"))
	var said []string
	// Out of time.
	options := quick(func() int { return 1 }, &said)
	options.Limit = 30 * time.Millisecond
	if err := drain(context.Background(), options); err == nil || !strings.Contains(err.Error(), "left as it is") {
		t.Fatalf("got %v", err)
	}
	if paused(t) {
		t.Fatal("left paused after giving up")
	}
	// Interrupted.
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	if err := drain(ctx, quick(func() int { return 1 }, &said)); err == nil {
		t.Fatal("an interrupted drain reported success")
	}
	if paused(t) {
		t.Fatal("left paused after being interrupted")
	}
}

func TestADrainLeavesAnOwnersOwnPauseAlone(t *testing.T) {
	t.Setenv("HIVE_CONFIG", filepath.Join(t.TempDir(), "machine.json"))
	if err := settings.Save(settings.Settings{Paused: true, MaxJobs: 1}); err != nil {
		t.Fatal(err)
	}
	var said []string
	if err := drain(context.Background(), quick(func() int { return 0 }, &said)); err != nil {
		t.Fatal(err)
	}
	if err := endDrain(); err != nil {
		t.Fatal(err)
	}
	if !paused(t) {
		t.Fatal("a machine its owner paused was resumed by an update")
	}
	if len(said) != 0 {
		t.Errorf("said %q of a machine with no job", said)
	}
}

func TestADrainDoesNotBelieveAnEmptyMachineStraightAfterPausingIt(t *testing.T) {
	t.Setenv("HIVE_CONFIG", filepath.Join(t.TempDir(), "machine.json"))
	var said []string
	options := quick(func() int { return 0 }, &said)
	options.Settle = 80 * time.Millisecond
	started := time.Now()
	if err := drain(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	// A job accepted just before the pause has no VM yet.
	if waited := time.Since(started); waited < options.Settle {
		t.Errorf("believed it after %s", waited)
	}
}

func TestAJobInHandIsAVMThatWasHandedOne(t *testing.T) {
	work := t.TempDir()
	for _, file := range []string{
		// A VM kept warm for the next job, one running a job, and what
		// else lives there.
		"job-1/out/ready",
		"job-2/out/ready", "job-2/in/clock",
		"notes/in/clock", ".lock",
		// One an older daemon is running, while the installer drains it.
		"job-3/share/ready", "job-3/share/clock",
		// A link a guest left is not a clock the host wrote.
		"job-4/out/ready",
	} {
		full := filepath.Join(work, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(work, "job-4", "in"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(work, ".lock"), filepath.Join(work, "job-4", "in", "clock")); err != nil {
		t.Fatal(err)
	}
	if got := vmJobs(work); got != 2 {
		t.Errorf("counted %d jobs where two VMs have one", got)
	}
}
