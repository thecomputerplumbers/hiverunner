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
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/protocol"
)

// Job is one GitHub Actions job to run in a throwaway VM.
type Job struct {
	AssignmentID string
	// JITConfig is a credential. It goes to the guest and nowhere else.
	JITConfig  string
	RunnerName string
	VCPUs      int
	// Timeout bounds the whole job; the VM is destroyed when it passes.
	Timeout time.Duration
}

// Runner runs a job to completion in isolation and returns the runner's exit
// code. It must destroy everything it created before returning, including
// when ctx is cancelled.
type Runner interface {
	Run(ctx context.Context, job Job) (exitCode int, err error)
}

// Unusable is implemented by a runner that can find out it will not be able
// to run jobs, and why: a guest image GitHub no longer accepts, for one. A
// non-empty reason declines every offer with it.
type Unusable interface {
	Unusable() string
}

// Reasons an offer is declined, in the machine's own words.
const (
	ReasonBusy     = "this machine is running as many jobs as its owner allows"
	ReasonNoCores  = "this machine does not have the cores free for this job"
	ReasonNoRunner = "this machine cannot run jobs: no runner image is installed"
	ReasonTooLarge = "this job is larger than this machine's owner allows"
)

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
}

type phase int

const (
	accepted phase = iota
	running
)

type slot struct {
	pool         string
	assignmentID string
	vcpus        int
	timeout      time.Duration
	phase        phase
	// cancel stops the start timer (accepted) or the runner (running).
	cancel func()
	// released means the coordinator backed out of a running job: its VM is
	// being destroyed.
	released bool
}

// Manager is the slots. It is safe for use by every pool's tunnel at once.
type Manager struct {
	runner       Runner
	startTimeout time.Duration
	log          *slog.Logger
	// limits is asked on every offer, so it may change while the daemon
	// runs. Nil is the zero Limits.
	limits func() Limits

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
	if m.runner == nil {
		return ReasonNoRunner
	}
	if checked, ok := m.runner.(Unusable); ok {
		if reason := checked.Unusable(); reason != "" {
			return reason
		}
	}
	id := key(pool, offer.AssignmentID)
	// The coordinator repeating itself is not a second job.
	if _, ok := m.held[id]; ok {
		return ""
	}
	// The pool is told the limits and should not offer past them. These are
	// for the offer made before it heard, and for another pool's jobs, which
	// this pool cannot see.
	limits := m.limit()
	if limits.MaxVCPUs != 0 && offer.VCPUs > limits.MaxVCPUs {
		return ReasonTooLarge
	}
	if len(m.held) >= limits.MaxJobs {
		return ReasonBusy
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
	held := &slot{
		pool:         pool,
		assignmentID: offer.AssignmentID,
		vcpus:        offer.VCPUs,
		timeout:      time.Duration(offer.TimeoutMinutes) * time.Minute,
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
	runCtx, cancel := context.WithCancel(ctx)
	if held.timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, held.timeout)
	}
	held.phase = running
	held.cancel = cancel
	exits := m.exitsLocked(pool)
	m.mu.Unlock()

	job := Job{
		AssignmentID: start.AssignmentID,
		JITConfig:    start.JITConfig,
		RunnerName:   start.RunnerName,
		VCPUs:        held.vcpus,
		Timeout:      held.timeout,
	}
	m.running.Add(1)
	go func() {
		defer m.running.Done()
		defer cancel()
		m.log.Info("job started", "assignment", job.AssignmentID, "runner", job.RunnerName, "vcpus", job.VCPUs)
		// Run returns only once the VM and its files are gone.
		exitCode, err := m.runner.Run(runCtx, job)
		if err != nil {
			m.log.Error("job failed to run", "assignment", job.AssignmentID, "error", err)
			if exitCode == 0 {
				exitCode = -1
			}
		}
		// The slot is freed here and nowhere else for a running job: not
		// before its VM is destroyed, even when the job was released.
		m.mu.Lock()
		released := held.released
		if m.held[id] == held {
			delete(m.held, id)
		}
		m.mu.Unlock()
		m.log.Info("job ended", "assignment", job.AssignmentID, "exit", exitCode, "released", released)
		// Said for a released job too: its result is of no interest to the
		// coordinator, but that its cores are free again is.
		exits <- protocol.NewJobExited(job.AssignmentID, exitCode)
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
