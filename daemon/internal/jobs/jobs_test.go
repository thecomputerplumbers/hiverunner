package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/protocol"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fake is a runner the test drives by hand.
type fake struct {
	started chan Job
	finish  chan int
	stopped chan struct{}
}

func newFake() *fake {
	return &fake{started: make(chan Job, 1), finish: make(chan int), stopped: make(chan struct{}, 1)}
}

func (f *fake) Run(ctx context.Context, job Job) (int, error) {
	f.started <- job
	select {
	case code := <-f.finish:
		return code, nil
	case <-ctx.Done():
		f.stopped <- struct{}{}
		return -1, ctx.Err()
	}
}

// The tests are an arm64 machine wherever they run, as every offer here
// that names no architecture is an arm64 job.
func init() { hostArch = func() string { return protocol.ArchARM64 } }

func offer(id string) *protocol.JobAssign {
	return &protocol.JobAssign{AssignmentID: id, Label: "hive", VCPUs: 2, TimeoutMinutes: 60}
}

func start(id string) *protocol.JobStart {
	return &protocol.JobStart{AssignmentID: id, JITConfig: "jit", RunnerName: "hw-m-" + id}
}

func within(t *testing.T, what string) <-chan time.Time {
	t.Helper()
	return time.After(3 * time.Second)
}

func TestAJobRunsAndItsResultIsDelivered(t *testing.T) {
	runner := newFake()
	m := New(runner, quiet)
	if reason := m.Offer("acme", offer("a-1")); reason != "" {
		t.Fatalf("offer declined: %s", reason)
	}
	if !m.Busy() || len(m.Holding("acme")) != 1 || m.Holding("acme")[0] != "a-1" || len(m.Holding("globex")) != 0 {
		t.Fatal("the slot is not held for acme")
	}
	if err := m.Start(context.Background(), "acme", start("a-1")); err != nil {
		t.Fatal(err)
	}
	job := <-runner.started
	if job.JITConfig != "jit" || job.VCPUs != 2 || job.Timeout != time.Hour || job.RunnerName != "hw-m-a-1" {
		t.Fatalf("runner got %+v", job)
	}
	runner.finish <- 3
	select {
	case exit := <-m.Exits("acme"):
		if exit.AssignmentID != "a-1" || exit.ExitCode != 3 {
			t.Fatalf("exit = %+v", exit)
		}
	case <-within(t, "exit"):
		t.Fatal("no result was delivered")
	}
	if m.Busy() {
		t.Fatal("the slot was not freed")
	}
}

func TestOneSlotAcrossPools(t *testing.T) {
	m := New(newFake(), quiet)
	if reason := m.Offer("acme", offer("a-1")); reason != "" {
		t.Fatal(reason)
	}
	if reason := m.Offer("globex", offer("g-1")); reason != ReasonBusy {
		t.Fatalf("second pool's offer: %q", reason)
	}
	// The same offer repeated is still the same job.
	if reason := m.Offer("acme", offer("a-1")); reason != "" {
		t.Fatalf("repeated offer: %q", reason)
	}
	// Another pool can neither start nor release it.
	if err := m.Start(context.Background(), "globex", start("a-1")); !errors.Is(err, ErrNotHeld) {
		t.Fatalf("foreign start: %v", err)
	}
	m.Release("globex", "a-1")
	if !m.Busy() {
		t.Fatal("a foreign release freed the slot")
	}
	m.Release("acme", "a-1")
	if m.Busy() {
		t.Fatal("release did not free the slot")
	}
	if reason := m.Offer("globex", offer("g-1")); reason != "" {
		t.Fatal(reason)
	}
}

func TestAnOfferThatIsNeverStartedExpires(t *testing.T) {
	m := New(newFake(), quiet)
	m.startTimeout = 30 * time.Millisecond
	m.Offer("acme", offer("a-1"))
	deadline := time.Now().Add(3 * time.Second)
	for m.Busy() {
		if time.Now().After(deadline) {
			t.Fatal("the machine stayed reserved")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := m.Start(context.Background(), "acme", start("a-1")); !errors.Is(err, ErrNotHeld) {
		t.Fatalf("late start: %v", err)
	}
}

// slow is a runner whose teardown takes a while, as destroying a VM does.
type slow struct {
	started  chan struct{}
	teardown chan struct{} // closed by the test to let teardown finish
	tornDown chan struct{}
}

func newSlow() *slow {
	return &slow{started: make(chan struct{}), teardown: make(chan struct{}), tornDown: make(chan struct{})}
}

func (r *slow) Run(ctx context.Context, _ Job) (int, error) {
	close(r.started)
	<-ctx.Done()
	<-r.teardown
	close(r.tornDown)
	return -1, ctx.Err()
}

// The order that matters: a released job's VM is destroyed before the
// machine takes anything in its place, and only then is the pool told.
func TestAReleasedJobHoldsTheSlotUntilItsVMIsGone(t *testing.T) {
	runner := newSlow()
	m := New(runner, quiet)
	m.Offer("acme", offer("a-1"))
	if err := m.Start(context.Background(), "acme", start("a-1")); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	m.Release("acme", "a-1")

	// Teardown is under way. The machine is not free yet, for anyone.
	if !m.Busy() {
		t.Fatal("the slot was freed while the VM was still being destroyed")
	}
	if reason := m.Offer("globex", offer("g-1")); reason != ReasonBusy {
		t.Fatalf("another pool's offer during teardown: %q, want it declined as busy", reason)
	}
	if held := m.Holding("acme"); len(held) != 1 || held[0] != "a-1" {
		t.Fatal("a reconnect during teardown would not name the job still on the machine")
	}
	// Releasing twice changes nothing.
	m.Release("acme", "a-1")

	close(runner.teardown)
	<-runner.tornDown
	deadline := time.Now().Add(3 * time.Second)
	for m.Busy() {
		if time.Now().After(deadline) {
			t.Fatal("the slot was never freed")
		}
		time.Sleep(time.Millisecond)
	}
	if reason := m.Offer("globex", offer("g-1")); reason != "" {
		t.Fatalf("offer after teardown: %q", reason)
	}
	// The pool is told the job is gone, so it can count the cores as free.
	select {
	case exit := <-m.Exits("acme"):
		if exit.AssignmentID != "a-1" {
			t.Fatalf("the wrong job was reported gone: %+v", exit)
		}
	case <-within(t, "the released job's exit"):
		t.Fatal("the pool was never told the released job's VM is gone")
	}
}

// Shutting down: cancelling stops the job, and Wait does not return until
// its VM has been destroyed.
func TestWaitReturnsOnlyAfterTeardown(t *testing.T) {
	runner := newSlow()
	m := New(runner, quiet)
	ctx, cancel := context.WithCancel(context.Background())
	m.Offer("acme", offer("a-1"))
	if err := m.Start(ctx, "acme", start("a-1")); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	cancel()

	waited := make(chan struct{})
	go func() { m.Wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("Wait returned while the VM was still being destroyed")
	case <-time.After(100 * time.Millisecond):
	}
	close(runner.teardown)
	select {
	case <-waited:
	case <-within(t, "wait"):
		t.Fatal("Wait never returned")
	}
	select {
	case <-runner.tornDown:
	default:
		t.Fatal("Wait returned before teardown finished")
	}
}

// An offer that was accepted but never started holds no VM: releasing it
// frees the machine at once.
func TestReleasingAnAcceptedOfferFreesTheSlotAtOnce(t *testing.T) {
	m := New(newFake(), quiet)
	m.Offer("acme", offer("a-1"))
	m.Release("acme", "a-1")
	if m.Busy() {
		t.Fatal("the slot was not freed")
	}
	m.Wait() // Nothing is running; this must not block.
}

func TestTheJobTimeoutDestroysTheRunner(t *testing.T) {
	runner := newFake()
	m := New(runner, quiet)
	short := offer("a-1")
	short.TimeoutMinutes = 0
	m.Offer("acme", short)
	m.mu.Lock()
	m.held[key("acme", "a-1")].timeout = 30 * time.Millisecond
	m.mu.Unlock()
	if err := m.Start(context.Background(), "acme", start("a-1")); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	select {
	case exit := <-m.Exits("acme"):
		if exit.ExitCode == 0 {
			t.Fatal("a timed-out job reported success")
		}
	case <-within(t, "timeout"):
		t.Fatal("the job outlived its timeout")
	}
}

func TestNoRunnerDeclinesEverything(t *testing.T) {
	m := New(nil, quiet)
	if reason := m.Offer("acme", offer("a-1")); reason != ReasonNoRunner {
		t.Fatalf("reason = %q", reason)
	}
	if m.Busy() {
		t.Fatal("a machine with no runner reserved its slot")
	}
}

// broken is a runner that has learned it cannot run jobs.
type broken struct{ *fake }

func (broken) Unusable() string { return "the guest image is too old" }

func TestAnUnusableRunnerDeclinesWithItsReason(t *testing.T) {
	m := New(broken{newFake()}, quiet)
	if reason := m.Offer("acme", offer("a-1")); reason != "the guest image is too old" {
		t.Fatalf("reason = %q", reason)
	}
	if m.Busy() {
		t.Fatal("an unusable runner reserved the slot")
	}
}

func TestAnOfferPastTheOwnersLimitIsDeclined(t *testing.T) {
	manager := New(newFake(), quiet)
	manager.Limit(func() Limits { return Limits{MaxVCPUs: 4} })
	if reason := manager.Offer("p", &protocol.JobAssign{AssignmentID: "big", VCPUs: 8}); reason != ReasonTooLarge {
		t.Fatalf("an 8 vCPU offer under a limit of 4: %q", reason)
	}
	if manager.Busy() {
		t.Fatal("a declined offer took the slot")
	}
	if reason := manager.Offer("p", &protocol.JobAssign{AssignmentID: "fits", VCPUs: 4}); reason != "" {
		t.Fatalf("a 4 vCPU offer under a limit of 4 was declined: %q", reason)
	}
}

func TestAJobForAnotherArchitectureIsDeclined(t *testing.T) {
	manager := New(newFake(), quiet)
	x64 := offer("x-1")
	x64.Label, x64.Arch = "hive-x64", protocol.ArchX64
	if reason := manager.Offer("p", x64); reason != ReasonWrongArch {
		t.Fatalf("an x64 offer to an arm64 machine: %q", reason)
	}
	if manager.Busy() {
		t.Fatal("a declined offer took the slot")
	}
	arm := offer("a-1")
	arm.Arch = protocol.ArchARM64
	if reason := manager.Offer("p", arm); reason != "" {
		t.Fatalf("an arm64 offer to an arm64 machine was declined: %q", reason)
	}

	// An x64 machine takes x64 jobs, and not the ones that name nothing:
	// those come from a pool that only knows arm64.
	hostArch = func() string { return protocol.ArchX64 }
	defer func() { hostArch = func() string { return protocol.ArchARM64 } }()
	box := New(newFake(), quiet)
	if reason := box.Offer("p", offer("a-2")); reason != ReasonWrongArch {
		t.Fatalf("an unmarked offer to an x64 machine: %q", reason)
	}
	x64.AssignmentID = "x-2"
	if reason := box.Offer("p", x64); reason != "" {
		t.Fatalf("an x64 offer to an x64 machine was declined: %q", reason)
	}
}

func limited(limits Limits) func() Limits { return func() Limits { return limits } }

func sized(id string, vcpus int) *protocol.JobAssign {
	job := offer(id)
	job.VCPUs = vcpus
	return job
}

// An owner who allows two jobs gets two, from any mix of pools, and not a
// third.
func TestSeveralJobsRunAtOnceUpToTheOwnersLimit(t *testing.T) {
	runner := newFake()
	runner.started = make(chan Job, 2)
	m := New(runner, quiet)
	m.Limit(limited(Limits{MaxJobs: 2, Cores: 18}))

	if reason := m.Offer("acme", offer("a-1")); reason != "" {
		t.Fatalf("first offer: %q", reason)
	}
	if m.Full() {
		t.Fatal("one job of two filled the machine")
	}
	if reason := m.Offer("globex", offer("g-1")); reason != "" {
		t.Fatalf("second offer, from another pool: %q", reason)
	}
	if !m.Full() {
		t.Fatal("two jobs of two did not fill the machine")
	}
	if reason := m.Offer("acme", offer("a-2")); reason != ReasonBusy {
		t.Fatalf("third offer: %q, want it declined as busy", reason)
	}
	for _, started := range []struct{ pool, id string }{{"acme", "a-1"}, {"globex", "g-1"}} {
		if err := m.Start(context.Background(), started.pool, start(started.id)); err != nil {
			t.Fatal(err)
		}
		<-runner.started
	}

	// One finishing frees one slot, and the other job is untouched.
	runner.finish <- 0
	var done protocol.JobExited
	select {
	case done = <-m.Exits("acme"):
	case done = <-m.Exits("globex"):
	case <-within(t, "the first exit"):
		t.Fatal("no job reported its exit")
	}
	if m.Full() || !m.Busy() {
		t.Fatal("one job ending should leave one running and one slot free")
	}
	if len(m.Holding("acme"))+len(m.Holding("globex")) != 1 {
		t.Fatalf("after %s ended the machine holds the wrong jobs", done.AssignmentID)
	}
	runner.finish <- 0
	m.Wait()
}

// Two pools cannot see each other's jobs, so the machine itself refuses a
// job that would leave its owner without cores.
func TestStackedJobsLeaveTheOwnerTheirCores(t *testing.T) {
	m := New(newFake(), quiet)
	m.Limit(limited(Limits{MaxJobs: 4, Cores: 12}))
	if reason := m.Offer("acme", sized("a-1", 8)); reason != "" {
		t.Fatalf("8 of 12 cores: %q", reason)
	}
	if reason := m.Offer("globex", sized("g-1", 4)); reason != ReasonNoCores {
		t.Fatalf("4 more of 12 cores, with 8 in use: %q, want no cores", reason)
	}
	if reason := m.Offer("globex", sized("g-2", 2)); reason != "" {
		t.Fatalf("2 more of 12 cores, with 8 in use: %q", reason)
	}
}

// Assignment ids are each pool's own.
func TestTwoPoolsMayUseTheSameAssignmentID(t *testing.T) {
	m := New(newFake(), quiet)
	m.Limit(limited(Limits{MaxJobs: 2}))
	m.Offer("acme", offer("same"))
	if reason := m.Offer("globex", offer("same")); reason != "" {
		t.Fatalf("another pool's job with the same id: %q", reason)
	}
	m.Release("acme", "same")
	if held := m.Holding("globex"); len(held) != 1 {
		t.Fatal("releasing one pool's job dropped the other's")
	}
}

// A hive-macos job goes to the native runner, and only on a Mac whose owner
// opted in; a Linux job never does.
func TestAMacOSJobGoesToTheNativeRunner(t *testing.T) {
	vm, mac := newFake(), newFake()
	m := New(vm, quiet)
	macos := &protocol.JobAssign{AssignmentID: "a-1", Label: "hive-macos", Arch: protocol.KindMacOS, VCPUs: 4, TimeoutMinutes: 60}

	// Not opted in: declined with a reason the pool can show.
	if reason := m.Offer("p", macos); reason != ReasonNoMacOS {
		t.Fatalf("Offer() = %q, want %q", reason, ReasonNoMacOS)
	}
	// Opted in, but nothing to run it with: still declined.
	m.Limit(func() Limits { return Limits{MacOS: true} })
	if reason := m.Offer("p", macos); reason != ReasonNoMacOS {
		t.Fatalf("Offer() without a native runner = %q, want %q", reason, ReasonNoMacOS)
	}
	m.Native(mac)
	if reason := m.Offer("p", macos); reason != "" {
		t.Fatalf("Offer() = %q, want accepted", reason)
	}
	if err := m.Start(context.Background(), "p", start("a-1")); err != nil {
		t.Fatal(err)
	}
	select {
	case job := <-mac.started:
		if job.Kind != protocol.KindMacOS || job.VCPUs != 4 {
			t.Fatalf("native runner got %+v", job)
		}
	case <-vm.started:
		t.Fatal("the VM runner got a macOS job")
	case <-within(t, "start"):
		t.Fatal("the job never started")
	}
	mac.finish <- 0
	select {
	case exited := <-m.Exits("p"):
		if exited.ExitCode != 0 {
			t.Fatalf("exit = %d", exited.ExitCode)
		}
	case <-within(t, "exit"):
		t.Fatal("no exit")
	}

	// A plain job still goes to the VM.
	if reason := m.Offer("p", offer("a-2")); reason != "" {
		t.Fatalf("Offer(linux) = %q", reason)
	}
	if err := m.Start(context.Background(), "p", start("a-2")); err != nil {
		t.Fatal(err)
	}
	select {
	case job := <-vm.started:
		if job.Kind != protocol.ArchARM64 {
			t.Fatalf("VM runner got kind %q", job.Kind)
		}
	case <-mac.started:
		t.Fatal("the native runner got a Linux job")
	case <-within(t, "start"):
		t.Fatal("the job never started")
	}
	vm.finish <- 0
	<-m.Exits("p")
}
