// Package jobs owns the machine's job slots and the lives of the jobs in
// them.
//
// A machine runs as many jobs at once as its owner allows, one by default,
// and that is one budget across every organization it serves. Each pool's
// tunnel brings offers here; an offer that fits what is left gets a slot and
// one that does not is declined. A job outlives any one tunnel connection: if
// the tunnel drops mid-job the runner carries on, and its result is delivered
// when the tunnel returns.
package jobs

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/protocol"
)

// Job is one GitHub Actions job to run in a throwaway VM, or, for a
// hive-macos job, on this Mac itself as a separate user.
type Job struct {
	AssignmentID string
	// Kind is what the job's label asked for: an architecture, which means a
	// Linux VM of it, or protocol.KindMacOS.
	Kind string
	// Pool is the pool that placed the job: whose work this is.
	Pool string
	// JITConfig is a credential. It goes to the guest and nowhere else.
	JITConfig  string
	RunnerName string
	VCPUs      int
	// Timeout bounds the whole job; the VM is destroyed when it passes.
	Timeout time.Duration
	// Label is the runs-on label, for kill-reason messages.
	Label string
}

// ErrTimeout is the cause when a job exceeds its limit.
var ErrTimeout = errors.New("job timed out")

// ErrReleased is the cause when the coordinator backs out of a running job.
var ErrReleased = errors.New("job released")

// Runner runs a job to completion in isolation and returns the runner's exit
// code. It must destroy everything it created before returning, including
// when ctx is cancelled. A VM that will not stop is the one exception: its
// runner may return without it, if it goes on counting it against what it
// can take (see Limiter and Unusable) until it has gone.
type Runner interface {
	Run(ctx context.Context, job Job) (Result, error)
}

// Chooser is implemented by a runner with more than one way of running a
// job. The way is chosen when the job is offered, and that way runs it: a
// setting changed before the job starts does not change how it runs.
type Chooser interface {
	Choose() Runner
}

// Limiter is implemented by a runner with limits of its own, which it sets
// on an offer's limits once it has been chosen to run the job: so the limits
// are always those of the way the job will run.
type Limiter interface {
	Limit(*Limits)
}

// Unusable is implemented by a runner that can find out it will not be able
// to run jobs, and why: a guest image GitHub no longer accepts, for one. A
// non-empty reason declines every offer with it.
type Unusable interface {
	Unusable() string
}

// Reasons an offer is declined, in the machine's own words.
const (
	ReasonBusy      = "this machine is running as many jobs as its owner allows"
	ReasonNoCores   = "this machine does not have the cores free for this job"
	ReasonNoRunner  = "this machine cannot run jobs: no runner image is installed"
	ReasonLowMemory = "this machine does not have the memory free for this job"
	ReasonTooLarge  = "this job is larger than this machine's owner allows"
	// ReasonWrongArch names no architecture: the offer's is the pool's word.
	ReasonWrongArch = "this job is for a machine of another architecture"
	// ReasonCoolingOff is declined while the machine waits out a host kill.
	ReasonCoolingOff = "this machine is cooling off after the host killed a job for want of memory"
	ReasonNoMacOS    = "this Mac does not take macOS jobs; its owner has not run hived macos setup"
	ReasonMacOSVMs   = "this Mac is running as many macOS VMs as Apple's licence allows"
)

// hostArch is what this machine is. A variable so tests can be another.
var hostArch = protocol.HostArch

// OwnerCores is what every job leaves for the machine's owner. The pool
// applies the same rule when it chooses a machine.
const OwnerCores = 2

// Limits is what the machine may lend at once. The zero value is one job of
// any size on a machine of unknown size, which is what the daemon did before
// its owner could say otherwise.
type Limits struct {
	// MaxJobs is how many jobs may run at once. Below one means one.
	MaxJobs int
	// MaxVCPUs is the largest single job. Zero means any.
	MaxVCPUs int
	// Cores is the machine's core count. Zero means unknown, and no job is
	// declined for want of cores.
	Cores int
	// MacOS is whether hive-macos jobs are taken: the owner opted in and the
	// Mac is ready for them.
	MacOS bool
	// MostMacOS is how many hive-macos jobs may run at once, within
	// MaxJobs. Zero sets no limit of its own. A Mac that runs each one in a
	// macOS VM is held to two: Apple's licence allows a Mac two macOS VMs.
	MostMacOS int
	// MacOSMemory is the memory a hive-macos job takes from the host: its
	// VM's, or zero for one run on the Mac itself, which is not checked.
	MacOSMemory uint64
	// Available is the memory the host can give a new VM right now, in
	// bytes. Zero means unknown: then Planned is the budget, if there is one.
	Available uint64
	// Planned is the memory the host's jobs may plan on, in bytes, where
	// what is free cannot be read, as on a Mac, which reclaims what it
	// caches as it is asked: a job is declined if it, every job held, the
	// margin and Reserve would not fit in it. Zero means no budget, and no
	// job is declined for want of memory.
	Planned uint64
	// Reserve is memory the owner keeps for everything else on the host: a
	// job is declined if accepting it would leave Available below it.
	Reserve uint64
	// GuestMemory is the memory a job of this many vCPUs is given. Nil
	// means memory is not checked.
	GuestMemory func(vcpus int) uint64
	// Hold declines every offer, as the reason it is giving: a machine
	// waiting out a host kill that already runs one job at most.
	Hold bool
}

// memory is what a job of this kind and size takes from the host.
func (l Limits) memory(kind string, vcpus int) uint64 {
	if kind == protocol.KindMacOS {
		return l.MacOSMemory
	}
	return l.GuestMemory(vcpus)
}

// MemoryMargin is what a job leaves free on the host beyond its guest's
// memory: the hypervisor, the daemon and the owner's own programs.
const MemoryMargin = 1 << 30

type phase int

const (
	accepted phase = iota
	running
)

type slot struct {
	pool         string
	assignmentID string
	kind         string
	vcpus        int
	timeout      time.Duration
	label        string
	// runner is what runs it, chosen when it was offered.
	runner Runner
	// memory is what its guest takes from the host, as the limits had it
	// when it was offered: a macOS VM's too, whatever the next offer is.
	memory uint64
	phase  phase
	// cancel stops the start timer (accepted) or the runner (running).
	cancel func()
	// released means the coordinator backed out of a running job: its VM is
	// being destroyed.
	released bool
}

// Manager is the slots. It is safe for use by every pool's tunnel at once.
type Manager struct {
	runner Runner
	// native runs hive-macos jobs on the Mac itself. Nil declines them.
	native       Runner
	startTimeout time.Duration
	log          *slog.Logger
	// limits is asked on every offer, so it may change while the daemon
	// runs. Nil is the zero Limits.
	limits func() Limits
	// onHostKilled is told when a job's VM was killed by the host. Nil is
	// nobody.
	onHostKilled func()

	mu    sync.Mutex
	held  map[string]*slot
	exits map[string]chan protocol.JobExited
	// running counts jobs whose runner has not returned, so shutdown can
	// wait for their VMs to be destroyed.
	running sync.WaitGroup
}

// New returns a manager. A nil runner declines every offer, which is what a
// machine with no runner image installed should do.
func New(runner Runner, log *slog.Logger) *Manager {
	return &Manager{
		runner:       runner,
		startTimeout: protocol.StartTimeout,
		log:          log,
		held:         map[string]*slot{},
		exits:        map[string]chan protocol.JobExited{},
	}
}

// Limit sets what the machine may lend at once. Call it before the first
// offer.
func (m *Manager) Limit(limits func() Limits) {
	m.limits = limits
}

// Native sets the runner for hive-macos jobs, which run on this Mac itself.
// Call it before the first offer. Nil declines every such job.
func (m *Manager) Native(runner Runner) {
	m.native = runner
}

// OnHostKilled sets what happens after a job ends because the host itself
// killed its VM. Call it before the first offer.
func (m *Manager) OnHostKilled(fn func()) {
	m.onHostKilled = fn
}

func (m *Manager) limit() Limits {
	var limits Limits
	if m.limits != nil {
		limits = m.limits()
	}
	if limits.MaxJobs < 1 {
		limits.MaxJobs = 1
	}
	return limits
}

// key names a slot. Assignment ids are a pool's own, so two pools may use
// the same one.
func key(pool, assignmentID string) string { return pool + "\x00" + assignmentID }

// Busy reports whether any job holds a slot.
func (m *Manager) Busy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.held) > 0
}

// Full reports whether every slot the owner allows is taken.
func (m *Manager) Full() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.held) >= m.limit().MaxJobs
}

// Holding is the assignments this pool has in slots, oldest name first. A
// reconnecting tunnel reports them in hello.
func (m *Manager) Holding(pool string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for _, held := range m.held {
		if held.pool == pool {
			ids = append(ids, held.assignmentID)
		}
	}
	sort.Strings(ids)
	return ids
}

// Offer answers a pool's job offer: "" to accept, or the reason it is
// declined. Accepting reserves a slot until Start, Release, or the start
// timeout, whichever comes first.
func (m *Manager) Offer(pool string, offer *protocol.JobAssign) (reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// The pool is told the limits and should not offer past them. These are
	// for the offer made before it heard, and for another pool's jobs, which
	// this pool cannot see.
	limits := m.limit()
	// The pool should never offer this: it is told what this machine is. A
	// job on the wrong kind of guest would not fail cleanly, so it is not
	// started at all. An offer that names no architecture is arm64.
	kind := cmp.Or(offer.Arch, protocol.ArchARM64)
	runner := m.runner
	if kind == protocol.KindMacOS {
		if !limits.MacOS || m.native == nil {
			return ReasonNoMacOS
		}
		runner = m.native
	} else if kind != hostArch() {
		return ReasonWrongArch
	}
	if limits.Hold {
		return ReasonCoolingOff
	}
	if chooser, ok := runner.(Chooser); ok {
		runner = chooser.Choose()
	}
	if runner == nil {
		return ReasonNoRunner
	}
	if checked, ok := runner.(Unusable); ok {
		if reason := checked.Unusable(); reason != "" {
			return reason
		}
	}
	if limiter, ok := runner.(Limiter); ok {
		limiter.Limit(&limits)
	}
	id := key(pool, offer.AssignmentID)
	// The coordinator repeating itself is not a second job.
	if _, ok := m.held[id]; ok {
		return ""
	}
	if limits.MaxVCPUs != 0 && offer.VCPUs > limits.MaxVCPUs {
		return ReasonTooLarge
	}
	if len(m.held) >= limits.MaxJobs {
		return ReasonBusy
	}
	if kind == protocol.KindMacOS && limits.MostMacOS > 0 {
		macOS := 0
		for _, held := range m.held {
			if held.kind == protocol.KindMacOS {
				macOS++
			}
		}
		if macOS >= limits.MostMacOS {
			return ReasonMacOSVMs
		}
	}
	used := 0
	for _, held := range m.held {
		used += held.vcpus
	}
	// Only stacked jobs are checked: the pool alone decides whether a lone
	// job fits the machine, as it always has.
	if limits.Cores > 0 && used > 0 && used+offer.VCPUs+OwnerCores > limits.Cores {
		return ReasonNoCores
	}
	memory := uint64(0)
	if limits.GuestMemory != nil {
		memory = limits.memory(kind, offer.VCPUs)
	}
	if memory > 0 {
		// A guest's memory is taken as it touches it, so what the host
		// reports free does not yet count jobs accepted but not started.
		// A budget counts every job held.
		budget, held := limits.Available, uint64(0)
		if budget == 0 {
			budget = limits.Planned
		}
		for _, other := range m.held {
			if other.phase == accepted || limits.Available == 0 {
				held += other.memory
			}
		}
		need := memory + held + MemoryMargin + limits.Reserve
		if budget > 0 && budget < need {
			// needed_mb is the sum of the rest, not the job's size: name
			// each part so the total is not read as one guest.
			m.log.Warn("declining a job: not enough free memory", "assignment", offer.AssignmentID,
				"available_mb", budget>>20, "needed_mb", need>>20,
				"guest_mb", memory>>20, "held_mb", held>>20, "margin_mb", uint64(MemoryMargin)>>20, "reserve_mb", limits.Reserve>>20,
				"planned", limits.Available == 0)
			return ReasonLowMemory
		}
	}
	held := &slot{
		pool:         pool,
		assignmentID: offer.AssignmentID,
		kind:         kind,
		vcpus:        offer.VCPUs,
		timeout:      time.Duration(offer.TimeoutMinutes) * time.Minute,
		label:        offer.Label,
		runner:       runner,
		memory:       memory,
		phase:        accepted,
	}
	// If the coordinator never follows up, the machine must not stay
	// reserved for a job that is not coming.
	timer := time.AfterFunc(m.startTimeout, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.held[id] == held && held.phase == accepted {
			m.log.Warn("an accepted job was never started; freeing its slot", "assignment", held.assignmentID)
			delete(m.held, id)
		}
	})
	held.cancel = func() { timer.Stop() }
	m.held[id] = held
	return ""
}

// ErrNotHeld means a start arrived for a job this machine is not holding:
// the offer expired, was released, or was never made.
var ErrNotHeld = errors.New("this machine is not holding that job")

// Start runs an accepted job. It returns at once; the result arrives on
// Exits for the pool.
func (m *Manager) Start(ctx context.Context, pool string, start *protocol.JobStart) error {
	id := key(pool, start.AssignmentID)
	m.mu.Lock()
	held := m.held[id]
	if held == nil || held.phase != accepted {
		m.mu.Unlock()
		return ErrNotHeld
	}
	held.cancel()
	runCtx, cancel := context.WithCancelCause(ctx)
	if held.timeout > 0 {
		timed, stop := context.WithTimeoutCause(runCtx, held.timeout, ErrTimeout)
		runCtx = timed
		held.phase = running
		held.cancel = func() {
			stop()
			cancel(ErrReleased)
		}
	} else {
		held.phase = running
		held.cancel = func() { cancel(ErrReleased) }
	}
	exits := m.exitsLocked(pool)
	m.mu.Unlock()

	job := Job{
		AssignmentID: start.AssignmentID,
		Kind:         held.kind,
		Pool:         pool,
		JITConfig:    start.JITConfig,
		RunnerName:   start.RunnerName,
		VCPUs:        held.vcpus,
		Timeout:      held.timeout,
		Label:        held.label,
	}
	runner := held.runner
	m.running.Add(1)
	go func() {
		defer m.running.Done()
		defer cancel(nil)
		m.log.Info("job started", "assignment", job.AssignmentID, "runner", job.RunnerName, "kind", job.Kind, "vcpus", job.VCPUs, "label", job.Label)
		// Run returns once the VM and its files are gone, or once a VM that
		// will not stop is counted by its runner instead of this slot.
		result, err := runner.Run(runCtx, job)
		m.mu.Lock()
		released := held.released
		m.mu.Unlock()
		exitCode := result.ExitCode
		if err != nil {
			// The pool releases a job once GitHub says it is over, which
			// stops a VM that was about to stop anyway. That is not a
			// failure and should not read as one in the owner's log.
			if !released {
				m.log.Error("job failed to run", "assignment", job.AssignmentID, "error", err)
			}
			if exitCode == 0 {
				exitCode = -1
			}
		}
		end := End{Reason: result.Reason, Detail: result.Detail}
		if end.Reason == "" {
			// The runner did not see a guest or host kill. Name the
			// stop we caused, or a VM that vanished without a result.
			end = Classify(Evidence{
				Label:    job.Label,
				HasExit:  exitCode != -1,
				ExitCode: exitCode,
				Timeout:  errors.Is(context.Cause(runCtx), ErrTimeout),
				Released: released || errors.Is(context.Cause(runCtx), ErrReleased),
				Shutdown: err != nil && !released && errors.Is(err, context.Canceled) && !errors.Is(context.Cause(runCtx), ErrTimeout),
			})
		}
		// The slot is freed here and nowhere else for a running job: not
		// before its VM is destroyed, even when the job was released.
		m.mu.Lock()
		if m.held[id] == held {
			delete(m.held, id)
		}
		m.mu.Unlock()
		if end.Reason != "" && end.Reason != ReasonReleased {
			m.log.Error(end.Detail, "assignment", job.AssignmentID, "reason", end.Reason, "exit", exitCode)
		}
		if end.Reason == ReasonHostKilled && m.onHostKilled != nil {
			m.onHostKilled()
		}
		ended := []any{"assignment", job.AssignmentID, "exit", exitCode, "reason", end.Reason, "released", released}
		if result.PeakGuestBytes > 0 {
			ended = append(ended, "peak_guest_mb", result.PeakGuestBytes>>20)
		}
		m.log.Info("job ended", ended...)
		// Said for a released job too: its result is of no interest to the
		// coordinator, but that its cores are free again is.
		exited := protocol.NewJobExited(job.AssignmentID, exitCode)
		exited.Reason = end.Reason
		exited.Detail = end.Detail
		exits <- exited
	}()
	return nil
}

// Release backs out of a job this pool placed. An accepted offer is dropped
// at once. A running job is stopped, and its slot stays taken until its VM
// is destroyed: cores still tearing one job down are not free for another.
func (m *Manager) Release(pool, assignmentID string) {
	id := key(pool, assignmentID)
	m.mu.Lock()
	defer m.mu.Unlock()
	held := m.held[id]
	if held == nil {
		return
	}
	m.log.Info("job released by the coordinator", "assignment", assignmentID)
	held.cancel()
	if held.phase == running {
		held.released = true
		return
	}
	delete(m.held, id)
}

// Wait returns when no job's runner is still running, which means every VM
// a job started has been destroyed. Cancel the context given to Start first;
// the daemon calls this before it exits so nothing is left on disk.
func (m *Manager) Wait() {
	m.running.Wait()
}

// Exits carries finished jobs' results for one pool. The tunnel sends each
// one; a result produced while the tunnel is down waits here for it.
func (m *Manager) Exits(pool string) chan protocol.JobExited {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.exitsLocked(pool)
}

func (m *Manager) exitsLocked(pool string) chan protocol.JobExited {
	if m.exits[pool] == nil {
		// Room for every slot's result several times over, so a job ending
		// while the tunnel is down never waits on it.
		m.exits[pool] = make(chan protocol.JobExited, 64)
	}
	return m.exits[pool]
}
