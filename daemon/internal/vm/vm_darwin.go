//go:build darwin

package vm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
}

var _ jobs.Runner = (*Runner)(nil)

// Run boots a fresh VM, hands it the job's runner configuration, waits for
// it to power off, and deletes everything it used.
func (r *Runner) Run(ctx context.Context, job jobs.Job) (int, error) {
	return r.run(ctx, job.AssignmentID, job.VCPUs, "jitconfig", job.JITConfig)
}

// SelfTest boots a VM with no credential and returns what the guest reports
// about itself: proof that boot, the share, the network and the runner
// binary work on this machine.
func (r *Runner) SelfTest(ctx context.Context, vcpus int) (string, error) {
	var report string
	code, err := r.runWith(ctx, "selftest", vcpus, "selftest", "1", func(share string) {
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

func (r *Runner) run(ctx context.Context, id string, vcpus int, file, content string) (int, error) {
	return r.runWith(ctx, id, vcpus, file, content, nil)
}

func (r *Runner) runWith(ctx context.Context, id string, vcpus int, file, content string, collect func(share string)) (code int, err error) {
	started := time.Now()
	// A directory per job, named so two jobs can never share one, and
	// removed whatever happens: the disk, the share and the credential.
	if err := os.MkdirAll(r.WorkDir, 0o700); err != nil {
		return -1, err
	}
	dir, err := os.MkdirTemp(r.WorkDir, "job-")
	if err != nil {
		return -1, err
	}
	defer os.RemoveAll(dir)

	disk := filepath.Join(dir, "disk.img")
	if err := cloneFile(r.Image.path(r.Image.Disk), disk); err != nil {
		return -1, fmt.Errorf("clone the guest disk: %w", err)
	}
	if err := os.Truncate(disk, diskBytes); err != nil {
		return -1, err
	}

	// The whole of what the guest can see of the host.
	share := filepath.Join(dir, "share")
	if err := os.Mkdir(share, 0o700); err != nil {
		return -1, err
	}
	// The guest sets its clock from this; it has none it can trust.
	now := strconv.FormatInt(time.Now().Unix(), 10)
	if err := os.WriteFile(filepath.Join(share, "clock"), []byte(now), 0o600); err != nil {
		return -1, err
	}
	if err := os.WriteFile(filepath.Join(share, file), []byte(content), 0o600); err != nil {
		return -1, err
	}

	console := filepath.Join(dir, "console.log")
	machine, err := r.configure(vcpus, disk, share, console)
	if err != nil {
		return -1, err
	}
	if err := machine.Start(); err != nil {
		return -1, fmt.Errorf("start the VM: %w", err)
	}
	r.Log.Info("VM started", "job", id, "vcpus", vcpus)

	stopped := waitStopped(machine)
	select {
	case state := <-stopped:
		if state != vz.VirtualMachineStateStopped {
			err = fmt.Errorf("the VM ended in state %v", state)
		}
	case <-ctx.Done():
		// Cancelled or out of time. There is nothing in the VM worth a
		// graceful shutdown: pull the plug.
		if machine.CanStop() {
			_ = machine.Stop()
		}
		select {
		case <-stopped:
		case <-time.After(15 * time.Second):
		}
		err = ctx.Err()
	}

	if collect != nil {
		collect(share)
	}
	code = -1
	if raw, readErr := os.ReadFile(filepath.Join(share, "exit")); readErr == nil {
		if parsed, parseErr := strconv.Atoi(strings.TrimSpace(string(raw))); parseErr == nil {
			code = parsed
		}
	} else if err == nil {
		err = errors.New("the VM stopped without reporting a result")
	}
	if err != nil {
		// The console is the only witness to a guest that never reported.
		r.Log.Error("VM failed", "job", id, "error", err, "console", tail(console, 30))
	}
	r.Log.Info("VM ended", "job", id, "exit", code, "seconds", int(time.Since(started).Seconds()))
	return code, err
}

func (r *Runner) configure(vcpus int, disk, share, console string) (*vz.VirtualMachine, error) {
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
	mac, err := vz.NewRandomLocallyAdministeredMACAddress()
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

// waitStopped reports the state the VM ends in.
func waitStopped(machine *vz.VirtualMachine) <-chan vz.VirtualMachineState {
	done := make(chan vz.VirtualMachineState, 1)
	go func() {
		for state := range machine.StateChangedNotify() {
			if state == vz.VirtualMachineStateStopped || state == vz.VirtualMachineStateError {
				done <- state
				return
			}
		}
	}()
	return done
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

func tail(path string, lines int) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	all := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}
