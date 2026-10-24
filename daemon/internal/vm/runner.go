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
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/fence"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/jobs"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/runnerrelease"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/sharefile"
)

const (
	// What a job's disk can grow to. The disk is a clone or an overlay, so
	// this costs nothing until the job writes.
	diskBytes = 40 * gib
	// The virtio-fs tags the guest agent mounts. What the host hands the
	// guest (the job, the clock, a stop's reason) is under InTag, which the
	// guest can only read. What it hands back (that it is ready, its exit
	// code, how the job went) is under OutTag, and anything there may be a
	// trap: see package sharefile. The host never writes there.
	InTag  = "hive"
	OutTag = "hive-out"
)

// MinImageVersion is the oldest guest image this daemon runs jobs in: the
// first whose agent reads the job from InTag and writes its results under
// OutTag.
const MinImageVersion = 12

// ReasonOldImage is why a machine whose guest image is older than
// MinImageVersion declines jobs.
const ReasonOldImage = "this machine's guest image is older than this hived can run jobs in; run hived update"

// What the guest may leave where its results go, and how long each result
// read back can be. The console is the guest's to fill, so only its end is
// read.
const (
	mostOutBytes    = 1 << 20
	mostOutEntries  = 64
	mostExit        = 32
	mostDiag        = 4 << 10
	mostSelfTest    = 64 << 10
	mostConsole     = 1 << 20
	mostKeptConsole = 16 << 20
	// A console longer than this is a guest filling the host's disk.
	mostConsoleBytes = 256 << 20
)

// How often a running job's results are looked at, and how long GitHub
// refusing the guest's runner declines jobs. Variables so the tests need
// not wait.
var (
	outPoll  = time.Second
	staleFor = time.Hour
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
	// OnUnusable, if set, is told when why this machine cannot run a VM
	// changes, with "" once it can again: hived doctor reads it from there,
	// since what the service may open is not what the owner's shell may.
	OnUnusable func(reason string)
	// Network is what the owner lets a job reach beyond the internet,
	// asked when a VM boots and again when it is given a job. Unset, it is
	// the internet alone. Where FencesNetwork is false nothing holds a
	// guest to it.
	Network func() fence.Policy
	// PublicDNS is whether a guest is given public resolvers rather than
	// the machine's own, asked when a VM boots: the owner's dns public.
	PublicDNS func() bool

	mu      sync.Mutex
	warm    *instance
	warming bool
	active  int
	// macs in use, by slot: see mac.
	macs [macSlots]bool
	// teardowns counts warm VMs being destroyed in the background.
	teardowns sync.WaitGroup
	// staleUntil is when a runner GitHub refused stops declining jobs, in
	// Unix nanoseconds. It expires because a job can write any exit code:
	// one job must not switch this machine off for good.
	staleUntil atomic.Int64
	// latest stands in for GitHub in tests: see runnerrelease.Latest.
	latest func(context.Context) (string, error)
	// unable is why the last look found this machine could not run a VM,
	// so that it is said when it changes and not on every look.
	unable atomic.Value
	// preflight stands in for Preflight in tests, and fakeVM for the host's
	// own launch.
	preflight func() error
	fakeVM    func(*instance) error
	// What the host's hypervisor keeps between VMs, if anything.
	platform platformState
}

// Unusable reports why this runner cannot run jobs, or "" if it can. It
// implements jobs.Unusable.
func (r *Runner) Unusable() string {
	if r.Image.ImageVersion < MinImageVersion {
		return ReasonOldImage
	}
	if r.stale() {
		return ReasonStaleImage
	}
	// Asked each time and not once at the start: what a VM needs of the
	// machine can go while the daemon runs, and come back. /dev/kvm changing
	// hands is one way, and a job offered then fails where it could have
	// gone to another machine.
	look := Preflight
	if r.preflight != nil {
		look = r.preflight
	}
	reason := ""
	if err := look(); err != nil {
		reason = "this machine cannot run a VM: " + err.Error()
	}
	if before, _ := r.unable.Swap(reason).(string); before != reason {
		if r.OnUnusable != nil {
			r.OnUnusable(reason)
		}
		if r.Log != nil && reason != "" {
			r.Log.Error("declining jobs until this is put right", "reason", reason)
		} else if r.Log != nil {
			r.Log.Info("this machine can run VMs again")
		}
	}
	return reason
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
	slot int
	dir  string
	// in is what the guest is handed, read-only to it; out is where it
	// writes back.
	in      string
	out     string
	console string
	vcpus   int
	booted  time.Time
	// ended is closed when the VM stops; failure says whether it stopped
	// any way but powering itself off, and is written before ended closes.
	ended   chan struct{}
	failure error
	// memory is the RAM the VM was given, for kill-reason messages.
	memory uint64
	// stopped is set when we pulled the plug, so a SIGKILL from us is not
	// reported as the host killing the VM.
	stopped atomic.Bool
	// hostKilled is set when the hypervisor died from outside, typically
	// the host OOM-killing QEMU.
	hostKilled bool
	// availableAtKill is the host's MemAvailable when hostKilled was set.
	availableAtKill uint64
	// peak is the most memory the hypervisor process held at once, in
	// bytes, read when it exited. Zero where that cannot be read.
	peak atomic.Uint64
	// stop pulls the plug. It is safe to call more than once, and on a VM
	// that has already stopped.
	stop func()
	// served is whether the guest can reach the host's service, and
	// unserve hangs up on it, along with any download still in flight.
	served  bool
	unserve func()
	// release returns the VM's network identity for the next one to use.
	release func()
	// resolvers are the name servers the guest was given when it booted.
	resolvers []netip.Addr
	// network is what the guest may reach: see fence.
	network atomic.Pointer[fence.Policy]
}

// refence holds the VM's guest to what the owner allows now, the
// resolvers it was given, and the machine's own addresses as they are now:
// a warm VM may have been booted on another network. What could not be
// read is as it was last read. A VM whose machine's addresses have never
// been read is not started, where its guest is held to the fence: it would
// reach the machine's public addresses and their neighbours.
func (r *Runner) refence(vm *instance) error {
	var policy fence.Policy
	if r.Network != nil {
		policy = r.Network()
	}
	policy.Resolvers = vm.resolvers
	was := vm.network.Load()
	found, err := hostNetworks()
	for try := 1; err != nil && was == nil && try < hostTries; try++ {
		time.Sleep(hostRetry)
		found, err = hostNetworks()
	}
	switch {
	case err == nil:
		policy.Host = found
	case was != nil:
		policy.Host = was.Host
	case FencesNetwork && !policy.Open:
		return fmt.Errorf("read this machine's own addresses, to keep the job off them: %w", err)
	}
	vm.network.Store(&policy)
	return nil
}

// How often, and how far apart, a VM's first read of the machine's own
// addresses is tried.
var (
	hostTries = 3
	hostRetry = 200 * time.Millisecond
)

// hostNetworks stands in for fence.HostNetworks in tests.
var hostNetworks = fence.HostNetworks

// hostEvery is how often a running VM's fence takes up the machine's own
// addresses again. One the machine gains during a job, a new temporary
// IPv6 address or a VPN's, is fenced off within it.
const hostEvery = 10 * time.Second

// rehost takes up the machine's addresses as they are now, and leaves the
// rest of what the guest may reach as it is: the owner's word applies from
// the next job, not halfway through this one.
// The machine's networks that cannot be read now are those last read.
func (vm *instance) rehost() {
	found, err := hostNetworks()
	if err != nil {
		return
	}
	for {
		was := vm.network.Load()
		var next fence.Policy
		if was != nil {
			next = *was
		}
		next.Host = found
		// A job handed over meanwhile set a whole new policy; this one
		// is then taken up again from that.
		if vm.network.CompareAndSwap(was, &next) {
			return
		}
	}
}

// watchHost keeps taking up the machine's addresses until the VM ends.
func (vm *instance) watchHost(every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-vm.ended:
			return
		case <-ticker.C:
			vm.rehost()
		}
	}
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
func (r *Runner) Run(ctx context.Context, job jobs.Job) (jobs.Result, error) {
	return r.run(ctx, job, "jitconfig", job.JITConfig, nil)
}

// SelfTest runs a VM with no credential and returns what the guest reports
// about itself: proof that boot, the share, the network and the runner
// binary work on this machine.
func (r *Runner) SelfTest(ctx context.Context, vcpus int) (string, error) {
	var report string
	result, err := r.run(ctx, jobs.Job{AssignmentID: "selftest", Pool: "selftest", VCPUs: vcpus}, "selftest", "1", func(out string) {
		raw, _ := sharefile.Read(filepath.Join(out, "selftest.out"), mostSelfTest)
		report = string(raw)
	})
	if err != nil {
		return report, err
	}
	if result.ExitCode != 0 {
		return report, fmt.Errorf("guest self-test exited %d", result.ExitCode)
	}
	return report, nil
}

// How long the guest has to notice stop-reason and write it into the job
// log before the plug is pulled.
const stopGrace = 2 * time.Second

func (r *Runner) run(ctx context.Context, job jobs.Job, file, content string, collect func(out string)) (result jobs.Result, err error) {
	id := job.AssignmentID
	started := time.Now()
	// An older guest would be handed its job where it can write.
	if r.Image.ImageVersion < MinImageVersion {
		return jobs.Result{ExitCode: -1}, errors.New(ReasonOldImage)
	}
	r.mu.Lock()
	r.active++
	vm := r.takeWarmLocked(job.VCPUs, job.MemoryBytes)
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.active--
		r.mu.Unlock()
	}()

	warm := vm != nil
	if vm == nil {
		if vm, err = r.boot(job.VCPUs, job.MemoryBytes); err != nil {
			return jobs.Result{ExitCode: -1}, err
		}
	}
	// Whatever happens: the disk, the shares and the credential go.
	defer vm.destroy()
	// What the guest may reach is what the owner allows now, not when a
	// warm VM booted. It is set before the job is.
	if err := r.refence(vm); err != nil {
		return jobs.Result{ExitCode: -1}, err
	}

	// The VM now works for this pool, and is told where the host's service
	// is before it is told about the job.
	extras := map[string]string{"host": hostName()}
	if job.Label != "" {
		extras["label"] = job.Label
	}
	if vm.served && r.Guest != nil && r.Guest.Enabled() {
		pool := job.Pool
		vm.pool.Store(&pool)
		extras[GuestServiceFile] = strconv.FormatUint(uint64(r.Guest.Port), 10)
	}
	if err := vm.handoff(extras, file, content); err != nil {
		return jobs.Result{ExitCode: -1}, err
	}
	r.Log.Info("VM has the job", "job", id, "vcpus", job.VCPUs, "label", job.Label, "warm", warm)

	err = vm.wait(ctx, file, r.askGuestToStop)

	if collect != nil {
		collect(vm.out)
	}
	code, hasExit := readExit(vm.out)
	if !hasExit && err == nil {
		err = errors.New("the VM stopped without reporting a result")
	}
	if code == ExitRunnerDeprecated && file == "jitconfig" && err == nil {
		r.refused()
	}
	end := classifyStop(vm, job, code, hasExit, ctx, err)
	result = jobs.Result{ExitCode: code, Reason: end.Reason, Detail: end.Detail, PeakGuestBytes: vm.peak.Load()}
	if errors.Is(err, context.Canceled) {
		// Stopped on purpose: released by the pool, or the daemon is
		// shutting down. The caller knows which; it is not a VM failing.
		r.Log.Info("VM stopped", "job", id, "exit", code, "reason", end.Reason, "seconds", int(time.Since(started).Seconds()))
		return result, err
	}
	if err != nil {
		// The guest's console is not logged. A job can write anything to it,
		// secrets included, and this log outlives the VM. Keeping it is an
		// explicit choice made while debugging a boot.
		r.Log.Error("VM failed", "job", id, "error", err, "reason", end.Reason, "console", vm.keepConsole())
	}
	if end.Detail != "" && end.Reason != jobs.ReasonReleased {
		r.Log.Error(end.Detail, "job", id, "reason", end.Reason, "exit", code)
	}
	r.Log.Info("VM ended", "job", id, "exit", code, "reason", end.Reason, "seconds", int(time.Since(started).Seconds()))
	return result, err
}

// wait returns when the VM has stopped, or when ctx ends, which stops it.
// Meanwhile, once the guest says it has taken the job's file it is removed,
// and a guest that leaves more where its results go than results can be, or
// writes more to its console, is stopped.
func (vm *instance) wait(ctx context.Context, file string, askToStop func(*instance, context.Context)) error {
	tick := time.NewTicker(outPoll)
	defer tick.Stop()
	taken := false
	for {
		select {
		case <-vm.ended:
			return vm.failure
		case <-ctx.Done():
			// A timeout or daemon shutdown: give the guest a moment to
			// write why into the GitHub log. A release is GitHub already
			// finished.
			askToStop(vm, ctx)
			vm.halt()
			return ctx.Err()
		case <-tick.C:
		}
		if !taken && sharefile.Present(filepath.Join(vm.out, "taken")) {
			// The agent has read the job, and nothing else in the VM
			// needs it.
			taken = true
			_ = os.Remove(filepath.Join(vm.in, file))
		}
		if sharefile.Overfull(vm.out, mostOutEntries, mostOutBytes) {
			vm.halt()
			return errors.New("the VM wrote more where its results go than results can be")
		}
		if info, err := os.Lstat(vm.console); err == nil && info.Size() > mostConsoleBytes {
			vm.halt()
			return errors.New("the VM wrote more to its console than a console can be")
		}
	}
}

// halt pulls the plug and waits a while for the VM to go.
func (vm *instance) halt() {
	if vm.alive() {
		vm.stop()
		select {
		case <-vm.ended:
		case <-time.After(15 * time.Second):
		}
	}
}

// stale reports whether jobs are declined because GitHub refused the
// guest's runner a short while ago.
func (r *Runner) stale() bool { return time.Now().UnixNano() < r.staleUntil.Load() }

// refused is a job's word that GitHub refused the guest's runner as too
// old. A job can write any exit code, so it is believed only when GitHub's
// latest runner is newer than the image's; then jobs are declined for a
// while, and asked about again after.
func (r *Runner) refused() {
	latest := r.latest
	if latest == nil {
		latest = runnerrelease.Latest
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	newest, err := latest(ctx)
	switch {
	case err != nil:
		r.Log.Warn("a job says GitHub refused the guest's runner, and GitHub could not be asked whether a newer one is out; carrying on", "runner", r.Image.RunnerVersion, "error", err)
	case !runnerrelease.Older(r.Image.RunnerVersion, newest):
		r.Log.Warn("a job says GitHub refused the guest's runner, but it is GitHub's latest; carrying on", "runner", r.Image.RunnerVersion)
	default:
		r.staleUntil.Store(time.Now().Add(staleFor).UnixNano())
		r.Log.Error("GitHub refused the guest's runner as too old; jobs are declined for an hour", "runner", r.Image.RunnerVersion, "latest", newest)
	}
}

func (r *Runner) askGuestToStop(vm *instance, ctx context.Context) {
	cause := context.Cause(ctx)
	reason := jobs.ReasonDaemonShutdown
	switch {
	case errors.Is(cause, jobs.ErrTimeout) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		reason = jobs.ReasonTimeout
	case errors.Is(cause, jobs.ErrReleased):
		return
	}
	_ = sharefile.WriteFresh(filepath.Join(vm.in, "stop-reason"), []byte(reason+"\n"))
	select {
	case <-vm.ended:
	case <-time.After(stopGrace):
	}
}

func classifyStop(vm *instance, job jobs.Job, code int, hasExit bool, ctx context.Context, err error) jobs.End {
	diag, _ := sharefile.Read(filepath.Join(vm.out, "diag"), mostDiag)
	used, total, swapUsed, swapTotal, oom, disk := jobs.ParseDiag(string(diag))
	console, _ := sharefile.Tail(vm.console, mostConsole)
	consoleOOM, consoleDisk, panicked := jobs.ScanConsole(string(console))
	mem := vm.memory
	if total > 0 {
		mem = total
	}
	cause := context.Cause(ctx)
	pulled := vm.stopped.Load()
	return jobs.Classify(jobs.Evidence{
		Host:               hostName(),
		Label:              job.Label,
		MemoryBytes:        mem,
		PeakUsedBytes:      used,
		SwapTotalBytes:     swapTotal,
		SwapUsedBytes:      swapUsed,
		GuestOOM:           oom || consoleOOM,
		DiskFull:           disk || consoleDisk,
		HostKilled:         vm.hostKilled && !pulled,
		SiblingKilled:      siblingKilled(vm),
		HostAvailableBytes: hostAvailable(vm),
		VMFailed:           !pulled && (vm.failure != nil || panicked || (err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded))),
		ExitCode:           code,
		HasExit:            hasExit,
		Timeout:            errors.Is(cause, jobs.ErrTimeout) || errors.Is(err, context.DeadlineExceeded),
		Released:           errors.Is(cause, jobs.ErrReleased),
		Shutdown:           errors.Is(err, context.Canceled) && !errors.Is(cause, jobs.ErrTimeout) && !errors.Is(cause, jobs.ErrReleased),
	})
}

func hostName() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "this machine"
	}
	return name
}

// readExit is the exit code the guest reported, if it reported one.
func readExit(out string) (int, bool) {
	raw, err := sharefile.Read(filepath.Join(out, "exit"), mostExit)
	if err != nil {
		return -1, false
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return -1, false
	}
	return parsed, true
}

// boot starts a fresh VM on a clone of the guest disk, with empty shares.
// The guest agent then waits for a job to appear in what it is handed.
// memory is less than its size gives, or zero for that.
func (r *Runner) boot(vcpus int, memory uint64) (*instance, error) {
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
		in:      filepath.Join(dir, "in"),
		out:     filepath.Join(dir, "out"),
		console: filepath.Join(dir, "console.log"),
		vcpus:   vcpus,
		memory:  memory,
		booted:  time.Now(),
		ended:   make(chan struct{}),
		stop:    func() {},
	}
	// The whole of what the guest can see of the host.
	for _, shared := range []string{vm.in, vm.out} {
		if err := os.Mkdir(shared, 0o700); err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
	}
	address, slot, release, err := r.mac()
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	vm.slot = slot
	vm.release = release
	// launch leaves nothing running when it fails.
	launch := r.launch
	if r.fakeVM != nil {
		launch = func(vm *instance, _ net.HardwareAddr) error { return r.fakeVM(vm) }
	}
	if err := launch(vm, address); err != nil {
		release()
		os.RemoveAll(dir)
		return nil, err
	}
	stop := vm.stop
	vm.stop = func() {
		vm.stopped.Store(true)
		stop()
	}
	return vm, nil
}

// handoff gives the VM its job. The clock goes first, because the guest has
// none it can trust and a warm VM may have been waiting a while. The job file
// appears whole or not at all, so the guest never reads half of one. Each is
// a new file: where the guest can write after all (see readOnlyShares), a
// name it left there is replaced, never written through.
func (vm *instance) handoff(extras map[string]string, file, content string) error {
	now := strconv.FormatInt(time.Now().Unix(), 10)
	if err := sharefile.WriteFresh(filepath.Join(vm.in, "clock"), []byte(now)); err != nil {
		return err
	}
	for name, value := range extras {
		if err := sharefile.WriteFresh(filepath.Join(vm.in, name), []byte(value)); err != nil {
			return err
		}
	}
	// Outside the share while it is being written: the guest sees only the
	// finished file.
	temporary := filepath.Join(vm.dir, "handoff.tmp")
	if err := sharefile.WriteFresh(temporary, []byte(content)); err != nil {
		return err
	}
	return os.Rename(temporary, filepath.Join(vm.in, file))
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
	if vm.alive() {
		// Still writing, perhaps: nothing it left is given back
		// permissions. What stays is swept at the next start.
		os.RemoveAll(vm.dir)
	} else {
		sharefile.RemoveAll(vm.dir)
	}
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

// takeWarmLocked hands over the warm VM if it suits the job. A job given
// less memory than its size does not get one: the warm VM has its size's.
func (r *Runner) takeWarmLocked(vcpus int, memory uint64) *instance {
	vm := r.warm
	if vm == nil || vm.vcpus != vcpus || memory != 0 || !vm.alive() {
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
	start := available && r.warm == nil && !r.warming && r.active == 0 && !r.stale() && r.Image.ImageVersion >= MinImageVersion
	if start {
		r.warming = true
	}
	r.mu.Unlock()
	if !start {
		return
	}

	vm, err := r.boot(r.WarmVCPUs, 0)
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
// by its owner alone, and only its end; otherwise it is deleted with the VM,
// unread.
func (vm *instance) keepConsole() string {
	keep := os.Getenv("HIVE_KEEP_CONSOLE")
	if keep == "" {
		return "discarded (set HIVE_KEEP_CONSOLE to a directory to keep it)"
	}
	if err := os.MkdirAll(keep, 0o700); err != nil {
		return "unavailable"
	}
	saved := filepath.Join(keep, filepath.Base(vm.dir)+".console.log")
	if err := sharefile.CopyTail(vm.console, saved, mostKeptConsole); err != nil {
		return "unavailable"
	}
	return saved
}

// hostKillMarker is touched in the work directory whenever the host kills a
// job's VM. The host killing one VM is the host out of memory, and the next
// thing it may take is hived or the other VMs; they then stop with nothing of
// their own to say. A file, not a variable, so a restarted daemon can see it.
const hostKillMarker = "host-oom"

// hostKilledVM records that the host killed vm, with how much memory the host
// had left. Call it before vm.ended closes.
func hostKilledVM(vm *instance) {
	vm.hostKilled = true
	vm.availableAtKill = memAvailable()
	_ = os.WriteFile(filepath.Join(filepath.Dir(vm.dir), hostKillMarker),
		[]byte(strconv.FormatUint(vm.availableAtKill, 10)+"\n"), 0o600)
}

// HostKillTime is when the host last killed a job's VM in this work
// directory, or the zero time if it has not.
func HostKillTime(workDir string) time.Time {
	info, err := os.Stat(filepath.Join(workDir, hostKillMarker))
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// siblingKilled reports whether the host killed another VM after vm booted.
func siblingKilled(vm *instance) bool {
	if vm.hostKilled {
		return false
	}
	info, err := os.Stat(filepath.Join(filepath.Dir(vm.dir), hostKillMarker))
	return err == nil && !info.ModTime().Before(vm.booted)
}

// hostAvailable is the memory the host had left when its kill was seen: this
// VM's own, or the marker's for a sibling's.
func hostAvailable(vm *instance) uint64 {
	if vm.hostKilled {
		return vm.availableAtKill
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(vm.dir), hostKillMarker))
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	return n
}

// memAvailable is MemAvailable from /proc/meminfo in bytes, or 0 where there
// is none.
func memAvailable() uint64 {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	return parseMemAvailable(string(raw))
}

func parseMemAvailable(meminfo string) uint64 {
	for _, line := range strings.Split(meminfo, "\n") {
		rest, ok := strings.CutPrefix(line, "MemAvailable:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0
		}
		return n << 10
	}
	return 0
}
