//go:build linux

package vm

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/fence"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/sharefile"
)

// FencesNetwork is whether a guest here is held to its fence.Policy. It
// is on Linux: every frame between QEMU and passt goes through hived.
const FencesNetwork = true

// What a Linux host keeps between VMs: the one socket every guest dials, and
// which VM each caller is.
type platformState struct {
	once     sync.Once
	listener *vsockListener
	listenOn uint32
	err      error
	mu       sync.Mutex
	// guests maps a VM's socket address to what answers it. The kernel
	// assigns that address to the VM; a guest cannot claim another's.
	guests map[uint32]http.Handler
	// unconfined is set once this host's QEMU has refused to confine
	// itself, so later VMs do not ask again.
	unconfined bool
	// complain says once, not for every VM, why there is no cache.
	complain sync.Once
	// readOnly is whether this host's virtiofsd can serve a directory the
	// guest cannot change, asked once.
	readOnly     sync.Once
	readOnlyOK   bool
	readOnlySaid sync.Once
}

// readOnlyShares reports whether virtiofsd at this path can serve what a
// guest is handed read-only. virtiofsd 1.11 and later can. An older one,
// as some distributions package, serves it writable: the host still never
// writes through or reads back what the guest leaves there (see package
// sharefile), but a guest could change what it was handed. That is said
// once.
func (r *Runner) readOnlyShares(virtiofsd string) bool {
	state := &r.platform
	state.readOnly.Do(func() {
		said, _ := exec.Command(virtiofsd, "--help").CombinedOutput()
		state.readOnlyOK = offersReadOnly(string(said))
	})
	if !state.readOnlyOK {
		state.readOnlySaid.Do(func() {
			r.Log.Warn("this machine's virtiofsd cannot share a directory read-only; a job's VM can change what it is handed. Install virtiofsd 1.11 or later", "virtiofsd", virtiofsd)
		})
	}
	return state.readOnlyOK
}

// offersReadOnly reports whether virtiofsd's --help lists --readonly.
func offersReadOnly(help string) bool {
	for _, field := range strings.Fields(help) {
		if strings.TrimRight(field, ",") == "--readonly" {
			return true
		}
	}
	return false
}

// The programs a VM is made of on Linux. KVM does the virtualization; QEMU
// is the machine around it; virtiofsd serves each shared directory; passt
// is the guest's network, with no privileges and no way to the host's own
// loopback.
type tools struct {
	qemu, qemuImg, virtiofsd, passt string
}

// Preflight reports why this host cannot run VMs, in words its owner can act
// on, or nil if it can.
func Preflight() error {
	found, err := findTools()
	if err != nil {
		return err
	}
	if err := kvmUsable(); err != nil {
		return err
	}
	if blocked := appArmorBlocks(found.virtiofsd, bundledVirtiofsd(), os.ReadFile, os.ReadDir); blocked != "" {
		return errors.New(blocked)
	}
	return nil
}

// bundledVirtiofsd is where the installer puts its own virtiofsd, on a
// system that packages none able to run without root.
func bundledVirtiofsd() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "HiveWorker", "bin", "virtiofsd")
	}
	return ""
}

func kvmUsable() error {
	file, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("this machine has no /dev/kvm: virtualization is switched off in its firmware, or this is a cloud VM without nested virtualization")
	}
	found := denied("/dev/kvm")
	if errors.Is(err, os.ErrPermission) {
		return errors.New(found.reason())
	}
	if err != nil {
		return fmt.Errorf("/dev/kvm: %w", err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	if reason := found.wontLast(); reason != "" {
		return errors.New(reason)
	}
	return nil
}

// vsockUsable reports whether this user may give a VM a socket to the host.
// The device is often the kvm group's alone, while /dev/kvm itself is also
// lent to whoever is sitting at the machine; so one can work without the
// other.
func vsockUsable() error {
	file, err := os.OpenFile("/dev/vhost-vsock", os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("the vhost_vsock module is not loaded: sudo modprobe vhost_vsock")
	}
	if errors.Is(err, os.ErrPermission) {
		return errors.New(denied("/dev/vhost-vsock").reason())
	}
	if err != nil {
		return fmt.Errorf("/dev/vhost-vsock: %w", err)
	}
	return file.Close()
}

// denied looks at a device this user could not open, for what to say of it.
func denied(path string) deniedDevice {
	found := deniedDevice{Path: path, UID: os.Getuid(), Group: "kvm", KVM: true}
	kvm, err := user.LookupGroup("kvm")
	if err != nil {
		return found
	}
	var stat unix.Stat_t
	if unix.Stat(path, &stat) == nil {
		owner := strconv.FormatUint(uint64(stat.Gid), 10)
		found.KVM = owner == kvm.Gid
		found.Group = owner
		if named, err := user.LookupGroupId(owner); err == nil {
			found.Group = named.Name
		}
	}
	if me, err := user.Current(); err == nil {
		found.User = me.Username
		if listed, err := me.GroupIds(); err == nil {
			found.Listed = slices.Contains(listed, kvm.Gid)
		}
	}
	found.Early, found.Late, found.Uaccess = inspectUdev(found.User, os.ReadFile, os.ReadDir)
	if held, err := os.Getgroups(); err == nil {
		gid, _ := strconv.Atoi(kvm.Gid)
		found.Holds = slices.Contains(held, gid) || os.Getegid() == gid
	}
	release, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	found.Windows = strings.Contains(strings.ToLower(string(release)), "microsoft")
	return found
}

func findTools() (tools, error) {
	system := map[string]string{"arm64": "qemu-system-aarch64", "amd64": "qemu-system-x86_64"}[runtime.GOARCH]
	if system == "" {
		return tools{}, fmt.Errorf("no VM support for %s", runtime.GOARCH)
	}
	var found tools
	var missing []string
	look := func(into *string, names ...string) {
		for _, name := range names {
			if strings.HasPrefix(name, "/") {
				if info, err := os.Stat(name); err == nil && !info.IsDir() {
					*into = name
					return
				}
			} else if name == "" {
				continue
			} else if resolved, err := exec.LookPath(name); err == nil {
				*into = resolved
				return
			}
		}
		missing = append(missing, filepath.Base(names[len(names)-1]))
	}
	look(&found.qemu, system)
	look(&found.qemuImg, "qemu-img")
	// The installer's own copy first: it is there only on a system that
	// packages no virtiofsd able to run without root. Otherwise the
	// system's, which distributions keep out of PATH. Not /usr/lib/qemu:
	// what lives there is the older one from inside QEMU, which must be root.
	look(&found.virtiofsd, bundledVirtiofsd(), "virtiofsd", "/usr/libexec/virtiofsd", "/usr/lib/virtiofsd")
	look(&found.passt, "passt")
	if len(missing) > 0 {
		return tools{}, fmt.Errorf("%s missing: on Debian or Ubuntu run `sudo apt install qemu-system qemu-utils passt`, and the installer again for virtiofsd", strings.Join(missing, ", "))
	}
	return found, nil
}

// launch starts the VM under KVM. Its disk is an overlay on the guest image:
// instant on any filesystem, and holding only what the job writes.
func (r *Runner) launch(vm *instance, mac net.HardwareAddr) error {
	found, err := findTools()
	if err != nil {
		return err
	}
	if err := kvmUsable(); err != nil {
		return err
	}
	if r.Image.Arch() != runtime.GOARCH {
		return fmt.Errorf("the guest image is for %s and this machine is %s", r.Image.Arch(), runtime.GOARCH)
	}
	disk := filepath.Join(vm.dir, "disk.qcow2")
	if out, err := exec.Command(found.qemuImg, "create", "-q", "-f", "qcow2",
		"-b", r.Image.path(r.Image.Disk), "-F", "raw", disk, strconv.FormatInt(diskBytes, 10)).CombinedOutput(); err != nil {
		return fmt.Errorf("create the job's disk: %w: %s", err, strings.TrimSpace(string(out)))
	}

	// A socket between this VM and the host, on no network at all: the guest
	// reaches the host's cache through it, and nothing else can. A VM
	// without it still runs jobs; they use GitHub's cache.
	offered := false
	if r.Guest != nil {
		if err := vsockUsable(); err != nil {
			r.platform.complain.Do(func() { r.Log.Warn("VMs on this machine cannot reach the cache", "error", err) })
		} else if err := r.listen(); err != nil {
			r.platform.complain.Do(func() { r.Log.Warn("VMs on this machine cannot reach the cache", "error", err) })
		} else {
			offered = true
		}
	}

	// A machine that cannot start says so at once, and two of its reasons
	// are ours to work around: a socket address some other VM on this host
	// already has, and a QEMU built without the means to confine itself.
	for attempt := 0; ; attempt++ {
		r.platform.mu.Lock()
		confined := !r.platform.unconfined
		r.platform.mu.Unlock()
		address := uint32(0)
		if offered {
			address = r.socketAddress(vm, attempt)
		}
		said, err := r.start(found, vm, mac, disk, address, confined)
		if err == nil {
			return nil
		}
		switch {
		case attempt < 3 && strings.Contains(said, "guest cid"):
		case attempt < 3 && confined && (strings.Contains(said, "sandbox") || strings.Contains(said, "seccomp")):
			r.platform.mu.Lock()
			r.platform.unconfined = true
			r.platform.mu.Unlock()
			r.Log.Warn("this QEMU cannot confine itself; VMs run without its seccomp filter")
		default:
			return err
		}
	}
}

// start brings up one VM and the two helpers beside it, and returns once the
// machine is running. If the machine stops at once, nothing is left behind
// and what QEMU said is returned with the error.
func (r *Runner) start(found tools, vm *instance, mac net.HardwareAddr, disk string, address uint32, confined bool) (said string, err error) {
	var helpers []*exec.Cmd
	stopHelpers := func() {
		for _, helper := range helpers {
			_ = helper.Process.Kill()
			_ = helper.Wait()
		}
	}
	// Sockets live in a short path of their own: a socket's path has a small
	// fixed limit, and a work directory can be deep.
	sockets, err := os.MkdirTemp("", "hive-")
	if err != nil {
		return "", err
	}
	forget, unfence := func() {}, func() {}
	defer func() {
		if err != nil {
			unfence()
			forget()
			stopHelpers()
			os.RemoveAll(sockets)
		}
	}()
	// A helper never outlives the daemon, however the daemon dies. (The
	// signal follows the thread that started the helper; the Go runtime
	// keeps its threads for the life of the process.)
	// A file handed over is the helper's descriptor 3 (inheritedFD).
	run := func(log string, name string, inherit *os.File, args ...string) (*exec.Cmd, error) {
		command := exec.Command(name, args...)
		command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
		if inherit != nil {
			command.ExtraFiles = []*os.File{inherit}
		}
		output, err := os.OpenFile(filepath.Join(vm.dir, log), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return nil, err
		}
		defer output.Close()
		command.Stdout, command.Stderr = output, output
		if err := command.Start(); err != nil {
			return nil, fmt.Errorf("start %s: %w", filepath.Base(name), err)
		}
		return command, nil
	}
	inSocket := filepath.Join(sockets, "in")
	outSocket := filepath.Join(sockets, "out")

	// The whole of what the guest can see of the host's files, served from
	// inside a namespace that contains nothing else: what it is handed, which
	// it cannot change where virtiofsd can say so, and where it writes back.
	handed := []string{"--socket-path=" + inSocket, "--shared-dir=" + vm.in, "--cache=never"}
	if r.readOnlyShares(found.virtiofsd) {
		handed = append(handed, "--readonly")
	}
	shareIn, err := run("virtiofsd-in.log", found.virtiofsd, nil, handed...)
	if err != nil {
		return "", err
	}
	helpers = append(helpers, shareIn)
	shareOut, err := run("virtiofsd-out.log", found.virtiofsd, nil,
		"--socket-path="+outSocket, "--shared-dir="+vm.out, "--cache=never")
	if err != nil {
		return "", err
	}
	helpers = append(helpers, shareOut)

	// The guest's network: out through this user's ordinary sockets, nothing
	// in, and no address that means the host's own loopback.
	// (It also writes a few lines to the system log for each VM. Sending them
	// to a file instead is refused by the confinement distributions ship it
	// with, which lets it create a log file but not open one.)
	//
	// Each is handed one end of a socket made here, and not one passt
	// listens on: the confinement Proxmox VE 9 runs passt under lets it
	// listen and then refuses the accept, and passt says nothing of it, so
	// the guest boots with a network card that reaches nowhere.
	//
	// hived keeps the other two ends, and stands between them: a frame the
	// guest sends reaches passt only if the guest may reach where it is
	// going. See package fence.
	relayEnd, fenceNetwork, err := socketPair()
	if err != nil {
		return "", err
	}
	defer relayEnd.Close()
	fenceGuest, machineEnd, err := socketPair()
	if err != nil {
		fenceNetwork.Close()
		return "", err
	}
	defer machineEnd.Close()
	toNetwork, toGuest, err := conns(fenceNetwork, fenceGuest)
	if err != nil {
		return "", err
	}
	unfence = func() {
		toNetwork.Close()
		toGuest.Close()
	}
	// The host's search domains stay with the host. A job resolves the
	// names it wrote, not those names inside the owner's tailnet or LAN,
	// and does not ask a public resolver about either.
	network := []string{"--foreground", "--quiet", "--one-off", "--no-map-gw", "--no-dhcp-search", "--fd", inheritedFD}
	chosen := upstreamDNS()
	if r.PublicDNS != nil && r.PublicDNS() {
		chosen = publicResolvers()
	}
	for _, server := range chosen {
		network = append(network, "--dns", server)
	}
	vm.resolvers = guestResolvers(chosen, "/etc/resolv.conf")
	if err = r.refence(vm); err != nil {
		return "", err
	}
	relay, err := run("passt.log", found.passt, relayEnd, network...)
	if err != nil {
		return "", err
	}
	helpers = append(helpers, relay)
	for _, socket := range []string{inSocket, outSocket} {
		if err := waitFor(socket, 10*time.Second, shareIn, shareOut, relay); err != nil {
			return "", fmt.Errorf("%w (%s)", err, tail(vm.dir, "virtiofsd-in.log", "virtiofsd-out.log", "passt.log"))
		}
	}

	var sysinfo unix.Sysinfo_t
	_ = unix.Sysinfo(&sysinfo)
	vcpus := min(max(vm.vcpus, 1), runtime.NumCPU())
	vm.memory = sized(vm.memory, GuestMemoryBytes(vcpus, uint64(sysinfo.Totalram)*uint64(sysinfo.Unit)))
	megabytes := vm.memory >> 20
	memory := strconv.FormatUint(megabytes, 10) + "M"

	args := []string{
		"-nodefaults", "-no-user-config", "-display", "none", "-no-reboot",
		"-accel", "kvm", "-cpu", "host",
		"-smp", strconv.Itoa(vcpus), "-m", memory,
		// Guest memory the share's server can see: virtio-fs needs it.
		"-object", "memory-backend-memfd,id=mem,share=on,size=" + memory,
		"-kernel", r.Image.path(r.Image.Kernel),
		"-initrd", r.Image.path(r.Image.Initrd),
		"-append", r.Image.CommandLine,
		// A throwaway disk: nothing on it needs to survive a crash.
		"-drive", "if=none,id=disk,format=qcow2,cache=unsafe,discard=unmap,file=" + disk,
		"-device", "virtio-blk-pci,drive=disk",
		"-netdev", "stream,id=net,server=off,addr.type=fd,addr.str=" + inheritedFD,
		// No option ROM: the kernel is booted directly, never from the
		// network, and the ROM is a separate package some systems lack.
		"-device", "virtio-net-pci,netdev=net,romfile=,mac=" + mac.String(),
		"-chardev", "socket,id=in,path=" + inSocket,
		"-device", "vhost-user-fs-pci,chardev=in,tag=" + InTag,
		"-chardev", "socket,id=out,path=" + outSocket,
		"-device", "vhost-user-fs-pci,chardev=out,tag=" + OutTag,
		// The guest's console goes to a file, for diagnosis. Nothing is
		// wired to its input: there is no way to type into the guest.
		"-device", "virtio-serial-pci",
		"-chardev", "file,id=console,path=" + vm.console,
		"-device", "virtconsole,chardev=console",
		"-device", "virtio-rng-pci",
		"-device", "virtio-balloon-pci",
	}
	switch runtime.GOARCH {
	case "arm64":
		args = append(args, "-machine", "virt,gic-version=host,memory-backend=mem")
	case "amd64":
		args = append(args, "-machine", "q35,memory-backend=mem")
	}
	if confined {
		// QEMU gives up what it does not need: new privileges, new programs,
		// and the system calls no machine has a use for.
		args = append(args, "-sandbox", "on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny")
	}
	if address != 0 {
		args = append(args, "-device", "vhost-vsock-pci,guest-cid="+strconv.FormatUint(uint64(address), 10))
		// Known before the guest boots, so it is never refused.
		r.platform.mu.Lock()
		r.platform.guests[address] = r.Guest.Handler(vm.owner)
		r.platform.mu.Unlock()
		forget = func() {
			r.platform.mu.Lock()
			delete(r.platform.guests, address)
			r.platform.mu.Unlock()
		}
	}

	// The fence runs for as long as the VM does: the VM going hangs up on
	// it, and it hangs up on passt.
	go func() {
		if err := fence.Relay(toGuest, toNetwork, vm.network.Load); err != nil && vm.alive() {
			r.Log.Warn("the VM's network stopped", "error", err)
		}
	}()

	// The VM is in a scope of its own, so the kernel killing it for memory
	// ends the scope and not the daemon's service with it.
	qemu, qemuArgs := inOwnScope(scopesAvailable(), filepath.Base(vm.dir), found.qemu, args)
	machine, err := run("qemu.log", qemu, machineEnd, qemuArgs...)
	if err != nil {
		return "", err
	}
	exited := make(chan error, 1)
	go func() { exited <- machine.Wait() }()
	select {
	case waitErr := <-exited:
		said = tail(vm.dir, "qemu.log")
		return said, fmt.Errorf("the VM did not start: %v (%s)", waitErr, said)
	case <-time.After(300 * time.Millisecond):
	}

	var stopping sync.Once
	vm.stop = func() { stopping.Do(func() { _ = machine.Process.Kill() }) }
	vm.served = address != 0
	vm.unserve = forget
	// The machine's own addresses can change while the VM runs.
	go vm.watchHost(hostEvery)
	go func() {
		waitErr := <-exited
		vm.peak.Store(peakBytes(machine.ProcessState))
		// Powered off by the guest, or killed by us (which has no exit
		// status): both are a stop. Any other end is the VM failing.
		var exit *exec.ExitError
		if errors.As(waitErr, &exit) {
			if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGKILL && !vm.stopped.Load() {
				hostKilledVM(vm)
				vm.failure = fmt.Errorf("the host killed the VM (%s)", tail(vm.dir, "qemu.log"))
			} else if exit.ExitCode() > 0 {
				vm.failure = fmt.Errorf("the VM ended with status %d (%s)", exit.ExitCode(), tail(vm.dir, "qemu.log"))
			}
		}
		stopHelpers()
		os.RemoveAll(sockets)
		close(vm.ended)
	}()
	return "", nil
}

// peakBytes is the most memory an exited process held at once: its
// resident high-water mark, what /proc/<pid>/status calls VmHWM, kept by the
// kernel for the parent once the process is gone. The VM's memory is QEMU's,
// so this is how much of its guest's memory the job touched, plus QEMU's own.
// Zero if it cannot be read; it is only ever reported.
func peakBytes(state *os.ProcessState) uint64 {
	if state == nil {
		return 0
	}
	return maxRSSBytes(state.SysUsage())
}

// maxRSSBytes reads ru_maxrss, which Linux gives in KiB.
func maxRSSBytes(usage any) uint64 {
	rusage, ok := usage.(*syscall.Rusage)
	if !ok || rusage == nil || rusage.Maxrss <= 0 {
		return 0
	}
	return uint64(rusage.Maxrss) << 10
}

var (
	scopeProbe sync.Once
	scopeOK    bool
)

// scopesAvailable is whether this login's systemd will start a transient
// scope for a VM. It is not where there is no systemd, or no user session.
func scopesAvailable() bool {
	scopeProbe.Do(func() {
		if _, err := exec.LookPath("systemd-run"); err != nil {
			return
		}
		scopeOK = exec.Command("systemd-run", "--user", "--scope", "--quiet", "--collect", "true").Run() == nil
	})
	return scopeOK
}

// inOwnScope is the command that starts a VM in a transient systemd scope of
// its own, or the command itself where there are no scopes. systemd-run
// replaces itself with the program once the scope exists, so the process
// started is still the VM: killing it, and the daemon's death signal to it,
// work as before.
func inOwnScope(available bool, name, program string, args []string) (string, []string) {
	if !available {
		return program, args
	}
	wrapped := []string{"--user", "--scope", "--quiet", "--collect",
		"--unit=hive-vm-" + unitSafe(name) + "-" + strconv.Itoa(os.Getpid()),
		"--description=Hive Runner job VM",
		"-p", "OOMPolicy=continue", "--", program}
	return "systemd-run", append(wrapped, args...)
}

// unitSafe keeps what a unit's name may hold.
func unitSafe(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '-'
	}, name)
}

// socketAddress is the address the VM's end of the host socket gets. It must
// be unique among every VM on the machine, ours or not, so it is spread by
// the work directory and then by the VM's slot. A VM beyond the slots, or
// one whose first address turned out to be taken, gets one from its own
// directory's name instead.
func (r *Runner) socketAddress(vm *instance, attempt int) uint32 {
	seed := sha256.Sum256([]byte(r.WorkDir))
	slot := max(vm.slot, 0)
	if vm.slot < 0 || attempt > 0 {
		seed = sha256.Sum256([]byte(vm.dir + strconv.Itoa(attempt)))
		slot = 0
	}
	// Above the reserved addresses (0 to 2) and well clear of the small
	// numbers people pick by hand.
	base := 0x10000 + binary.BigEndian.Uint32(seed[:4])%0x3fff0000
	return base&^0xf | uint32(slot)
}

// listen opens, once, the socket every guest of this runner dials. Each
// caller is told apart by the address the kernel gave its VM.
func (r *Runner) listen() error {
	state := &r.platform
	state.once.Do(func() {
		state.guests = map[uint32]http.Handler{}
		state.listener, state.err = listenVsock(r.Guest.Port)
		if state.err != nil {
			state.err = fmt.Errorf("open the host socket (is the vhost_vsock module loaded?): %w", state.err)
			return
		}
		service := &http.Server{
			ReadHeaderTimeout: 30 * time.Second,
			ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
				return context.WithValue(ctx, callerKey{}, conn.RemoteAddr().(vsockAddr).cid)
			},
			Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				caller, _ := request.Context().Value(callerKey{}).(uint32)
				state.mu.Lock()
				handler := state.guests[caller]
				state.mu.Unlock()
				if handler == nil {
					// Not one of ours: another VM on this machine, or a
					// process on the host itself.
					http.Error(w, "unknown caller", http.StatusForbidden)
					return
				}
				handler.ServeHTTP(w, request)
			}),
		}
		go func() { _ = service.Serve(state.listener) }()
	})
	return state.err
}

type callerKey struct{}

// inheritedFD is the number a helper knows the one file it is handed by:
// the first after its input, output and errors.
const inheritedFD = "3"

// socketPair is a connected stream socket, both ends. Neither end is passed
// on to a helper that was not handed it.
func socketPair() (*os.File, *os.File, error) {
	ends, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("make the guest's network socket: %w", err)
	}
	return os.NewFile(uintptr(ends[0]), "network"), os.NewFile(uintptr(ends[1]), "network"), nil
}

// conns are hived's two ends of the guest's network as connections, which
// the runtime's poller waits on. The files themselves are closed.
func conns(one, other *os.File) (net.Conn, net.Conn, error) {
	first, err := net.FileConn(one)
	one.Close()
	second, otherErr := net.FileConn(other)
	other.Close()
	if err == nil && otherErr == nil {
		return first, second, nil
	}
	for _, made := range []net.Conn{first, second} {
		if made != nil {
			made.Close()
		}
	}
	return nil, nil, fmt.Errorf("make the guest's network socket: %w", errors.Join(err, otherErr))
}

// upstreamDNS is what the guest should be told to resolve names with, or
// nil when the host's own resolv.conf will do as it is.
func upstreamDNS() []string {
	return guestDNS("/etc/resolv.conf", "/run/systemd/resolve/resolv.conf")
}

// waitFor waits for a helper's socket to appear, and gives up early if a
// helper has died.
func waitFor(socket string, limit time.Duration, helpers ...*exec.Cmd) error {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			return nil
		}
		for _, helper := range helpers {
			// Signal 0 asks only whether the process is still there.
			if helper.Process.Signal(syscall.Signal(0)) != nil {
				return fmt.Errorf("%s stopped before it was ready", filepath.Base(helper.Path))
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("%s was not ready in time", filepath.Base(socket))
}

// tail is the end of a helper's log, for an error message. These logs are
// the hypervisor's own, not the guest's console, but what it says may still
// be long: only the end is read.
func tail(dir string, names ...string) string {
	var parts []string
	for _, name := range names {
		raw, _ := sharefile.Tail(filepath.Join(dir, name), 4<<10)
		text := strings.TrimSpace(string(raw))
		if len(text) > 400 {
			text = text[len(text)-400:]
		}
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "; ")
}

// vsockListener accepts connections on the host's end of the VM sockets.
type vsockListener struct {
	file *os.File
	raw  syscall.RawConn
	port uint32
}

type vsockAddr struct{ cid, port uint32 }

func (a vsockAddr) Network() string { return "vsock" }
func (a vsockAddr) String() string  { return fmt.Sprintf("vsock:%d:%d", a.cid, a.port) }

func listenVsock(port uint32) (*vsockListener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		unix.Close(fd)
		return nil, err
	}
	if err := unix.Listen(fd, 64); err != nil {
		unix.Close(fd)
		return nil, err
	}
	// As a file, so the runtime's poller waits on it and Close wakes Accept.
	file := os.NewFile(uintptr(fd), "vsock")
	raw, err := file.SyscallConn()
	if err != nil {
		file.Close()
		return nil, err
	}
	return &vsockListener{file: file, raw: raw, port: port}, nil
}

func (l *vsockListener) Accept() (net.Conn, error) {
	var (
		fd     int
		peer   unix.Sockaddr
		failed error
	)
	err := l.raw.Read(func(listener uintptr) bool {
		fd, peer, failed = unix.Accept4(int(listener), unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK)
		return !errors.Is(failed, unix.EAGAIN)
	})
	if err != nil {
		return nil, err
	}
	if failed != nil {
		return nil, failed
	}
	remote := vsockAddr{}
	if address, ok := peer.(*unix.SockaddrVM); ok {
		remote = vsockAddr{cid: address.CID, port: address.Port}
	}
	return &vsockConn{
		File:   os.NewFile(uintptr(fd), "vsock"),
		local:  vsockAddr{cid: unix.VMADDR_CID_HOST, port: l.port},
		remote: remote,
	}, nil
}

func (l *vsockListener) Close() error { return l.file.Close() }
func (l *vsockListener) Addr() net.Addr {
	return vsockAddr{cid: unix.VMADDR_CID_HOST, port: l.port}
}

// vsockConn is one guest's connection. The file does the reading, writing
// and deadlines; this adds only who is on the other end.
type vsockConn struct {
	*os.File
	local, remote vsockAddr
}

func (c *vsockConn) LocalAddr() net.Addr  { return c.local }
func (c *vsockConn) RemoteAddr() net.Addr { return c.remote }

// dialVsockLoopback connects to this host's own end of the VM sockets, as a
// local process would. Only the tests do: it is how they stand in for a
// caller that is not one of the runner's guests.
func dialVsockLoopback(port uint32) (*os.File, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := unix.Connect(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_LOCAL, Port: port}); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), "vsock"), nil
}
