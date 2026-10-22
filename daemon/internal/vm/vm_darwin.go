//go:build darwin

package vm

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Code-Hex/vz/v3"
	"golang.org/x/sys/unix"
)

// FencesNetwork is whether a guest here is held to its fence.Policy. Not
// on a Mac: the guest is behind the system's NAT, which gives hived no say
// in where its packets go, and pf, which would, needs root.
const FencesNetwork = false

// Apple's Virtualization.framework keeps nothing between VMs.
type platformState struct{}

// Preflight reports why this host cannot run VMs, or nil if it can. A Mac
// that runs the signed daemon can.
func Preflight() error { return nil }

// launch starts the VM under Apple's Virtualization.framework, on an APFS
// clone of the guest disk.
func (r *Runner) launch(vm *instance, mac net.HardwareAddr) error {
	disk := filepath.Join(vm.dir, "disk.img")
	if err := cloneFile(r.Image.path(r.Image.Disk), disk); err != nil {
		return fmt.Errorf("clone the guest disk: %w", err)
	}
	if err := os.Truncate(disk, diskBytes); err != nil {
		return err
	}
	machine, err := r.configure(vm.vcpus, disk, vm.share, vm.console, mac)
	if err != nil {
		return err
	}
	total, _ := unix.SysctlUint64("hw.memsize")
	vm.memory = GuestMemoryBytes(vm.vcpus, total)
	// Listening before the guest boots, so it never finds the socket shut.
	// A VM without the service still runs jobs; they use GitHub's cache.
	if r.Guest != nil {
		if err := serve(vm, machine, r.Guest); err != nil {
			r.Log.Warn("this VM cannot reach the cache", "error", err)
		}
	}
	// Subscribe before starting, so a VM that dies at once is still seen.
	changes := machine.StateChangedNotify()
	go func() {
		for state := range changes {
			if state == vz.VirtualMachineStateStopped || state == vz.VirtualMachineStateError {
				if state != vz.VirtualMachineStateStopped {
					vm.failure = fmt.Errorf("the VM ended in state %v", state)
					if !vm.stopped.Load() {
						hostKilledVM(vm)
					}
				}
				close(vm.ended)
				return
			}
		}
	}()
	vm.stop = func() {
		if machine.CanStop() {
			_ = machine.Stop()
		}
	}
	if err := machine.Start(); err != nil {
		if vm.unserve != nil {
			vm.unserve()
		}
		return fmt.Errorf("start the VM: %w", err)
	}
	return nil
}

// serve answers the guest on a socket only this VM has. Which hive the VM
// works for is read from the instance each time, so a warm VM is served
// nothing until a job is handed to it.
func serve(vm *instance, machine *vz.VirtualMachine, guest *GuestService) error {
	devices := machine.SocketDevices()
	if len(devices) == 0 {
		return errors.New("the VM has no socket device")
	}
	listener, err := devices[0].Listen(guest.Port)
	if err != nil {
		return err
	}
	service := &http.Server{
		Handler:           guest.Handler(vm.owner),
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() { _ = service.Serve(listener) }()
	vm.served = true
	// Also closes the socket, and any download still in flight.
	vm.unserve = func() { _ = service.Close() }
	return nil
}

func (r *Runner) configure(vcpus int, disk, share, console string, mac net.HardwareAddr) (*vz.VirtualMachine, error) {
	if vcpus < 1 {
		vcpus = 1
	}
	if host := runtime.NumCPU(); vcpus > host {
		vcpus = host
	}
	total, _ := unix.SysctlUint64("hw.memsize")
	memory := GuestMemoryBytes(vcpus, total)

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
	address, err := vz.NewMACAddress(mac)
	if err != nil {
		return nil, err
	}
	network.SetMACAddress(address)
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

	// A socket between this VM and the host, on no network at all: the guest
	// reaches the host's cache through it, and nothing else can.
	socket, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		return nil, err
	}
	config.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{socket})

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
