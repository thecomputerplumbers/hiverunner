//go:build linux

package vm

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

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
}

// The programs a VM is made of on Linux. KVM does the virtualization; QEMU
// is the machine around it; virtiofsd serves the one shared directory; passt
// is the guest's network, with no privileges and no way to the host's own
// loopback.
type tools struct {
	qemu, qemuImg, virtiofsd, passt string
}

// Preflight reports why this host cannot run VMs, in words its owner can act
// on, or nil if it can.
func Preflight() error {
	_, err := findTools()
	if err != nil {
		return err
	}
	return kvmUsable()
}

func kvmUsable() error {
	file, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("this machine has no /dev/kvm: virtualization is switched off in its firmware, or this is a cloud VM without nested virtualization")
	}
	if errors.Is(err, os.ErrPermission) {
		return errors.New("your user cannot open /dev/kvm: run `sudo usermod -aG kvm $USER`, then log out and back in")
	}
	if err != nil {
		return fmt.Errorf("/dev/kvm: %w", err)
	}
	return file.Close()
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
			} else if resolved, err := exec.LookPath(name); err == nil {
				*into = resolved
				return
			}
		}
		missing = append(missing, names[0])
	}
	look(&found.qemu, system)
	look(&found.qemuImg, "qemu-img")
	// Distributions keep virtiofsd out of PATH.
	look(&found.virtiofsd, "virtiofsd", "/usr/libexec/virtiofsd", "/usr/lib/qemu/virtiofsd", "/usr/lib/virtiofsd")
	look(&found.passt, "passt")
	if len(missing) > 0 {
		return tools{}, fmt.Errorf("missing %s. On Debian or Ubuntu: sudo apt install qemu-system qemu-utils virtiofsd passt", strings.Join(missing, ", "))
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
		if err := r.listen(); err != nil {
			r.Log.Warn("this VM cannot reach the cache", "error", err)
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
	forget := func() {}
	defer func() {
		if err != nil {
			forget()
			stopHelpers()
			os.RemoveAll(sockets)
		}
	}()
	// A helper never outlives the daemon, however the daemon dies. (The
	// signal follows the thread that started the helper; the Go runtime
	// keeps its threads for the life of the process.)
	run := func(log string, name string, args ...string) (*exec.Cmd, error) {
		command := exec.Command(name, args...)
		command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
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
	shareSocket := filepath.Join(sockets, "fs")
	networkSocket := filepath.Join(sockets, "net")

	// The whole of what the guest can see of the host's files, served from
	// inside a namespace that contains nothing else.
	share, err := run("virtiofsd.log", found.virtiofsd,
		"--socket-path="+shareSocket, "--shared-dir="+vm.share, "--cache=never")
	if err != nil {
		return "", err
	}
	helpers = append(helpers, share)

	// The guest's network: out through this user's ordinary sockets, nothing
	// in, and no address that means the host's own loopback.
	// (It also writes a few lines to the system log for each VM. Sending them
	// to a file instead is refused by the confinement distributions ship it
	// with, which lets it create a log file but not open one.)
	network := []string{"--foreground", "--quiet", "--one-off", "--no-map-gw", "--socket", networkSocket}
	for _, server := range upstreamDNS() {
		network = append(network, "--dns", server)
	}
	relay, err := run("passt.log", found.passt, network...)
	if err != nil {
		return "", err
	}
	helpers = append(helpers, relay)
	for _, socket := range []string{shareSocket, networkSocket} {
		if err := waitFor(socket, 10*time.Second, share, relay); err != nil {
			return "", fmt.Errorf("%w (%s)", err, tail(vm.dir, "virtiofsd.log", "passt.log"))
		}
	}

	var sysinfo unix.Sysinfo_t
	_ = unix.Sysinfo(&sysinfo)
	vcpus := min(max(vm.vcpus, 1), runtime.NumCPU())
	megabytes := GuestMemoryBytes(vcpus, uint64(sysinfo.Totalram)*uint64(sysinfo.Unit)) >> 20
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
		"-netdev", "stream,id=net,server=off,addr.type=unix,addr.path=" + networkSocket,
		"-device", "virtio-net-pci,netdev=net,mac=" + mac.String(),
		"-chardev", "socket,id=share,path=" + shareSocket,
		"-device", "vhost-user-fs-pci,chardev=share,tag=" + shareTag,
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

	machine, err := run("qemu.log", found.qemu, args...)
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
	go func() {
		waitErr := <-exited
		// Powered off by the guest, or killed by us (which has no exit
		// status): both are a stop. Any other end is the VM failing.
		var exit *exec.ExitError
		if errors.As(waitErr, &exit) && exit.ExitCode() > 0 {
			vm.failure = fmt.Errorf("the VM ended with status %d (%s)", exit.ExitCode(), tail(vm.dir, "qemu.log"))
		}
		stopHelpers()
		os.RemoveAll(sockets)
		close(vm.ended)
	}()
	return "", nil
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

// upstreamDNS is what the guest should be told to resolve names with when
// the host's own resolver is on loopback, where the guest cannot reach it.
// Empty means the host's resolv.conf is usable as it is.
func upstreamDNS() []string {
	servers := nameservers("/etc/resolv.conf")
	usable := servers[:0:0]
	for _, server := range servers {
		if ip := net.ParseIP(server); ip != nil && !ip.IsLoopback() {
			usable = append(usable, server)
		}
	}
	if len(usable) > 0 {
		return nil
	}
	// systemd-resolved keeps the real servers here.
	for _, server := range nameservers("/run/systemd/resolve/resolv.conf") {
		if ip := net.ParseIP(server); ip != nil && !ip.IsLoopback() {
			usable = append(usable, server)
		}
	}
	if len(usable) == 0 {
		// A guest that cannot resolve names cannot run a job at all.
		usable = []string{"1.1.1.1", "8.8.8.8"}
	}
	return usable
}

func nameservers(file string) []string {
	handle, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer handle.Close()
	var servers []string
	scanner := bufio.NewScanner(handle)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "nameserver" {
			// An IPv6 server may carry a zone, which is not part of it.
			servers = append(servers, strings.SplitN(fields[1], "%", 2)[0])
		}
	}
	return servers
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
// the hypervisor's own, not the guest's console.
func tail(dir string, names ...string) string {
	var parts []string
	for _, name := range names {
		raw, _ := os.ReadFile(filepath.Join(dir, name))
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
