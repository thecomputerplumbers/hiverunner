//go:build darwin

package vm

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Code-Hex/vz/v3"
	"golang.org/x/sys/unix"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/jobs"
)

const (
	gib = 1 << 30
	// What a job's disk can grow to. The clone is sparse, so this costs
	// nothing until the job writes.
	diskBytes = 40 * gib
	// The virtio-fs tag the guest agent mounts.
	shareTag = "hive"
)

// Runner runs jobs in virtual machines. It implements jobs.Runner.
type Runner struct {
	Image Image
	// WorkDir holds each job's disk and share while it runs. It must be on
	// the same APFS volume as the image, or cloning falls back to a copy.
	WorkDir string
	Log     *slog.Logger
	// WarmVCPUs is the size of the VM kept booted and waiting between jobs,
	// so a job of that size skips the boot. Zero keeps none. Tend does the
	// keeping.
	WarmVCPUs int

	mu      sync.Mutex
	warm    *instance
	warming bool
	active  int
	// macs in use, by slot: see mac.
	macs [macSlots]bool
	// teardowns counts warm VMs being destroyed in the background.
	teardowns sync.WaitGroup
}

// A machine runs one job at a time, with perhaps a warm VM beside it and one
// being torn down. Four network identities cover that with room.
const macSlots = 4

var _ jobs.Runner = (*Runner)(nil)

// A warm VM is replaced before the guest agent gives up waiting for a job.
const warmMaxAge = 20 * time.Minute

// instance is one VM and everything on the host that belongs to it.
type instance struct {
	dir     string
	share   string
	console string
	vcpus   int
	booted  time.Time
	machine *vz.VirtualMachine
	// ended is closed when the VM stops; state says how.
	ended chan struct{}
	state vz.VirtualMachineState
	// release returns the VM's network identity for the next one to use.
	release func()
}

// Run hands the job to a VM, waits for it to power off, and deletes
// everything it used. The VM is the warm one if it fits, or a fresh boot.
func (r *Runner) Run(ctx context.Context, job jobs.Job) (int, error) {
	return r.run(ctx, job.AssignmentID, job.VCPUs, "jitconfig", job.JITConfig, nil)
}

// SelfTest runs a VM with no credential and returns what the guest reports
// about itself: proof that boot, the share, the network and the runner
// binary work on this machine.
func (r *Runner) SelfTest(ctx context.Context, vcpus int) (string, error) {
	var report string
	code, err := r.run(ctx, "selftest", vcpus, "selftest", "1", func(share string) {
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

func (r *Runner) run(ctx context.Context, id string, vcpus int, file, content string, collect func(share string)) (code int, err error) {
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

	if err := vm.handoff(file, content); err != nil {
		return -1, err
	}
	r.Log.Info("VM has the job", "job", id, "vcpus", vcpus, "warm", warm)

	select {
	case <-vm.ended:
		if vm.state != vz.VirtualMachineStateStopped {
			err = fmt.Errorf("the VM ended in state %v", vm.state)
		}
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
	}
	fail := func(err error) (*instance, error) {
		os.RemoveAll(dir)
		return nil, err
	}

	disk := filepath.Join(dir, "disk.img")
	if err := cloneFile(r.Image.path(r.Image.Disk), disk); err != nil {
		return fail(fmt.Errorf("clone the guest disk: %w", err))
	}
	if err := os.Truncate(disk, diskBytes); err != nil {
		return fail(err)
	}
	// The whole of what the guest can see of the host.
	if err := os.Mkdir(vm.share, 0o700); err != nil {
		return fail(err)
	}
	address, release, err := r.mac()
	if err != nil {
		return fail(err)
	}
	vm.release = release
	fail = func(err error) (*instance, error) {
		release()
		os.RemoveAll(dir)
		return nil, err
	}
	if vm.machine, err = r.configure(vcpus, disk, vm.share, vm.console, address); err != nil {
		return fail(err)
	}
	// Subscribe before starting, so a VM that dies at once is still seen.
	changes := vm.machine.StateChangedNotify()
	go func() {
		for state := range changes {
			if state == vz.VirtualMachineStateStopped || state == vz.VirtualMachineStateError {
				vm.state = state
				close(vm.ended)
				return
			}
		}
	}()
	if err := vm.machine.Start(); err != nil {
		return fail(fmt.Errorf("start the VM: %w", err))
	}
	return vm, nil
}

// handoff gives the VM its job. The clock goes first, because the guest has
// none it can trust and a warm VM may have been waiting a while. The job file
// appears whole or not at all, so the guest never reads half of one.
func (vm *instance) handoff(file, content string) error {
	now := strconv.FormatInt(time.Now().Unix(), 10)
	if err := os.WriteFile(filepath.Join(vm.share, "clock"), []byte(now), 0o600); err != nil {
		return err
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
		if vm.machine.CanStop() {
			_ = vm.machine.Stop()
		}
		select {
		case <-vm.ended:
		case <-time.After(15 * time.Second):
		}
	}
	os.RemoveAll(vm.dir)
	vm.release()
}

// mac lends a VM its network identity.
//
// macOS hands each new network card an address and remembers it for a day,
// out of a pool of about 250. A fresh random card per job would exhaust that
// pool on a busy machine and leave new VMs with no network. So a runner
// reuses a small fixed set, derived from its work directory so that two
// runners on one Mac do not share them.
func (r *Runner) mac() (*vz.MACAddress, func(), error) {
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
		address, err := vz.NewRandomLocallyAdministeredMACAddress()
		return address, func() {}, err
	}
	seed := sha256.Sum256([]byte(r.WorkDir))
	// 0x02: locally administered, unicast.
	hardware := net.HardwareAddr{0x02, 0x68, seed[0], seed[1], seed[2], seed[3]&0xfc | byte(slot)}
	address, err := vz.NewMACAddress(hardware)
	release := func() {
		r.mu.Lock()
		r.macs[slot] = false
		r.mu.Unlock()
	}
	if err != nil {
		release()
		return nil, nil, err
	}
	return address, release, nil
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
// idle, and none when it is not: on battery or with the lid closed, a VM
// waiting for a job is the owner's battery spent on nothing.
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
	start := available && r.warm == nil && !r.warming && r.active == 0
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

func (r *Runner) configure(vcpus int, disk, share, console string, mac *vz.MACAddress) (*vz.VirtualMachine, error) {
	if vcpus < 1 {
		vcpus = 1
	}
	if host := runtime.NumCPU(); vcpus > host {
		vcpus = host
	}
	// 2 GiB a core, at least 4: what a hosted runner of the same size has
	// or a little less, and never more than half the Mac.
	memory := uint64(max(vcpus*2, 4)) * gib
	if total, err := unix.SysctlUint64("hw.memsize"); err == nil && memory > total/2 {
		memory = total / 2 / gib * gib
	}

	boot, err := vz.NewLinuxBootLoader(
		r.Image.path(r.Image.Kernel),
		vz.WithCommandLine(r.Image.CommandLine),
		vz.WithInitrd(r.Image.path(r.Image.Initrd)),
	)
	if err != nil {
		return nil, fmt.Errorf("boot loader: %w", err)
	}
	config, err := vz.NewVirtualMachineConfiguration(boot, uint(vcpus), memory)
	if err != nil {
		return nil, fmt.Errorf("VM configuration: %w", err)
	}

	attachment, err := vz.NewDiskImageStorageDeviceAttachment(disk, false)
	if err != nil {
		return nil, fmt.Errorf("disk: %w", err)
	}
	block, err := vz.NewVirtioBlockDeviceConfiguration(attachment)
	if err != nil {
		return nil, err
	}
	config.SetStorageDevicesVirtualMachineConfiguration([]vz.StorageDeviceConfiguration{block})

	// NAT: the guest can reach out, and nothing can reach in.
	nat, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, fmt.Errorf("network: %w", err)
	}
	network, err := vz.NewVirtioNetworkDeviceConfiguration(nat)
	if err != nil {
		return nil, err
	}
	network.SetMACAddress(mac)
	config.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{network})

	directory, err := vz.NewSharedDirectory(share, false)
	if err != nil {
		return nil, fmt.Errorf("share: %w", err)
	}
	single, err := vz.NewSingleDirectoryShare(directory)
	if err != nil {
		return nil, err
	}
	filesystem, err := vz.NewVirtioFileSystemDeviceConfiguration(shareTag)
	if err != nil {
		return nil, err
	}
	filesystem.SetDirectoryShare(single)
	config.SetDirectorySharingDevicesVirtualMachineConfiguration([]vz.DirectorySharingDeviceConfiguration{filesystem})

	// The guest's console goes to a file, for diagnosis. Nothing is wired to
	// its input: there is no way to type into the guest.
	serial, err := vz.NewFileSerialPortAttachment(console, false)
	if err != nil {
		return nil, fmt.Errorf("console: %w", err)
	}
	port, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serial)
	if err != nil {
		return nil, err
	}
	config.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{port})

	entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, err
	}
	config.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})

	balloon, err := vz.NewVirtioTraditionalMemoryBalloonDeviceConfiguration()
	if err != nil {
		return nil, err
	}
	config.SetMemoryBalloonDevicesVirtualMachineConfiguration([]vz.MemoryBalloonDeviceConfiguration{balloon})

	if ok, err := config.Validate(); !ok || err != nil {
		return nil, fmt.Errorf("VM configuration is not valid: %w", err)
	}
	return vz.NewVirtualMachine(config)
}

// cloneFile makes an APFS copy-on-write clone: instant, and it shares every
// block with the original until the job writes. Off APFS it copies.
func cloneFile(from, to string) error {
	if err := unix.Clonefile(from, to, 0); err == nil {
		return nil
	}
	source, err := os.Open(from)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer target.Close()
	_, err = target.ReadFrom(source)
	return err
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
