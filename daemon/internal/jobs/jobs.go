// Package jobs owns the machine's one job slot and the life of the job in it.
//
// A machine serving several organizations still runs one job at a time. Each
// pool's tunnel brings offers here; whichever arrives first gets the slot and
// the others are declined. The job outlives any one tunnel connection: if the
// tunnel drops mid-job the runner carries on, and its result is delivered
// when the tunnel returns.
package jobs

import (
	"context"
	"errors"
	"log/slog"
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
	ReasonBusy     = "this machine is running a job for another pool"
	ReasonNoRunner = "this machine cannot run jobs: no runner image is installed"
)

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
	// being destroyed, and its result is of no interest.
	released bool
}

// Manager is the slot. It is safe for use by every pool's tunnel at once.
type Manager struct {
	runner       Runner
	startTimeout time.Duration
	log          *slog.Logger

	mu      sync.Mutex
	current *slot
	exits   map[string]chan protocol.JobExited
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
		exits:        map[string]chan protocol.JobExited{},
	}
}

// Busy reports whether a job holds the slot.
func (m *Manager) Busy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current != nil
}

// Current is the assignment this pool has in the slot, or "". A reconnecting
// tunnel reports it in hello.
func (m *Manager) Current(pool string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil && m.current.pool == pool {
		return m.current.assignmentID
	}
	return ""
}

// Offer answers a pool's job offer: "" to accept, or the reason it is
// declined. Accepting reserves the slot until Start, Release, or the start
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
	if m.current != nil {
		// The coordinator repeating itself is not a second job.
		if m.current.pool == pool && m.current.assignmentID == offer.AssignmentID {
			return ""
		}
		return ReasonBusy
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
		if m.current == held && held.phase == accepted {
			m.log.Warn("an accepted job was never started; freeing the machine", "assignment", held.assignmentID)
			m.current = nil
		}
	})
	held.cancel = func() { timer.Stop() }
	m.current = held
	return ""
}

// ErrNotHeld means a start arrived for a job this machine is not holding:
// the offer expired, was released, or was never made.
var ErrNotHeld = errors.New("this machine is not holding that job")

// Start runs an accepted job. It returns at once; the result arrives on
// Exits for the pool.
func (m *Manager) Start(ctx context.Context, pool string, start *protocol.JobStart) error {
	m.mu.Lock()
	held := m.current
	if held == nil || held.pool != pool || held.assignmentID != start.AssignmentID || held.phase != accepted {
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
		if m.current == held {
			m.current = nil
		}
		m.mu.Unlock()
		m.log.Info("job ended", "assignment", job.AssignmentID, "exit", exitCode, "released", released)
		// A released job's result is of no interest to the coordinator.
		if !released {
			exits <- protocol.NewJobExited(job.AssignmentID, exitCode)
		}
	}()
	return nil
}

// Release backs out of a job this pool placed. An accepted offer is dropped
// at once. A running job is stopped, and the slot stays taken until its VM is
// destroyed: a machine still tearing one job down is not free for another.
func (m *Manager) Release(pool, assignmentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	held := m.current
	if held == nil || held.pool != pool || held.assignmentID != assignmentID {
		return
	}
	m.log.Info("job released by the coordinator", "assignment", assignmentID)
	held.cancel()
	if held.phase == running {
		held.released = true
		return
	}
	m.current = nil
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
		// One job at a time means at most a few can ever be waiting.
		m.exits[pool] = make(chan protocol.JobExited, 16)
	}
	return m.exits[pool]
}
