//go:build darwin || linux

package vm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/jobs"
)

const (
	gib = 1 << 30
	// What a job's disk can grow to. The disk is a clone or an overlay, so
	// this costs nothing until the job writes.
	diskBytes = 40 * gib
	// The virtio-fs tag the guest agent mounts.
	shareTag = "hive"
)

// Runner runs jobs in virtual machines. It implements jobs.Runner.
//
// Everything here is the same on every host: which VM a job gets, how the
// job is handed over, what is read back and what is deleted. Starting a VM
// is the host's own business and lives in launch, one per operating system.
type Runner struct {
	Image Image
	// WorkDir holds each job's disk and share while it runs. It must be on
	// the same volume as the image, so a job's disk is a clone or an overlay
	// of the image and not a copy.
	WorkDir string
	Log     *slog.Logger
	// WarmVCPUs is the size of the VM kept booted and waiting between jobs,
	// so a job of that size skips the boot. Zero keeps none. Tend does the
	// keeping.
	WarmVCPUs int
	// Guest, if set, is a service of the host's that each VM can reach over
	// a private socket: the Actions cache.
	Guest *GuestService

	mu      sync.Mutex
	warm    *instance
	warming bool
	active  int
	// macs in use, by slot: see mac.
	macs [macSlots]bool
	// teardowns counts warm VMs being destroyed in the background.
	teardowns sync.WaitGroup
	// stale is set once GitHub has refused this image's runner as too old.
	// Nothing clears it: a new image means a new daemon start.
	stale atomic.Bool
	// What the host's hypervisor keeps between VMs, if anything.
	platform platformState
}

// Unusable reports why this runner cannot run jobs, or "" if it can. It
// implements jobs.Unusable.
func (r *Runner) Unusable() string {
	if r.stale.Load() {
		return ReasonStaleImage
	}
	return ""
}

// A machine runs a handful of jobs at once at most, with perhaps a warm VM
// beside them and one being torn down. Sixteen network identities cover that
// with room.
const macSlots = 16

var _ jobs.Runner = (*Runner)(nil)

// A warm VM is replaced before the guest agent gives up waiting for a job.
const warmMaxAge = 20 * time.Minute

// instance is one VM and everything on the host that belongs to it.
type instance struct {
	// pool is whose job the VM is running: "" until it has one. The guest
	// service reads it; the guest cannot set it.
	pool atomic.Pointer[string]
	// slot is the VM's network identity, or -1 for one made up on the spot.
	slot    int
	dir     string
	share   string
	console string
	vcpus   int
	booted  time.Time
	// ended is closed when the VM stops; failure says whether it stopped
	// any way but powering itself off, and is written before ended closes.
	ended   chan struct{}
	failure error
	// stop pulls the plug. It is safe to call more than once, and on a VM
	// that has already stopped.
	stop func()
	// served is whether the guest can reach the host's service, and
	// unserve hangs up on it, along with any download still in flight.
	served  bool
	unserve func()
	// release returns the VM's network identity for the next one to use.
	release func()
}

// owner reports whose job the VM is running, for the guest service.
func (vm *instance) owner() string {
	if pool := vm.pool.Load(); pool != nil {
		return *pool
	}
	return ""
}

// Run hands the job to a VM, waits for it to power off, and deletes
// everything it used. The VM is the warm one if it fits, or a fresh boot.
func (r *Runner) Run(ctx context.Context, job jobs.Job) (int, error) {
	return r.run(ctx, job.AssignmentID, job.Pool, job.VCPUs, "jitconfig", job.JITConfig, nil)
}

// SelfTest runs a VM with no credential and returns what the guest reports
// about itself: proof that boot, the share, the network and the runner
// binary work on this machine.
func (r *Runner) SelfTest(ctx context.Context, vcpus int) (string, error) {
	var report string
	code, err := r.run(ctx, "selftest", "selftest", vcpus, "selftest", "1", func(share string) {
		raw, _ := os.ReadFile(filepath.Join(share, "selftest.out"))
		report = string(raw)
	})
	if err != nil {
		return report, err
	}
	if code != 0 {
		return report, fmt.Errorf("guest self-test exited %d", code)
	}
	return report, nil
}

func (r *Runner) run(ctx context.Context, id, pool string, vcpus int, file, content string, collect func(share string)) (code int, err error) {
	started := time.Now()
	r.mu.Lock()
	r.active++
	vm := r.takeWarmLocked(vcpus)
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.active--
		r.mu.Unlock()
	}()

	warm := vm != nil
	if vm == nil {
		if vm, err = r.boot(vcpus); err != nil {
			return -1, err
		}
	}
	// Whatever happens: the disk, the share and the credential go.
	defer vm.destroy()

	// The VM now works for this pool, and is told where the host's service
	// is before it is told about the job.
	extras := map[string]string{}
	if vm.served && r.Guest.Enabled() {
		vm.pool.Store(&pool)
		extras[GuestServiceFile] = strconv.FormatUint(uint64(r.Guest.Port), 10)
	}
	if err := vm.handoff(extras, file, content); err != nil {
		return -1, err
	}
	r.Log.Info("VM has the job", "job", id, "vcpus", vcpus, "warm", warm)

	select {
	case <-vm.ended:
		err = vm.failure
	case <-ctx.Done():
		// Cancelled or out of time. There is nothing in the VM worth a
		// graceful shutdown; destroy pulls the plug.
		err = ctx.Err()
	}

	if collect != nil {
		collect(vm.share)
	}
	code = -1
	if raw, readErr := os.ReadFile(filepath.Join(vm.share, "exit")); readErr == nil {
		if parsed, parseErr := strconv.Atoi(strings.TrimSpace(string(raw))); parseErr == nil {
			code = parsed
		}
	} else if err == nil {
		err = errors.New("the VM stopped without reporting a result")
	}
	if code == ExitRunnerDeprecated && file == "jitconfig" {
		// Not a job that failed: a runner that will never take one. Every
		// later VM from this image would do the same, so stop offering.
		r.stale.Store(true)
		r.Log.Error("GitHub refused the guest's runner as too old; this machine will decline jobs until the image is rebuilt", "runner", r.Image.RunnerVersion)
	}
	if errors.Is(err, context.Canceled) {
		// Stopped on purpose: released by the pool, or the daemon is
		// shutting down. The caller knows which; it is not a VM failing.
		r.Log.Info("VM stopped", "job", id, "exit", code, "seconds", int(time.Since(started).Seconds()))
		return code, err
	}
	if err != nil {
		// The guest's console is not logged. A job can write anything to it,
		// secrets included, and this log outlives the VM. Keeping it is an
		// explicit choice made while debugging a boot.
		r.Log.Error("VM failed", "job", id, "error", err, "console", vm.keepConsole())
	}
	r.Log.Info("VM ended", "job", id, "exit", code, "seconds", int(time.Since(started).Seconds()))
	return code, err
}

// boot starts a fresh VM on a clone of the guest disk, with an empty share.
// The guest agent then waits for a job to appear in the share.
func (r *Runner) boot(vcpus int) (*instance, error) {
	if err := os.MkdirAll(r.WorkDir, 0o700); err != nil {
		return nil, err
	}
	// A directory per VM, named so two can never share one.
	dir, err := os.MkdirTemp(r.WorkDir, "job-")
	if err != nil {
		return nil, err
	}
	vm := &instance{
		dir:     dir,
		share:   filepath.Join(dir, "share"),
		console: filepath.Join(dir, "console.log"),
		vcpus:   vcpus,
		booted:  time.Now(),
		ended:   make(chan struct{}),
		stop:    func() {},
	}
	// The whole of what the guest can see of the host.
	if err := os.Mkdir(vm.share, 0o700); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	address, slot, release, err := r.mac()
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	vm.slot = slot
	vm.release = release
	// launch leaves nothing running when it fails.
	if err := r.launch(vm, address); err != nil {
		release()
		os.RemoveAll(dir)
		return nil, err
	}
	return vm, nil
}

// handoff gives the VM its job. The clock goes first, because the guest has
// none it can trust and a warm VM may have been waiting a while. The job file
// appears whole or not at all, so the guest never reads half of one.
func (vm *instance) handoff(extras map[string]string, file, content string) error {
	now := strconv.FormatInt(time.Now().Unix(), 10)
	if err := os.WriteFile(filepath.Join(vm.share, "clock"), []byte(now), 0o600); err != nil {
		return err
	}
	for name, value := range extras {
		if err := os.WriteFile(filepath.Join(vm.share, name), []byte(value), 0o600); err != nil {
			return err
		}
	}
	// Outside the share while it is being written: the guest sees only the
	// finished file.
	temporary := filepath.Join(vm.dir, "handoff.tmp")
	if err := os.WriteFile(temporary, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, filepath.Join(vm.share, file))
}

func (vm *instance) alive() bool {
	select {
	case <-vm.ended:
		return false
	default:
		return true
	}
}

// destroy stops the VM if it is still running and deletes its files.
func (vm *instance) destroy() {
	if vm.alive() {
		vm.stop()
		select {
		case <-vm.ended:
		case <-time.After(15 * time.Second):
		}
	}
	if vm.unserve != nil {
		vm.unserve()
	}
	os.RemoveAll(vm.dir)
	vm.release()
}

// mac lends a VM its network identity.
//
// A host's network hands each new card an address and may remember it for a
// day, out of a small pool: macOS keeps about 250. A fresh random card per
// job would exhaust that on a busy machine and leave new VMs with no
// network. So a runner reuses a small fixed set, derived from its work
// directory so that two runners on one machine do not share them.
func (r *Runner) mac() (net.HardwareAddr, int, func(), error) {
	r.mu.Lock()
	slot := -1
	for i, used := range r.macs {
		if !used {
			slot = i
			r.macs[i] = true
			break
		}
	}
	r.mu.Unlock()
	if slot < 0 {
		// More VMs at once than this runner ever means to have. Do not
		// refuse the job over it.
		hardware := make(net.HardwareAddr, 6)
		if _, err := rand.Read(hardware); err != nil {
			return nil, -1, nil, err
		}
		// Locally administered, unicast.
		hardware[0] = hardware[0]&0xfc | 0x02
		return hardware, -1, func() {}, nil
	}
	seed := sha256.Sum256([]byte(r.WorkDir))
	// 0x02: locally administered, unicast.
	hardware := net.HardwareAddr{0x02, 0x68, seed[0], seed[1], seed[2], seed[3]&0xf0 | byte(slot)}
	release := func() {
		r.mu.Lock()
		r.macs[slot] = false
		r.mu.Unlock()
	}
	return hardware, slot, release, nil
}

// takeWarmLocked hands over the warm VM if it suits the job.
func (r *Runner) takeWarmLocked(vcpus int) *instance {
	vm := r.warm
	if vm == nil || vm.vcpus != vcpus || !vm.alive() {
		return nil
	}
	r.warm = nil
	return vm
}

// Tend keeps one VM booted and waiting while the machine is available and
// idle, and none when it is not, such as when the lid is closed.
//
// It returns when ctx is cancelled, and not before every warm VM it started
// has been destroyed and its files removed. The daemon waits for it on the
// way out for that reason.
func (r *Runner) Tend(ctx context.Context, available func() bool) {
	if r.WarmVCPUs < 1 {
		return
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		r.tend(available())
		select {
		case <-ctx.Done():
			r.tend(false)
			r.teardowns.Wait()
			return
		case <-tick.C:
		}
	}
}

func (r *Runner) tend(available bool) {
	r.mu.Lock()
	if vm := r.warm; vm != nil && (!available || !vm.alive() || time.Since(vm.booted) > warmMaxAge) {
		r.warm = nil
		// Destroying waits for the VM to stop; jobs are not held up for it,
		// but shutdown is.
		r.teardowns.Add(1)
		go func() {
			defer r.teardowns.Done()
			vm.destroy()
		}()
	}
	// Not during a job: booting would take cores from work that is paid for.
	// And not from an image that cannot run one.
	start := available && r.warm == nil && !r.warming && r.active == 0 && !r.stale.Load()
	if start {
		r.warming = true
	}
	r.mu.Unlock()
	if !start {
		return
	}

	vm, err := r.boot(r.WarmVCPUs)
	r.mu.Lock()
	r.warming = false
	if err != nil {
		r.mu.Unlock()
		r.Log.Warn("could not boot a warm VM", "error", err)
		return
	}
	r.warm = vm
	r.mu.Unlock()
	r.Log.Info("a VM is booted and waiting", "vcpus", r.WarmVCPUs)
}

// keepConsole says what became of the guest's console output. It is kept
// only when HIVE_KEEP_CONSOLE names a directory, and then as a file readable
// by its owner alone; otherwise it is deleted with the VM, unread.
func (vm *instance) keepConsole() string {
	keep := os.Getenv("HIVE_KEEP_CONSOLE")
	if keep == "" {
		return "discarded (set HIVE_KEEP_CONSOLE to a directory to keep it)"
	}
	raw, err := os.ReadFile(vm.console)
	if err != nil {
		return "unavailable"
	}
	if err := os.MkdirAll(keep, 0o700); err != nil {
		return "unavailable"
	}
	saved := filepath.Join(keep, filepath.Base(vm.dir)+".console.log")
	if err := os.WriteFile(saved, raw, 0o600); err != nil {
		return "unavailable"
	}
	return saved
}
