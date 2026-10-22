package macvm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/jobs"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/native"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func init() {
	// The runner the tests fetch.
	sum := sha256.Sum256([]byte("runner"))
	runnerSHA256 = hex.EncodeToString(sum[:])
	stopWait = 200 * time.Millisecond
	bootTimeout = 300 * time.Millisecond
	powerOffGrace = 200 * time.Millisecond
	stopGrace = 50 * time.Millisecond
	poll = 10 * time.Millisecond
}

// newImage is an image as Create leaves one: not sealed, no runner beside it.
func newImage(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "macos-image")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		HardwareModelFile: "model",
		AuxiliaryFile:     "aux",
		DiskFile:          "disk",
		IdentifierFile:    "identifier",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := (Image{Dir: dir, MacOS: "26.0 (25A354)", Created: time.Now()}).save(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// fetch stands in for GitHub: it writes a runner where the real one goes.
func fetch(dir string) (string, error) {
	path := filepath.Join(dir, native.RunnerFile)
	return path, os.WriteFile(path, []byte("runner"), 0o644)
}

func sealedImage(t *testing.T) string {
	t.Helper()
	dir := newImage(t)
	if _, err := Seal(dir, fetch, io.Discard); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAnImageRunsJobsOnlyOnceSealed(t *testing.T) {
	dir := newImage(t)
	image, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if problem := image.Problem(); !strings.Contains(problem, "not sealed") {
		t.Fatalf("a new image: %q", problem)
	}
	var said strings.Builder
	sealed, err := Seal(dir, fetch, &said)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed.Digest, "sha256:") || sealed.Problem() != "" {
		t.Fatalf("sealed: digest %q, problem %q", sealed.Digest, sealed.Problem())
	}
	if !strings.Contains(said.String(), "digest") || !strings.Contains(said.String(), native.RunnerVersion) {
		t.Errorf("Seal said %q", said.String())
	}
	loaded, err := Load(dir)
	if err != nil || loaded.Digest != sealed.Digest || loaded.Problem() != "" {
		t.Fatalf("read back: %+v %v", loaded, err)
	}

	// Sealing again reads it all again, and leaves a runner that is the one
	// pinned where it is.
	said.Reset()
	again, err := Seal(dir, func(string) (string, error) { return "", errors.New("fetched again") }, &said)
	if err != nil || again.Digest != sealed.Digest || !strings.Contains(said.String(), "digest") {
		t.Fatalf("sealed again: %q %v, said %q", again.Digest, err, said.String())
	}
	// A runner that is not is fetched again.
	if err := os.WriteFile(filepath.Join(dir, native.RunnerFile), []byte("someone else's"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Seal(dir, fetch, io.Discard); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, native.RunnerFile)); string(raw) != "runner" {
		t.Fatalf("the runner beside the image is %q", raw)
	}
}

func TestAnImageChangedSinceItWasSealedIsRefused(t *testing.T) {
	dir := sealedImage(t)
	before, _ := Load(dir)
	if err := os.WriteFile(filepath.Join(dir, DiskFile), []byte("disk, with Xcode on it"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if problem := changed.Problem(); !strings.Contains(problem, "changed since it was sealed") {
		t.Fatalf("a changed disk: %q", problem)
	}
	after, err := Seal(dir, fetch, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if after.Digest == before.Digest || after.Problem() != "" {
		t.Fatalf("resealed: %q (was %q), problem %q", after.Digest, before.Digest, after.Problem())
	}
}

func TestAnImageWithoutItsRunnerIsRefused(t *testing.T) {
	dir := sealedImage(t)
	if err := os.Remove(filepath.Join(dir, native.RunnerFile)); err != nil {
		t.Fatal(err)
	}
	image, _ := Load(dir)
	if problem := image.Problem(); !strings.Contains(problem, native.RunnerVersion) {
		t.Fatalf("no runner: %q", problem)
	}
}

func TestNoImageSaysHowToMakeOne(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "macos-image")); !errors.Is(err, ErrNoImage) {
		t.Fatalf("Load() = %v, want ErrNoImage", err)
	}
}

// An image that is open is not sealed, removed or cloned from under it.
func TestAnOpenImageIsLeftAlone(t *testing.T) {
	dir := sealedImage(t)
	release, err := lockImage(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Seal(dir, fetch, io.Discard); !errors.Is(err, ErrImageInUse) {
		t.Errorf("Seal() = %v, want in use", err)
	}
	if err := Remove(dir); !errors.Is(err, ErrImageInUse) {
		t.Errorf("Remove() = %v, want in use", err)
	}
	r := newRunner(t, dir, func(*instance) error { t.Error("a VM booted from an open image"); return nil })
	if _, err := r.Run(context.Background(), job("a-1")); !errors.Is(err, ErrImageInUse) {
		t.Errorf("Run() = %v, want in use", err)
	}
	release()
	if err := Remove(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the image is still there")
	}
}

func copyFile(from, to string) error {
	raw, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, raw, 0o600)
}

func newRunner(t *testing.T, image string, launch func(*instance) error) *Runner {
	t.Helper()
	return &Runner{Dir: image, WorkDir: image + "-jobs", Log: quiet, launch: launch, clone: copyFile, latest: newer}
}

// newer stands in for GitHub with a runner later than the one pinned.
func newer(context.Context) (string, error) { return "999.0.0", nil }

func job(id string) jobs.Job {
	return jobs.Job{AssignmentID: id, Kind: "macos", Pool: "acme", JITConfig: "jit-" + id, VCPUs: 4, Label: "hive-macos"}
}

// guest plays the agent: it reads what it was given, says it is ready and
// reports code, then powers off.
func guest(t *testing.T, code string, seen *map[string]string) func(*instance) error {
	return func(vm *instance) error {
		got := map[string]string{}
		for _, name := range []string{"jitconfig", DigestFile, runnerShared, "label"} {
			raw, err := os.ReadFile(filepath.Join(vm.in, name))
			if err != nil {
				t.Errorf("nothing handed in as %s", name)
			}
			got[name] = string(raw)
		}
		for _, clone := range []string{vm.disk, vm.auxiliary} {
			if _, err := os.Stat(clone); err != nil {
				t.Errorf("no clone at %s", clone)
			}
		}
		*seen = got
		go func() {
			_ = os.WriteFile(filepath.Join(vm.out, "ready"), nil, 0o600)
			_ = os.WriteFile(filepath.Join(vm.out, "exit"), []byte(code+"\n"), 0o600)
			vm.end()
		}()
		return nil
	}
}

func jobDirs(t *testing.T, r *Runner) []string {
	t.Helper()
	found, _ := filepath.Glob(filepath.Join(r.WorkDir, "job-*"))
	return found
}

func TestAJobRunsInAClonedVMThatIsThenDeleted(t *testing.T) {
	dir := sealedImage(t)
	image, _ := Load(dir)
	var seen map[string]string
	r := newRunner(t, dir, guest(t, "0", &seen))
	result, err := r.Run(context.Background(), job("a-1"))
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("Run() = %+v, %v", result, err)
	}
	if seen["jitconfig"] != "jit-a-1" || seen[DigestFile] != image.Digest || seen[runnerShared] != "runner" || seen["label"] != "hive-macos" {
		t.Fatalf("the guest was given %v", seen)
	}
	if left := jobDirs(t, r); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
	// The image itself is as it was.
	if after, _ := Load(dir); after.Problem() != "" {
		t.Fatalf("the image after a job: %q", after.Problem())
	}
}

func TestAFailedJobIsDeletedToo(t *testing.T) {
	var seen map[string]string
	r := newRunner(t, sealedImage(t), guest(t, "1", &seen))
	result, err := r.Run(context.Background(), job("a-1"))
	if err != nil || result.ExitCode != 1 {
		t.Fatalf("Run() = %+v, %v", result, err)
	}
	if left := jobDirs(t, r); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

func TestAVMThatWillNotStartLeavesNothing(t *testing.T) {
	r := newRunner(t, sealedImage(t), func(*instance) error { return errors.New("no hypervisor") })
	if _, err := r.Run(context.Background(), job("a-1")); err == nil || !strings.Contains(err.Error(), "no hypervisor") {
		t.Fatalf("Run() = %v", err)
	}
	if left := jobDirs(t, r); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

// running is a guest that boots and never finishes its job, and counts the
// times its plug is pulled.
func running(stops *atomic.Int32, ready bool) func(*instance) error {
	return func(vm *instance) error {
		if ready {
			_ = os.WriteFile(filepath.Join(vm.out, "ready"), nil, 0o600)
		}
		vm.stop = func() {
			if stops.Add(1) == 1 {
				vm.end()
			}
		}
		return nil
	}
}

func TestACancelledJobStopsItsVM(t *testing.T) {
	var stops atomic.Int32
	r := newRunner(t, sealedImage(t), running(&stops, true))
	ctx, cancel := context.WithCancelCause(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel(jobs.ErrTimeout)
	}()
	result, err := r.Run(ctx, job("a-1"))
	if !errors.Is(err, context.Canceled) || result.ExitCode != -1 {
		t.Fatalf("Run() = %+v, %v", result, err)
	}
	if stops.Load() == 0 {
		t.Fatal("the VM was never stopped")
	}
	if left := jobDirs(t, r); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

// An image with no agent, or one that does not boot, gives the job up
// rather than holding it until its timeout.
func TestAGuestThatNeverSaysItIsReadyIsStopped(t *testing.T) {
	var stops atomic.Int32
	r := newRunner(t, sealedImage(t), running(&stops, false))
	_, err := r.Run(context.Background(), job("a-1"))
	if err == nil || !strings.Contains(err.Error(), "did not start its agent") {
		t.Fatalf("Run() = %v", err)
	}
	if stops.Load() == 0 {
		t.Fatal("the VM was never stopped")
	}
	if left := jobDirs(t, r); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

func TestAnUnsealedImageRunsNothing(t *testing.T) {
	r := newRunner(t, newImage(t), func(*instance) error { t.Error("a VM booted"); return nil })
	if _, err := r.Run(context.Background(), job("a-1")); err == nil || !strings.Contains(err.Error(), "not sealed") {
		t.Fatalf("Run() = %v", err)
	}
}

// Two VMs at most, whatever else asks: Apple's licence.
func TestAThirdVMIsRefused(t *testing.T) {
	booted := make(chan *instance, MostVMs)
	r := newRunner(t, sealedImage(t), func(vm *instance) error {
		_ = os.WriteFile(filepath.Join(vm.out, "ready"), nil, 0o600)
		vm.stop = func() {}
		booted <- vm
		return nil
	})
	done := make(chan error, MostVMs)
	for i := range MostVMs {
		go func() {
			_, err := r.Run(context.Background(), job("a-"+string(rune('1'+i))))
			done <- err
		}()
	}
	var vms []*instance
	for range MostVMs {
		select {
		case vm := <-booted:
			vms = append(vms, vm)
		case <-time.After(3 * time.Second):
			t.Fatal("a VM never booted")
		}
	}
	if vms[0].mac.String() == vms[1].mac.String() {
		t.Errorf("two VMs share the network identity %s", vms[0].mac)
	}
	if _, err := r.Run(context.Background(), job("a-3")); !errors.Is(err, ErrFull) {
		t.Fatalf("a third VM: %v, want ErrFull", err)
	}
	for _, vm := range vms {
		_ = os.WriteFile(filepath.Join(vm.out, "exit"), []byte("0"), 0o600)
		vm.end()
	}
	for range MostVMs {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	var seen map[string]string
	r.launch = guest(t, "0", &seen)
	if _, err := r.Run(context.Background(), job("a-3")); err != nil {
		t.Fatalf("once the others are gone: %v", err)
	}
}

// A daemon that was killed leaves its VMs' clones; the next one clears them
// before it runs anything.
func TestWhatAKilledDaemonLeftIsCleared(t *testing.T) {
	var seen map[string]string
	r := newRunner(t, sealedImage(t), guest(t, "0", &seen))
	left := filepath.Join(r.WorkDir, "job-1234")
	if err := os.MkdirAll(filepath.Join(left, "in"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(left, "in", "jitconfig"), []byte("a credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), job("a-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(left); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a previous run's VM is still there")
	}
}

func TestARunnerGitHubRefusesDeclinesFromThenOn(t *testing.T) {
	var seen map[string]string
	r := newRunner(t, sealedImage(t), guest(t, "7", &seen))
	if _, err := r.Run(context.Background(), job("a-1")); err != nil {
		t.Fatal(err)
	}
	if got := r.Unusable(); got != ReasonStaleRunner {
		t.Fatalf("Unusable() = %q, want %q", got, ReasonStaleRunner)
	}
}

func TestTheGuestsFilesAreWhatTheOwnerInstalls(t *testing.T) {
	dir := t.TempDir()
	if err := WriteGuestFiles(dir); err != nil {
		t.Fatal(err)
	}
	for name, executable := range map[string]bool{"hive-agent": true, "install.sh": true, "dev.hiverunner.agent.plist": false} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("no %s: %v", name, err)
		}
		if got := info.Mode()&0o100 != 0; got != executable {
			t.Errorf("%s executable = %v", name, got)
		}
	}
	agent, _ := os.ReadFile(filepath.Join(dir, "hive-agent"))
	for _, want := range []string{"mount_share " + InTag, "mount_share " + OutTag, DigestFile, "HIVE_IMAGE_DIGEST", runnerShared, "--jitconfig"} {
		if !strings.Contains(string(agent), want) {
			t.Errorf("the agent does not mention %q", want)
		}
	}
}

func TestSealingNoImageSaysHowToMakeOne(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "macos-image")
	if _, err := Seal(missing, fetch, io.Discard); !errors.Is(err, ErrNoImage) {
		t.Fatalf("Seal() = %v, want ErrNoImage", err)
	}
	if err := Remove(missing); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
}

// The guest can put anything where its results go. What it puts there is
// never followed to the host's files, never waited on, and never read past
// the size of a result.
func TestAHostileGuestReachesNothingOfTheHosts(t *testing.T) {
	for name, plant := range map[string]func(t *testing.T, exit, owners string){
		"a link to a host file": func(t *testing.T, exit, owners string) {
			if err := os.Symlink(owners, exit); err != nil {
				t.Fatal(err)
			}
		},
		"a link to a device that never ends": func(t *testing.T, exit, _ string) {
			if err := os.Symlink("/dev/zero", exit); err != nil {
				t.Fatal(err)
			}
		},
		"a FIFO nobody writes": func(t *testing.T, exit, _ string) {
			if err := unix.Mkfifo(exit, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"a directory": func(t *testing.T, exit, _ string) {
			if err := os.Mkdir(exit, 0o700); err != nil {
				t.Fatal(err)
			}
		},
		"a file larger than any result": func(t *testing.T, exit, _ string) {
			if err := os.WriteFile(exit, []byte(strings.Repeat("7", 1<<20)), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			owners := filepath.Join(t.TempDir(), "owners-file")
			if err := os.WriteFile(owners, []byte("7"), 0o600); err != nil {
				t.Fatal(err)
			}
			r := newRunner(t, sealedImage(t), func(vm *instance) error {
				_ = os.WriteFile(filepath.Join(vm.out, "ready"), nil, 0o600)
				plant(t, filepath.Join(vm.out, "exit"), owners)
				go func() {
					time.Sleep(100 * time.Millisecond)
					vm.end()
				}()
				return nil
			})
			done := make(chan struct{})
			var result jobs.Result
			var err error
			go func() {
				defer close(done)
				result, err = r.Run(context.Background(), job("a-1"))
			}()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("Run never returned")
			}
			// A directory is more than results ever are, and may be
			// caught as that first.
			if err == nil || !strings.Contains(err.Error(), "without reporting") && !strings.Contains(err.Error(), "more where its results go") || result.ExitCode != -1 {
				t.Fatalf("Run() = %+v, %v; want no result", result, err)
			}
			if r.Unusable() == ReasonStaleRunner {
				t.Error("a planted exit code switched macOS jobs off")
			}
			if raw, _ := os.ReadFile(owners); string(raw) != "7" {
				t.Errorf("the host's file now reads %q", raw)
			}
		})
	}
}

// A ready that is a link to something is not the agent saying it is ready.
func TestALinkIsNotReady(t *testing.T) {
	var stops atomic.Int32
	r := newRunner(t, sealedImage(t), func(vm *instance) error {
		_ = os.Symlink("/etc/hosts", filepath.Join(vm.out, "ready"))
		return running(&stops, false)(vm)
	})
	if _, err := r.Run(context.Background(), job("a-1")); err == nil || !strings.Contains(err.Error(), "did not start its agent") {
		t.Fatalf("Run() = %v", err)
	}
}

// What the host hands in is written as a new file, never through whatever
// had the name before.
func TestAStopReasonIsNeverWrittenThroughALink(t *testing.T) {
	owners := filepath.Join(t.TempDir(), "owners-file")
	if err := os.WriteFile(owners, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stops atomic.Int32
	var in string
	r := newRunner(t, sealedImage(t), func(vm *instance) error {
		in = vm.in
		if err := os.Symlink(owners, filepath.Join(vm.in, "stop-reason")); err != nil {
			t.Fatal(err)
		}
		vm.stop = func() {
			if stops.Add(1) == 1 {
				// Read before the clone is deleted.
				if info, err := os.Lstat(filepath.Join(in, "stop-reason")); err != nil || !info.Mode().IsRegular() {
					t.Errorf("stop-reason is not a file of its own: %v", err)
				}
				vm.end()
			}
		}
		_ = os.WriteFile(filepath.Join(vm.out, "ready"), nil, 0o600)
		return nil
	})
	ctx, cancel := context.WithCancelCause(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel(jobs.ErrTimeout)
	}()
	if _, err := r.Run(ctx, job("a-1")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() = %v", err)
	}
	if raw, _ := os.ReadFile(owners); string(raw) != "mine" {
		t.Errorf("the host's file now reads %q", raw)
	}
}

// The configuration is removed from what is handed in once the agent has
// read it.
func TestTheConfigurationIsRemovedOnceRead(t *testing.T) {
	var still atomic.Bool
	r := newRunner(t, sealedImage(t), func(vm *instance) error {
		_ = os.WriteFile(filepath.Join(vm.out, "ready"), nil, 0o600)
		go func() {
			time.Sleep(100 * time.Millisecond)
			_, err := os.Stat(filepath.Join(vm.in, "jitconfig"))
			still.Store(err == nil)
			_ = os.WriteFile(filepath.Join(vm.out, "exit"), []byte("0"), 0o600)
			vm.end()
		}()
		return nil
	})
	if _, err := r.Run(context.Background(), job("a-1")); err != nil {
		t.Fatal(err)
	}
	if still.Load() {
		t.Error("the configuration was still handed in after the agent said it was ready")
	}
}

// A runner replaced beside the image is not handed to a job.
func TestARunnerThatIsNotThePinnedOneIsRefused(t *testing.T) {
	dir := sealedImage(t)
	if err := os.WriteFile(filepath.Join(dir, native.RunnerFile), []byte("someone else's"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := newRunner(t, dir, func(*instance) error { t.Error("a VM booted"); return nil })
	if _, err := r.Run(context.Background(), job("a-1")); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("Run() = %v", err)
	}
	if left := jobDirs(t, r); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

// A job can write any exit code, so a refused runner declines jobs for a
// while, not for good.
func TestARefusedRunnerDeclinesOnlyForAWhile(t *testing.T) {
	defer func(was time.Duration) { staleFor = was }(staleFor)
	staleFor = 100 * time.Millisecond
	var seen map[string]string
	r := newRunner(t, sealedImage(t), guest(t, "7", &seen))
	if _, err := r.Run(context.Background(), job("a-1")); err != nil {
		t.Fatal(err)
	}
	if r.Unusable() != ReasonStaleRunner {
		t.Fatal("not declining after a refused runner")
	}
	time.Sleep(150 * time.Millisecond)
	if r.Unusable() == ReasonStaleRunner {
		t.Fatal("still declining once the while is over")
	}
}

// A VM that does not stop when told to keeps its clone and its place:
// another VM must not start beside it past the licence.
func TestAVMThatWillNotStopKeepsItsPlace(t *testing.T) {
	var stuck *instance
	r := newRunner(t, sealedImage(t), func(vm *instance) error {
		stuck = vm
		_ = os.WriteFile(filepath.Join(vm.out, "ready"), nil, 0o600)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if _, err := r.Run(ctx, job("a-1")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() = %v", err)
	}
	if left := jobDirs(t, r); len(left) != 1 {
		t.Fatalf("a running VM's clone was deleted: %v", left)
	}
	if held := taken(r); held != 1 {
		t.Fatalf("%d places taken with a VM still running", held)
	}
	stuck.end()
	deadline := time.Now().Add(3 * time.Second)
	for len(jobDirs(t, r)) != 0 || taken(r) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the clone and its place were kept after the VM stopped")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The daemon clears a dead daemon's clones as it starts, before any job,
// and makes nothing on a Mac that has never had a macOS image.
func TestClaimClearsLeftoversAtStart(t *testing.T) {
	r := newRunner(t, sealedImage(t), nil)
	left := filepath.Join(r.WorkDir, "job-1234")
	if err := os.MkdirAll(left, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := r.Claim(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(left); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a previous run's clone is still there")
	}
	none := filepath.Join(t.TempDir(), "macos-image")
	empty := &Runner{Dir: none, WorkDir: none + "-jobs", Log: quiet}
	if err := empty.Claim(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(empty.WorkDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a work directory was made on a Mac with no macOS image")
	}
}

// taken is how many VMs the runner counts as running.
func taken(r *Runner) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	held := 0
	for _, used := range r.slots {
		if used {
			held++
		}
	}
	return held
}

// A job can write 7 whenever it likes. It switches macOS jobs off only when
// GitHub really has a newer runner than the one pinned.
func TestARefusalIsBelievedOnlyWhenGitHubHasANewerRunner(t *testing.T) {
	for name, test := range map[string]struct {
		latest func(context.Context) (string, error)
		stale  bool
	}{
		"GitHub's latest is the one pinned": {func(context.Context) (string, error) { return native.RunnerVersion, nil }, false},
		"GitHub cannot be asked":            {func(context.Context) (string, error) { return "", errors.New("offline") }, false},
		"GitHub has a newer one":            {newer, true},
	} {
		t.Run(name, func(t *testing.T) {
			var seen map[string]string
			r := newRunner(t, sealedImage(t), guest(t, "7", &seen))
			r.latest = test.latest
			if _, err := r.Run(context.Background(), job("a-1")); err != nil {
				t.Fatal(err)
			}
			if got := r.Unusable() == ReasonStaleRunner; got != test.stale {
				t.Fatalf("declining = %v, want %v", got, test.stale)
			}
		})
	}
}

// VMs that would not stop count against the licence after their jobs have
// ended, so offers are declined rather than failing when a VM cannot start.
func TestVMsThatWillNotStopAreCountedForOffers(t *testing.T) {
	var stuck []*instance
	r := newRunner(t, sealedImage(t), func(vm *instance) error {
		stuck = append(stuck, vm)
		_ = os.WriteFile(filepath.Join(vm.out, "ready"), nil, 0o600)
		return nil
	})
	r.HostMemory = 64 << 30
	runStuck := func() {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(30 * time.Millisecond)
			cancel()
		}()
		if _, err := r.Run(ctx, job("a-1")); !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() = %v", err)
		}
	}
	var limits jobs.Limits
	// One at a time unless the owner says two: max-macos-vms.
	r.Limit(&limits)
	if limits.MostMacOS != 1 || limits.MacOSMemory != Memory(64<<30) {
		t.Fatalf("limits with nothing running, as the owner left it: %+v", limits)
	}
	r.Most = func() int { return 5 }
	r.Limit(&limits)
	if limits.MostMacOS != MostVMs {
		t.Fatalf("limits with nothing running, the owner asking for more than Apple allows: %+v", limits)
	}
	runStuck()
	r.Limit(&limits)
	if limits.MostMacOS != MostVMs-1 {
		t.Fatalf("with one VM that will not stop, %d may run", limits.MostMacOS)
	}
	runStuck()
	if got := r.Unusable(); got != ReasonStuck {
		t.Fatalf("with every place held: %q", got)
	}
	for _, vm := range stuck {
		vm.end()
	}
	deadline := time.Now().Add(3 * time.Second)
	for taken(r) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the places were kept after the VMs stopped")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := r.Unusable(); got == ReasonStuck {
		t.Fatal("still declining once the VMs stopped")
	}
}

// A guest that leaves more than a few small files where its results go is
// stopped.
func TestAGuestThatFillsItsResultsIsStopped(t *testing.T) {
	for name, fill := range map[string]func(out string){
		"many files": func(out string) {
			for i := range mostOutEntries + 1 {
				_ = os.WriteFile(filepath.Join(out, "f"+strconv.Itoa(i)), nil, 0o600)
			}
		},
		"a large file": func(out string) {
			_ = os.WriteFile(filepath.Join(out, "big"), make([]byte, mostOutBytes+1), 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			var stops atomic.Int32
			r := newRunner(t, sealedImage(t), func(vm *instance) error {
				_ = os.WriteFile(filepath.Join(vm.out, "ready"), nil, 0o600)
				fill(vm.out)
				return running(&stops, true)(vm)
			})
			if _, err := r.Run(context.Background(), job("a-1")); err == nil || !strings.Contains(err.Error(), "more where its results go") {
				t.Fatalf("Run() = %v", err)
			}
			if stops.Load() == 0 {
				t.Fatal("the VM was never stopped")
			}
		})
	}
}
