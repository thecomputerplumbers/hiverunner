//go:build darwin && arm64

package macvm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Code-Hex/vz/v3"
	"golang.org/x/sys/unix"
)

// A Mac that runs the signed daemon can run macOS VMs. Which macOS the
// guest is, and whether it can mount the job's share, is the image's.
func platformProblem() string { return "" }

// machine is what a macOS VM is made of, for a job or for the owner.
type machine struct {
	vcpus      uint
	memory     uint64
	disk       string
	model      *vz.MacHardwareModel
	auxiliary  *vz.MacAuxiliaryStorage
	identifier *vz.MacMachineIdentifier
	// mac is the VM's network identity; nil is a new one.
	mac net.HardwareAddr
	// shares, if any, are what the guest sees of the host.
	shares []vz.DirectorySharingDeviceConfiguration
}

// start boots a job's VM on its clones, with a machine identifier of its
// own. It sees two directories of the host's and nothing else: one it can
// only read, and one for its results.
func start(guest *instance) error {
	model, err := vz.NewMacHardwareModelWithDataPath(guest.hardwareModel)
	if err != nil {
		return fmt.Errorf("hardware model: %w", err)
	}
	auxiliary, err := vz.NewMacAuxiliaryStorage(guest.auxiliary)
	if err != nil {
		return fmt.Errorf("auxiliary storage: %w", err)
	}
	// A new identity for every job: to the guest, and to anything it signs
	// in to, each VM is a Mac it has never seen.
	identifier, err := vz.NewMacMachineIdentifier()
	if err != nil {
		return err
	}
	in, err := shareOne(InTag, guest.in, true)
	if err != nil {
		return err
	}
	out, err := shareOne(OutTag, guest.out, false)
	if err != nil {
		return err
	}
	total, _ := unix.SysctlUint64("hw.memsize")
	vm, err := configure(machine{
		vcpus:      uint(min(guest.vcpus, runtime.NumCPU())),
		memory:     Memory(total),
		disk:       guest.disk,
		model:      model,
		auxiliary:  auxiliary,
		identifier: identifier,
		mac:        guest.mac,
		shares:     []vz.DirectorySharingDeviceConfiguration{in, out},
	})
	if err != nil {
		return err
	}
	// Subscribe before starting, so a VM that dies at once is still seen.
	changes := vm.StateChangedNotify()
	abandoned := make(chan struct{})
	go func() {
		for {
			select {
			case state := <-changes:
				if state == vz.VirtualMachineStateStopped || state == vz.VirtualMachineStateError {
					if state != vz.VirtualMachineStateStopped {
						guest.failure = fmt.Errorf("the macOS VM ended in state %v", state)
					}
					guest.end()
					return
				}
			case <-abandoned:
				return
			}
		}
	}()
	guest.stop = func() {
		if vm.CanStop() {
			_ = vm.Stop()
		}
	}
	if err := vm.Start(); err != nil {
		close(abandoned)
		return err
	}
	return nil
}

// shareOne is a directory the guest mounts by its tag.
func shareOne(tag, path string, readOnly bool) (*vz.VirtioFileSystemDeviceConfiguration, error) {
	directory, err := vz.NewSharedDirectory(path, readOnly)
	if err != nil {
		return nil, fmt.Errorf("share: %w", err)
	}
	single, err := vz.NewSingleDirectoryShare(directory)
	if err != nil {
		return nil, err
	}
	share, err := vz.NewVirtioFileSystemDeviceConfiguration(tag)
	if err != nil {
		return nil, err
	}
	share.SetDirectoryShare(single)
	return share, nil
}

func configure(m machine) (*vz.VirtualMachine, error) {
	boot, err := vz.NewMacOSBootLoader()
	if err != nil {
		return nil, fmt.Errorf("boot loader: %w", err)
	}
	config, err := vz.NewVirtualMachineConfiguration(boot, m.vcpus, m.memory)
	if err != nil {
		return nil, fmt.Errorf("VM configuration: %w", err)
	}
	platform, err := vz.NewMacPlatformConfiguration(
		vz.WithMacHardwareModel(m.model),
		vz.WithMacMachineIdentifier(m.identifier),
		vz.WithMacAuxiliaryStorage(m.auxiliary),
	)
	if err != nil {
		return nil, fmt.Errorf("platform: %w", err)
	}
	config.SetPlatformVirtualMachineConfiguration(platform)

	attachment, err := vz.NewDiskImageStorageDeviceAttachment(m.disk, false)
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
	var address *vz.MACAddress
	if m.mac != nil {
		address, err = vz.NewMACAddress(m.mac)
	} else {
		address, err = vz.NewRandomLocallyAdministeredMACAddress()
	}
	if err != nil {
		return nil, err
	}
	network.SetMACAddress(address)
	config.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{network})

	// macOS wants a display even with nobody looking, and the owner's
	// sessions need one, with a keyboard and a pointer.
	graphics, err := vz.NewMacGraphicsDeviceConfiguration()
	if err != nil {
		return nil, err
	}
	display, err := vz.NewMacGraphicsDisplayConfiguration(1920, 1200, 80)
	if err != nil {
		return nil, err
	}
	graphics.SetDisplays(display)
	config.SetGraphicsDevicesVirtualMachineConfiguration([]vz.GraphicsDeviceConfiguration{graphics})
	keyboard, err := vz.NewUSBKeyboardConfiguration()
	if err != nil {
		return nil, err
	}
	config.SetKeyboardsVirtualMachineConfiguration([]vz.KeyboardConfiguration{keyboard})
	pointer, err := vz.NewUSBScreenCoordinatePointingDeviceConfiguration()
	if err != nil {
		return nil, err
	}
	config.SetPointingDevicesVirtualMachineConfiguration([]vz.PointingDeviceConfiguration{pointer})

	entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, err
	}
	config.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})

	if len(m.shares) > 0 {
		config.SetDirectorySharingDevicesVirtualMachineConfiguration(m.shares)
	}
	if ok, err := config.Validate(); !ok || err != nil {
		return nil, fmt.Errorf("VM configuration is not valid: %w", err)
	}
	return vz.NewVirtualMachine(config)
}

// Create installs macOS from a local IPSW into a new image: a disk, the
// auxiliary storage the boot loader needs, the hardware model the IPSW
// asks for, and the image's own machine identifier. It is made beside its
// final place and moved there whole, so a failed install leaves nothing.
func Create(ctx context.Context, options CreateOptions) error {
	say := func(format string, args ...any) { fmt.Fprintf(options.Say, format+"\n", args...) }
	if _, err := os.Stat(filepath.Join(options.Dir, manifestFile)); err == nil {
		return fmt.Errorf("there is already a macOS image in %s; remove it first with: hived macos image remove", options.Dir)
	}
	restore, err := vz.LoadMacOSRestoreImageFromPath(options.IPSW)
	if err != nil {
		return fmt.Errorf("reading %s: %w", options.IPSW, err)
	}
	requirements := restore.MostFeaturefulSupportedConfiguration()
	if requirements == nil {
		return fmt.Errorf("%s is a macOS this Mac cannot run in a VM", options.IPSW)
	}
	model := requirements.HardwareModel()
	if !model.Supported() {
		return fmt.Errorf("this Mac does not support the hardware model %s asks for", options.IPSW)
	}

	staging := options.Dir + ".creating"
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return err
	}
	finished := false
	defer func() {
		if !finished {
			os.RemoveAll(staging)
		}
	}()
	identifier, err := vz.NewMacMachineIdentifier()
	if err != nil {
		return err
	}
	for name, data := range map[string][]byte{
		HardwareModelFile: model.DataRepresentation(),
		IdentifierFile:    identifier.DataRepresentation(),
	} {
		if err := os.WriteFile(filepath.Join(staging, name), data, 0o600); err != nil {
			return err
		}
	}
	// Sparse: it takes only what macOS and the owner write to it.
	disk := filepath.Join(staging, DiskFile)
	file, err := os.OpenFile(disk, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	err = file.Truncate(int64(options.DiskGB) << 30)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	auxiliary, err := vz.NewMacAuxiliaryStorage(filepath.Join(staging, AuxiliaryFile), vz.WithCreatingMacAuxiliaryStorage(model))
	if err != nil {
		return fmt.Errorf("auxiliary storage: %w", err)
	}
	total, _ := unix.SysctlUint64("hw.memsize")
	vm, err := configure(machine{
		vcpus:      uint(max(uint64(VCPUs), requirements.MinimumSupportedCPUCount())),
		memory:     max(Memory(total), requirements.MinimumSupportedMemorySize()),
		disk:       disk,
		model:      model,
		auxiliary:  auxiliary,
		identifier: identifier,
	})
	if err != nil {
		return err
	}
	installer, err := vz.NewMacOSInstaller(vm, options.IPSW)
	if err != nil {
		return err
	}
	version := restore.OperatingSystemVersion().String() + " (" + restore.BuildVersion() + ")"
	say("Installing macOS %s. This takes a while.", version)
	go func() {
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-installer.Done():
				return
			case <-tick.C:
				say("  %d%%", int(installer.FractionCompleted()*100))
			}
		}
	}()
	if err := installer.Install(ctx); err != nil {
		return fmt.Errorf("installing macOS: %w", err)
	}
	if vm.CanStop() {
		_ = vm.Stop()
	}
	image := Image{Dir: staging, MacOS: version, Created: time.Now().UTC()}
	if err := image.save(); err != nil {
		return err
	}
	if err := os.Rename(staging, options.Dir); err != nil {
		return err
	}
	finished = true
	return nil
}

// Open boots the image itself, not a clone, in a window, for its owner to
// set up by hand. It returns when the window closes or the VM shuts down.
// The guest's files are shared under /Volumes/My Shared Files/hive, and the
// owner's folder, if any, under /Volumes/My Shared Files/files; both read
// only. Whatever the owner changes, the image needs sealing again after.
//
// The caller must be on the process's main thread: macOS draws windows
// nowhere else.
func Open(ctx context.Context, options OpenOptions) error {
	release, err := lockImage(options.Dir, true)
	if err != nil {
		return err
	}
	defer release()
	image, err := Load(options.Dir)
	if err != nil {
		return err
	}
	model, err := vz.NewMacHardwareModelWithDataPath(image.path(HardwareModelFile))
	if err != nil {
		return fmt.Errorf("hardware model: %w", err)
	}
	auxiliary, err := vz.NewMacAuxiliaryStorage(image.path(AuxiliaryFile))
	if err != nil {
		return fmt.Errorf("auxiliary storage: %w", err)
	}
	identifier, err := vz.NewMacMachineIdentifierWithDataPath(image.path(IdentifierFile))
	if err != nil {
		return fmt.Errorf("machine identifier: %w", err)
	}
	files, err := os.MkdirTemp("", "hive-macos-guest-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(files)
	if err := WriteGuestFiles(files); err != nil {
		return err
	}
	shared := map[string]string{"hive": files}
	if options.Share != "" {
		shared["files"] = options.Share
	}
	directories := map[string]*vz.SharedDirectory{}
	for name, path := range shared {
		if directories[name], err = vz.NewSharedDirectory(path, true); err != nil {
			return fmt.Errorf("share %s: %w", path, err)
		}
	}
	multiple, err := vz.NewMultipleDirectoryShare(directories)
	if err != nil {
		return err
	}
	tag, err := vz.MacOSGuestAutomountTag()
	if err != nil {
		return err
	}
	share, err := vz.NewVirtioFileSystemDeviceConfiguration(tag)
	if err != nil {
		return err
	}
	share.SetDirectoryShare(multiple)
	total, _ := unix.SysctlUint64("hw.memsize")
	vm, err := configure(machine{
		vcpus:      uint(min(VCPUs, runtime.NumCPU())),
		memory:     Memory(total),
		disk:       image.path(DiskFile),
		model:      model,
		auxiliary:  auxiliary,
		identifier: identifier,
		shares:     []vz.DirectorySharingDeviceConfiguration{share},
	})
	if err != nil {
		return err
	}
	if err := vm.Start(); err != nil {
		return fmt.Errorf("start the VM: %w", err)
	}
	fmt.Fprintln(options.Say, "The image is open in a window. Shut it down from its Apple menu when you are done, then run: hived macos image seal")
	go func() {
		<-ctx.Done()
		if vm.CanStop() {
			_ = vm.Stop()
		}
	}()
	err = vm.StartGraphicApplication(1280, 800, vz.WithWindowTitle("Hive Runner macOS image"), vz.WithController(true))
	// The window closed with the VM still running: macOS is asked to shut
	// down, and stopped if it has not within a minute.
	if vm.CanRequestStop() {
		if ok, _ := vm.RequestStop(); ok {
			deadline := time.After(time.Minute)
			for vm.State() != vz.VirtualMachineStateStopped {
				select {
				case <-deadline:
					_ = vm.Stop()
					return errors.Join(err, errors.New("the VM did not shut down in a minute and was stopped"))
				case <-time.After(time.Second):
				}
			}
		}
	}
	return err
}
