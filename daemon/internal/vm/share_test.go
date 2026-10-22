//go:build darwin || linux

package vm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/jobs"
)

// fakeRunner is a runner whose VMs are stood in for by guest: called once
// the job has been handed over, with the VM, and the VM powers itself off
// when it returns. before, if set, is called as the VM boots, before the
// job is handed over.
func fakeRunner(t *testing.T, before, guest func(vm *instance)) *Runner {
	t.Helper()
	hostNetworks = func() ([]netip.Prefix, error) { return nil, nil }
	outPoll = 5 * time.Millisecond
	t.Cleanup(func() {
		hostNetworks = defaultHostNetworks
		outPoll = time.Second
	})
	r := &Runner{
		Image:   Image{ImageVersion: MinImageVersion, RunnerVersion: "2.300.0"},
		WorkDir: t.TempDir(),
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		latest:  func(context.Context) (string, error) { return "2.400.0", nil },
	}
	r.fakeVM = func(vm *instance) error {
		var once sync.Once
		off := func() { once.Do(func() { close(vm.ended) }) }
		vm.stop = off
		if before != nil {
			before(vm)
		}
		go func() {
			for {
				for _, name := range []string{"jitconfig", "selftest", "nothing-to-do"} {
					if _, err := os.Lstat(filepath.Join(vm.in, name)); err == nil {
						if guest != nil {
							guest(vm)
						}
						off()
						return
					}
				}
				if !vm.alive() {
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
		return nil
	}
	return r
}

var defaultHostNetworks = hostNetworks

// result writes one of the guest's results the way the agent does.
func result(t *testing.T, vm *instance, name, value string) {
	t.Helper()
	temporary := filepath.Join(vm.out, name+".tmp")
	if err := os.WriteFile(temporary, []byte(value), 0o600); err != nil {
		t.Error(err)
	}
	if err := os.Rename(temporary, filepath.Join(vm.out, name)); err != nil {
		t.Error(err)
	}
}

// hostSecret is a file of the host's, outside any share, that a guest
// would like read or written.
func hostSecret(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// within fails the test if run takes longer than limit: a FIFO must never
// be waited on.
func within[T any](t *testing.T, limit time.Duration, run func() T) T {
	t.Helper()
	done := make(chan T, 1)
	go func() { done <- run() }()
	select {
	case got := <-done:
		return got
	case <-time.After(limit):
		t.Fatal("blocked")
		panic("unreachable")
	}
}

func TestTheJobIsHandedOverWhereTheGuestCannotWriteBack(t *testing.T) {
	var handed []string
	var stillThere bool
	r := fakeRunner(t, nil, func(vm *instance) {
		entries, _ := os.ReadDir(vm.in)
		for _, entry := range entries {
			handed = append(handed, entry.Name())
		}
		// The agent says it has the job, and the host takes it away.
		result(t, vm, "taken", "")
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Lstat(filepath.Join(vm.in, "jitconfig")); errors.Is(err, os.ErrNotExist) {
				break
			}
			time.Sleep(time.Millisecond)
		}
		_, err := os.Lstat(filepath.Join(vm.in, "jitconfig"))
		stillThere = err == nil
		result(t, vm, "exit", "0\n")
	})
	got, err := r.Run(context.Background(), jobs.Job{AssignmentID: "a-1", Pool: "p", VCPUs: 2, JITConfig: "credential", Label: "x64"})
	if err != nil || got.ExitCode != 0 {
		t.Fatalf("Run() = %+v, %v", got, err)
	}
	if strings.Join(handed, " ") != "clock host jitconfig label" {
		t.Errorf("handed %v", handed)
	}
	if stillThere {
		t.Error("the job's configuration was left where the guest can read it after it said it had")
	}
}

// What a guest can leave under a name the host reads back.
var resultTraps = []struct {
	name  string
	plant func(t *testing.T, at string)
}{
	{"a link to a host file", func(t *testing.T, at string) {
		if err := os.Symlink(hostSecret(t, "7\n"), at); err != nil {
			t.Fatal(err)
		}
	}},
	{"a hard link to a host file", func(t *testing.T, at string) {
		if err := os.Link(hostSecret(t, "7\n"), at); err != nil {
			t.Fatal(err)
		}
	}},
	{"a link to a device that never ends", func(t *testing.T, at string) {
		if err := os.Symlink("/dev/zero", at); err != nil {
			t.Fatal(err)
		}
	}},
	{"a FIFO", func(t *testing.T, at string) {
		if err := unix.Mkfifo(at, 0o600); err != nil {
			t.Fatal(err)
		}
	}},
	{"a directory", func(t *testing.T, at string) {
		if err := os.Mkdir(at, 0o700); err != nil {
			t.Fatal(err)
		}
	}},
	{"a file longer than a result", func(t *testing.T, at string) {
		if err := os.WriteFile(at, append([]byte("7\noom=1\n"), bytes.Repeat([]byte(" "), 100<<10)...), 0o600); err != nil {
			t.Fatal(err)
		}
	}},
}

func TestWhatTheGuestLeavesForTheHostIsNeverFollowed(t *testing.T) {
	for _, trap := range resultTraps {
		t.Run(trap.name, func(t *testing.T) {
			r := fakeRunner(t, nil, func(vm *instance) {
				for _, name := range []string{"exit", "diag", "selftest.out"} {
					trap.plant(t, filepath.Join(vm.out, name))
				}
			})
			type outcome struct {
				report string
				err    error
			}
			got := within(t, 10*time.Second, func() outcome {
				report, err := r.SelfTest(context.Background(), 2)
				return outcome{report, err}
			})
			if got.report != "" {
				t.Errorf("the report is %q", got.report)
			}
			if got.err == nil || !strings.Contains(got.err.Error(), "without reporting a result") {
				t.Errorf("SelfTest() = %v; want no result read", got.err)
			}

			// And as a job: no exit read, so nothing latched and nothing
			// classified from the host's file.
			r = fakeRunner(t, nil, func(vm *instance) {
				for _, name := range []string{"exit", "diag"} {
					trap.plant(t, filepath.Join(vm.out, name))
				}
			})
			result := within(t, 10*time.Second, func() jobs.Result {
				result, _ := r.Run(context.Background(), jobs.Job{AssignmentID: "a-1", Pool: "p", VCPUs: 2, JITConfig: "c"})
				return result
			})
			if result.ExitCode != -1 || result.Reason == jobs.ReasonGuestOOM {
				t.Errorf("Run() = %+v", result)
			}
			if reason := r.Unusable(); reason != "" {
				t.Errorf("declining jobs: %s", reason)
			}
		})
	}
}

// Where virtiofsd cannot serve what the guest is handed read-only, the
// guest can leave things there too, before it is handed its job and while
// it is stopped.
func TestWhatTheGuestLeavesWhereItIsHandedTheJobIsNotWrittenThrough(t *testing.T) {
	secrets := map[string]string{}
	r := fakeRunner(t, func(vm *instance) {
		for _, name := range []string{"clock", "host", "label", "jitconfig", "stop-reason"} {
			secrets[name] = hostSecret(t, "the host's own "+name+"\n")
			if err := os.Symlink(secrets[name], filepath.Join(vm.in, name)); err != nil {
				t.Fatal(err)
			}
		}
	}, func(vm *instance) {
		// A guest that never stops: it is asked to, then stopped.
		<-vm.ended
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	type outcome struct {
		result jobs.Result
		err    error
	}
	got := within(t, 30*time.Second, func() outcome {
		result, err := r.Run(ctx, jobs.Job{AssignmentID: "a-1", Pool: "p", VCPUs: 2, JITConfig: "c", Label: "x64"})
		return outcome{result, err}
	})
	if !errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("Run() = %+v, %v", got.result, got.err)
	}
	for name, path := range secrets {
		if raw, err := os.ReadFile(path); err != nil || string(raw) != "the host's own "+name+"\n" {
			t.Errorf("%s was written through to the host's file: %q, %v", name, raw, err)
		}
	}
}

func TestOneGuestCannotStopAMachineTakingJobs(t *testing.T) {
	staleFor = 50 * time.Millisecond
	t.Cleanup(func() { staleFor = time.Hour })
	refuse := func(vm *instance) { result(t, vm, "exit", strconv.Itoa(ExitRunnerDeprecated)+"\n") }
	for name, test := range map[string]struct {
		latest   func(context.Context) (string, error)
		selftest bool
		declined bool
	}{
		"GitHub has a newer runner": {
			latest:   func(context.Context) (string, error) { return "2.400.0", nil },
			declined: true,
		},
		"the image's runner is GitHub's latest": {
			latest: func(context.Context) (string, error) { return "2.300.0", nil },
		},
		"GitHub cannot be asked": {
			latest: func(context.Context) (string, error) { return "", errors.New("offline") },
		},
		"a self-test": {
			latest:   func(context.Context) (string, error) { return "2.400.0", nil },
			selftest: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := fakeRunner(t, nil, refuse)
			r.latest = test.latest
			r.preflight = func() error { return nil }
			if test.selftest {
				_, _ = r.SelfTest(context.Background(), 2)
			} else {
				_, _ = r.Run(context.Background(), jobs.Job{AssignmentID: "a-1", Pool: "p", VCPUs: 2, JITConfig: "c"})
			}
			reason := r.Unusable()
			if declined := reason == ReasonStaleImage; declined != test.declined {
				t.Fatalf("Unusable() = %q", reason)
			}
			if !test.declined {
				return
			}
			// Not for good: the machine is asked again after a while.
			time.Sleep(2 * staleFor)
			if reason := r.Unusable(); reason != "" {
				t.Errorf("still declining after the latch expired: %q", reason)
			}
		})
	}
}

func TestAGuestThatFillsWhereResultsGoIsStopped(t *testing.T) {
	r := fakeRunner(t, nil, func(vm *instance) {
		for i := range mostOutEntries + 1 {
			if err := os.WriteFile(filepath.Join(vm.out, fmt.Sprint("f", i)), nil, 0o600); err != nil {
				t.Error(err)
			}
		}
		<-vm.ended
	})
	err := within(t, 30*time.Second, func() error {
		_, err := r.Run(context.Background(), jobs.Job{AssignmentID: "a-1", Pool: "p", VCPUs: 2, JITConfig: "c"})
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "more where its results go") {
		t.Fatalf("Run() = %v", err)
	}
}

func TestAnImageWhoseGuestWritesWhereItIsHandedTheJobIsNotRun(t *testing.T) {
	booted := false
	r := fakeRunner(t, func(*instance) { booted = true }, nil)
	r.Image.ImageVersion = MinImageVersion - 1
	r.preflight = func() error { return nil }
	if reason := r.Unusable(); reason != ReasonOldImage {
		t.Errorf("Unusable() = %q", reason)
	}
	if _, err := r.Run(context.Background(), jobs.Job{AssignmentID: "a-1", Pool: "p", VCPUs: 2, JITConfig: "c"}); err == nil || booted {
		t.Errorf("Run() = %v, booted %v", err, booted)
	}
	r.tend(true)
	if r.warm != nil || booted {
		t.Error("kept a VM warm from an image it will not run jobs in")
	}
}

func TestOnlyTheEndOfTheConsoleIsRead(t *testing.T) {
	dir := t.TempDir()
	vm := &instance{dir: dir, out: filepath.Join(dir, "out"), console: filepath.Join(dir, "console.log"), ended: make(chan struct{})}
	if err := os.Mkdir(vm.out, 0o700); err != nil {
		t.Fatal(err)
	}
	// What the guest said long ago is gone from what is read; what it said
	// last is not.
	console := "Kernel panic - not syncing\n" + strings.Repeat("x", mostConsole) + "\nOut of memory: Killed process 1\n"
	if err := os.WriteFile(vm.console, []byte(console), 0o600); err != nil {
		t.Fatal(err)
	}
	end := classifyStop(vm, jobs.Job{}, 137, true, context.Background(), nil)
	if end.Reason != jobs.ReasonGuestOOM {
		t.Errorf("classifyStop() = %+v", end)
	}
	keep := t.TempDir()
	t.Setenv("HIVE_KEEP_CONSOLE", keep)
	saved := vm.keepConsole()
	info, err := os.Stat(saved)
	if err != nil || info.Size() != int64(len(console)) {
		t.Errorf("kept %s: %v, %v", saved, info, err)
	}
}
