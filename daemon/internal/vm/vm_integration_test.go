//go:build darwin

package vm

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
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
	code, err := r.run(ctx, "held", 2, "nothing-to-do", "")
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
	_, err := r.runWith(ctx, "share", 1, "selftest", "1", func(share string) {
		entries, _ := os.ReadDir(share)
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"clock": true, "selftest": true, "selftest.out": true, "exit": true}
	for _, name := range names {
		if !allowed[name] {
			t.Errorf("unexpected file in the share: %s", name)
		}
	}
}
