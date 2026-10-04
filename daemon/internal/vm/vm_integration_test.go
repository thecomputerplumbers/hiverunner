//go:build darwin

package vm

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// These boot real virtual machines, so they need the built guest image and a
// test binary signed with the virtualization entitlement. Run them with
// daemon/test-vm.sh; plain `go test` skips them.
func realRunner(t *testing.T) *Runner {
	t.Helper()
	if os.Getenv("HIVE_VM_TEST") == "" {
		t.Skip("set HIVE_VM_TEST=1 and run via daemon/test-vm.sh")
	}
	dir, err := DefaultImageDir()
	if err != nil {
		t.Fatal(err)
	}
	image, err := LoadImage(dir)
	if err != nil {
		t.Fatalf("build the guest image first (daemon/image/build.sh): %v", err)
	}
	// On the image's volume, so the disk is cloned rather than copied.
	work, err := os.MkdirTemp(dir, ".test-jobs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(work) })
	return &Runner{Image: image, WorkDir: work, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func leftovers(t *testing.T, r *Runner) int {
	t.Helper()
	entries, err := os.ReadDir(r.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestAVMBootsReportsAndLeavesNothingBehind(t *testing.T) {
	r := realRunner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	started := time.Now()
	report, err := r.SelfTest(ctx, 2)
	if err != nil {
		t.Fatalf("self-test: %v\n%s", err, report)
	}
	for _, want := range []string{"CPUS=2", "USER=runner", "NETWORK=ok", "RUNNER=" + r.Image.RunnerVersion} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	if n := leftovers(t, r); n != 0 {
		t.Errorf("%d job directories left behind", n)
	}
	t.Logf("boot to destroyed in %s", time.Since(started).Round(100*time.Millisecond))
}

// A job that is released, or runs out of time, takes its VM with it.
func TestCancellationDestroysTheVM(t *testing.T) {
	r := realRunner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	started := time.Now()
	// A file the guest agent does not act on: the VM boots and waits.
	code, err := r.run(ctx, "held", 2, "nothing-to-do", "", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run() = %d, %v; want a deadline error", code, err)
	}
	if code == 0 {
		t.Error("a destroyed VM reported success")
	}
	if elapsed := time.Since(started); elapsed > 25*time.Second {
		t.Errorf("destroying the VM took %s", elapsed)
	}
	if n := leftovers(t, r); n != 0 {
		t.Errorf("%d job directories left behind", n)
	}
}

// The guest sees one directory. What the host put there is all there is.
func TestTheShareHoldsOnlyTheJob(t *testing.T) {
	r := realRunner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var names []string
	_, err := r.run(ctx, "share", 1, "selftest", "1", func(share string) {
		entries, _ := os.ReadDir(share)
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"clock": true, "ready": true, "selftest": true, "selftest.out": true, "exit": true}
	for _, name := range names {
		if !allowed[name] {
			t.Errorf("unexpected file in the share: %s", name)
		}
	}
}

// A VM kept booted takes a job without the boot, and is replaced afterwards.
func TestAWarmVMTakesTheJob(t *testing.T) {
	r := realRunner(t)
	r.WarmVCPUs = 2
	tending, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Tend(tending, func() bool { return true }); close(done) }()
	defer func() { stop(); <-done }()

	warm := func() *instance {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.warm
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); !ok(); {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitFor("a warm VM", func() bool { return warm() != nil })
	first := warm()
	// Let the guest reach the point where it is waiting for a job.
	time.Sleep(4 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cold := time.Now()
	if _, err := (&Runner{Image: r.Image, WorkDir: r.WorkDir, Log: r.Log}).SelfTest(ctx, 2); err != nil {
		t.Fatal(err)
	}
	coldTook := time.Since(cold)

	started := time.Now()
	report, err := r.SelfTest(ctx, 2)
	if err != nil {
		t.Fatalf("self-test on the warm VM: %v\n%s", err, report)
	}
	warmTook := time.Since(started)
	if !strings.Contains(report, "NETWORK=ok") {
		t.Errorf("warm VM report:\n%s", report)
	}
	t.Logf("cold %s, warm %s", coldTook.Round(10*time.Millisecond), warmTook.Round(10*time.Millisecond))
	if warmTook >= coldTook {
		t.Errorf("the warm VM (%s) was no faster than a cold boot (%s)", warmTook, coldTook)
	}

	// A different size cannot use it; the warm VM is left alone.
	waitFor("a replacement warm VM", func() bool { return warm() != nil && warm() != first })
	second := warm()
	if _, err := r.SelfTest(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if warm() != second {
		t.Error("a job of another size took the warm VM")
	}

	// Shutdown: when Tend returns, the warm VM is already gone, files and
	// all. No waiting: the daemon exits right after this.
	stop()
	<-done
	if warm() != nil {
		t.Error("Tend returned with a warm VM still held")
	}
	if n := leftovers(t, r); n != 0 {
		t.Errorf("Tend returned with %d job directories still on disk", n)
	}
	// Let the deferred stop and wait be harmless.
	done = make(chan struct{})
	close(done)
}

// When unavailable, such as with the lid closed, no VM is kept waiting.
func TestNoWarmVMWhenUnavailable(t *testing.T) {
	r := realRunner(t)
	r.WarmVCPUs = 2
	var available atomic.Bool
	available.Store(true)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Tend(ctx, available.Load); close(done) }()
	defer func() { stop(); <-done }()

	held := func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.warm != nil
	}
	until := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); !ok(); {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	until("a warm VM", held)
	available.Store(false)
	until("the warm VM to be destroyed", func() bool { return !held() && leftovers(t, r) == 0 })
	available.Store(true)
	until("a warm VM again", held)
}
