package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/protocol"
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

func (f *fake) Run(ctx context.Context, job Job) (Result, error) {
	f.started <- job
	select {
	case code := <-f.finish:
		return Result{ExitCode: code}, nil
	case <-ctx.Done():
		f.stopped <- struct{}{}
		return Result{ExitCode: -1}, ctx.Err()
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
	if job.JITConfig != "jit" || job.VCPUs != 2 || job.Timeout != time.Hour || job.RunnerName != "hw-m-a-1" || job.Label != "hive" {
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

func (r *slow) Run(ctx context.Context, _ Job) (Result, error) {
	close(r.started)
	<-ctx.Done()
	<-r.teardown
	close(r.tornDown)
	return Result{ExitCode: -1}, ctx.Err()
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
		if exit.AssignmentID != "a-1" || exit.Reason != ReasonReleased {
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
	select {
	case exit := <-m.Exits("acme"):
		if exit.Reason != ReasonDaemonShutdown {
			t.Fatalf("a job stopped by shutdown = %+v", exit)
		}
	case <-within(t, "shutdown exit"):
		t.Fatal("the pool was never told the shutdown job ended")
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
		if exit.ExitCode == 0 || exit.Reason != ReasonTimeout {
			t.Fatalf("a timed-out job = %+v", exit)
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

const gib = 1 << 30

// Four GiB a job, whatever its size.
func fourGiB(int) uint64 { return 4 * gib }

// A host without the memory for a guest and the margin declines the job
// instead of letting the kernel pick a VM to kill.
func TestAJobIsDeclinedWhenTheHostLacksTheMemory(t *testing.T) {
	m := New(newFake(), quiet)
	m.Limit(limited(Limits{MaxJobs: 2, Available: 4*gib + MemoryMargin - 1, GuestMemory: fourGiB}))
	if reason := m.Offer("acme", sized("a-1", 2)); reason != ReasonLowMemory {
		t.Fatalf("4 GiB guest with 1 GiB short of the margin: %q, want low memory", reason)
	}
	if m.Busy() {
		t.Fatal("a declined job holds a slot")
	}
	m.Limit(limited(Limits{MaxJobs: 2, Available: 4*gib + MemoryMargin, GuestMemory: fourGiB}))
	if reason := m.Offer("acme", sized("a-1", 2)); reason != "" {
		t.Fatalf("exactly enough memory: %q", reason)
	}
}

// The owner's reserve is memory a job may not cut into, however much the
// host reports free.
func TestAJobIsDeclinedWhenItWouldCutIntoTheReserve(t *testing.T) {
	m := New(newFake(), quiet)
	m.Limit(limited(Limits{MaxJobs: 2, Available: 24*gib + 4*gib + MemoryMargin - 1, Reserve: 24 * gib, GuestMemory: fourGiB}))
	if reason := m.Offer("acme", sized("a-1", 2)); reason != ReasonLowMemory {
		t.Fatalf("a byte into the reserve: %q, want low memory", reason)
	}
	m.Limit(limited(Limits{MaxJobs: 2, Available: 24*gib + 4*gib + MemoryMargin, Reserve: 24 * gib, GuestMemory: fourGiB}))
	if reason := m.Offer("acme", sized("a-1", 2)); reason != "" {
		t.Fatalf("exactly clear of the reserve: %q", reason)
	}
}

// A guest has not touched its memory when it is accepted, so the host's
// figure does not yet include it.
func TestAcceptedJobsCountAgainstFreeMemory(t *testing.T) {
	m := New(newFake(), quiet)
	m.Limit(limited(Limits{MaxJobs: 2, Available: 8*gib + MemoryMargin, GuestMemory: fourGiB}))
	if reason := m.Offer("acme", sized("a-1", 2)); reason != "" {
		t.Fatalf("first job: %q", reason)
	}
	if reason := m.Offer("acme", sized("a-2", 2)); reason != "" {
		t.Fatalf("second job fits beside the first: %q", reason)
	}
	m.Limit(limited(Limits{MaxJobs: 3, Available: 8*gib + MemoryMargin, GuestMemory: fourGiB}))
	if reason := m.Offer("acme", sized("a-3", 2)); reason != ReasonLowMemory {
		t.Fatalf("third job past two accepted ones: %q, want low memory", reason)
	}
}

func TestUnknownMemoryDeclinesNothing(t *testing.T) {
	m := New(newFake(), quiet)
	m.Limit(limited(Limits{MaxJobs: 1, GuestMemory: fourGiB}))
	if reason := m.Offer("acme", sized("a-1", 2)); reason != "" {
		t.Fatalf("host memory unknown: %q", reason)
	}
}

type killer struct{}

func (killer) Run(context.Context, Job) (Result, error) {
	return Result{ExitCode: 137, Reason: ReasonHostKilled}, nil
}

// A job the host killed is told to whoever limits the machine, before the
// next offer can be made.
func TestAHostKilledJobIsReported(t *testing.T) {
	m := New(killer{}, quiet)
	hits := make(chan struct{}, 2)
	m.OnHostKilled(func() { hits <- struct{}{} })
	if reason := m.Offer("acme", offer("a-1")); reason != "" {
		t.Fatalf("offer: %q", reason)
	}
	if err := m.Start(context.Background(), "acme", start("a-1")); err != nil {
		t.Fatal(err)
	}
	if done := <-m.Exits("acme"); done.Reason != ReasonHostKilled {
		t.Fatalf("exit reason %q", done.Reason)
	}
	select {
	case <-hits:
	default:
		t.Fatal("a host kill was not reported before the exit")
	}

	// Any other end is not one.
	runner := newFake()
	runner.started = make(chan Job, 1)
	other := New(runner, quiet)
	other.OnHostKilled(func() { t.Error("reported a host kill for a job that finished") })
	other.Offer("acme", offer("a-2"))
	other.Start(context.Background(), "acme", start("a-2"))
	<-runner.started
	runner.finish <- 1
	<-other.Exits("acme")
}

func TestAMachineCoolingOffDeclinesEveryOffer(t *testing.T) {
	m := New(newFake(), quiet)
	m.Limit(limited(Limits{MaxJobs: 1, Hold: true}))
	if reason := m.Offer("acme", offer("a-1")); reason != ReasonCoolingOff {
		t.Fatalf("offer while cooling off: %q", reason)
	}
	m.Limit(limited(Limits{MaxJobs: 1}))
	if reason := m.Offer("acme", offer("a-1")); reason != "" {
		t.Fatalf("offer after: %q", reason)
	}
}

func macOSOffer(id string) *protocol.JobAssign {
	return &protocol.JobAssign{AssignmentID: id, Label: "hive-macos", Arch: protocol.KindMacOS, VCPUs: 4, TimeoutMinutes: 60}
}

// A Mac that runs each macOS job in a VM runs two at most, whatever its
// owner allows, and a Linux job still takes a slot beside them.
func TestMacOSVMsAreHeldToTheLicence(t *testing.T) {
	m := New(newFake(), quiet)
	m.Native(newFake())
	m.Limit(limited(Limits{MaxJobs: 4, MacOS: true, MostMacOS: 2}))
	for _, id := range []string{"m-1", "m-2"} {
		if reason := m.Offer("acme", macOSOffer(id)); reason != "" {
			t.Fatalf("macOS job %s: %q", id, reason)
		}
	}
	if reason := m.Offer("acme", macOSOffer("m-3")); reason != ReasonMacOSVMs {
		t.Fatalf("a third macOS VM: %q, want %q", reason, ReasonMacOSVMs)
	}
	if reason := m.Offer("acme", offer("l-1")); reason != "" {
		t.Fatalf("a Linux job beside two macOS VMs: %q", reason)
	}
	m.Release("acme", "m-1")
	if reason := m.Offer("acme", macOSOffer("m-3")); reason != "" {
		t.Fatalf("a macOS job once one has gone: %q", reason)
	}
}

// A macOS job run in a VM needs that VM's memory free on the host. One run
// on the Mac itself is not checked, as before.
func TestAMacOSVMNeedsItsMemory(t *testing.T) {
	m := New(newFake(), quiet)
	m.Native(newFake())
	m.Limit(limited(Limits{MaxJobs: 2, MacOS: true, Available: 8*gib + MemoryMargin - 1, GuestMemory: fourGiB, MacOSMemory: 8 * gib}))
	if reason := m.Offer("acme", macOSOffer("m-1")); reason != ReasonLowMemory {
		t.Fatalf("an 8 GiB macOS VM a byte short: %q, want low memory", reason)
	}
	m.Limit(limited(Limits{MaxJobs: 2, MacOS: true, Available: 8*gib + MemoryMargin - 1, GuestMemory: fourGiB}))
	if reason := m.Offer("acme", macOSOffer("m-1")); reason != "" {
		t.Fatalf("a macOS job on the Mac itself: %q", reason)
	}
	m.Release("acme", "m-1")
	m.Limit(limited(Limits{MaxJobs: 2, MacOS: true, Available: 12*gib + MemoryMargin - 1, GuestMemory: fourGiB, MacOSMemory: 8 * gib}))
	if reason := m.Offer("acme", macOSOffer("m-1")); reason != "" {
		t.Fatalf("a macOS VM with room: %q", reason)
	}
	if reason := m.Offer("acme", sized("l-1", 2)); reason != ReasonLowMemory {
		t.Fatalf("a Linux job past an accepted macOS VM: %q, want low memory", reason)
	}
}

// chooser hands out whichever runner it is pointed at now.
type chooser struct{ now Runner }

func (c *chooser) Run(context.Context, Job) (Result, error) { panic("a chooser runs nothing itself") }

func (c *chooser) Choose() Runner { return c.now }

// The way a job runs is decided when it is offered. Changing it before the
// job starts changes how later jobs run, not that one.
func TestAJobRunsTheWayItWasAcceptedFor(t *testing.T) {
	inVM, asUser := newFake(), newFake()
	ways := &chooser{now: inVM}
	m := New(newFake(), quiet)
	m.Native(ways)
	m.Limit(limited(Limits{MaxJobs: 2, MacOS: true}))
	if reason := m.Offer("acme", macOSOffer("m-1")); reason != "" {
		t.Fatalf("offer: %q", reason)
	}
	ways.now = asUser
	if err := m.Start(context.Background(), "acme", start("m-1")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-inVM.started:
	case <-asUser.started:
		t.Fatal("a job accepted for a VM ran another way")
	case <-within(t, "start"):
		t.Fatal("the job never started")
	}
	inVM.finish <- 0
	<-m.Exits("acme")

	// A way that cannot run declines the offer with its reason.
	ways.now = broken{}
	if reason := m.Offer("acme", macOSOffer("m-2")); reason != (broken{}).Unusable() {
		t.Fatalf("offer to an unusable way: %q", reason)
	}
}

// oneAtATime is a way of running jobs that takes one at most.
type oneAtATime struct{ *fake }

func (oneAtATime) Limit(limits *Limits) { limits.MostMacOS = 1 }

// An offer's limits are those of the way chosen for it, read once with the
// choice: not the setting read again, which may have changed in between.
func TestTheLimitsAreThoseOfTheWayChosen(t *testing.T) {
	ways := &chooser{now: oneAtATime{newFake()}}
	m := New(newFake(), quiet)
	m.Native(ways)
	m.Limit(limited(Limits{MaxJobs: 4, MacOS: true}))
	if reason := m.Offer("acme", macOSOffer("m-1")); reason != "" {
		t.Fatalf("first: %q", reason)
	}
	if reason := m.Offer("acme", macOSOffer("m-2")); reason != ReasonMacOSVMs {
		t.Fatalf("second, past the chosen way's limit: %q", reason)
	}
	ways.now = newFake()
	if reason := m.Offer("acme", macOSOffer("m-2")); reason != "" {
		t.Fatalf("second, by a way with no limit of its own: %q", reason)
	}
}

// A job holds the memory it was accepted with, so a Linux offer counts a
// macOS VM accepted before it whatever the limits say of macOS now.
func TestALinuxOfferCountsTheMacOSVMsAccepted(t *testing.T) {
	m := New(newFake(), quiet)
	m.Native(newFake())
	m.Limit(limited(Limits{MaxJobs: 2, MacOS: true, Available: 12*gib + MemoryMargin - 1, GuestMemory: fourGiB, MacOSMemory: 8 * gib}))
	if reason := m.Offer("acme", macOSOffer("m-1")); reason != "" {
		t.Fatalf("a macOS VM with room: %q", reason)
	}
	m.Limit(limited(Limits{MaxJobs: 2, MacOS: true, Available: 12*gib + MemoryMargin - 1, GuestMemory: fourGiB}))
	if reason := m.Offer("acme", sized("l-1", 2)); reason != ReasonLowMemory {
		t.Fatalf("a Linux job past an accepted macOS VM, with no macOS memory in its limits: %q, want low memory", reason)
	}
}

// Where what is free cannot be read, as on a Mac, a planned budget holds
// every job held, running or not, and the owner's reserve.
func TestAMacPlansItsMemory(t *testing.T) {
	m := New(newFake(), quiet)
	m.Native(newFake())
	sixteen := Limits{MaxJobs: 4, MacOS: true, Planned: 16 * gib, GuestMemory: fourGiB, MacOSMemory: 8 * gib}
	m.Limit(limited(sixteen))
	if reason := m.Offer("acme", macOSOffer("m-1")); reason != "" {
		t.Fatalf("one 8 GiB macOS VM on a 16 GiB Mac: %q", reason)
	}
	if reason := m.Offer("acme", macOSOffer("m-2")); reason != ReasonLowMemory {
		t.Fatalf("a second 8 GiB macOS VM on a 16 GiB Mac: %q, want low memory", reason)
	}
	if err := m.Start(context.Background(), "acme", start("m-1")); err != nil {
		t.Fatal(err)
	}
	if reason := m.Offer("acme", sized("l-1", 2)); reason != "" {
		t.Fatalf("a 4 GiB Linux job beside a running macOS VM: %q", reason)
	}
	if reason := m.Offer("acme", sized("l-2", 2)); reason != ReasonLowMemory {
		t.Fatalf("another 4 GiB Linux job, past the plan: %q, want low memory", reason)
	}
	m.Release("acme", "l-1")
	reserved := sixteen
	reserved.Reserve = 4 * gib
	m.Limit(limited(reserved))
	if reason := m.Offer("acme", sized("l-1", 2)); reason != ReasonLowMemory {
		t.Fatalf("a Linux job into the owner's reserve: %q, want low memory", reason)
	}
}

// causing is a runner that reports why its context ended, as the VM runner
// does when it decides what to tell the guest and the pool.
type causing struct {
	started chan struct{}
	cause   chan error
}

func (r *causing) Run(ctx context.Context, _ Job) (Result, error) {
	close(r.started)
	<-ctx.Done()
	r.cause <- context.Cause(ctx)
	return Result{ExitCode: -1}, ctx.Err()
}

// A released job's runner is told it was released, whatever its timeout:
// read as a plain cancel, it said the daemon was shutting down.
func TestARunnerSeesItsJobWasReleased(t *testing.T) {
	runner := &causing{started: make(chan struct{}), cause: make(chan error, 1)}
	m := New(runner, quiet)
	m.Offer("acme", offer("a-1"))
	if err := m.Start(context.Background(), "acme", start("a-1")); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	m.Release("acme", "a-1")
	select {
	case cause := <-runner.cause:
		if !errors.Is(cause, ErrReleased) {
			t.Fatalf("the runner saw %v, want ErrReleased", cause)
		}
	case <-within(t, "the release"):
		t.Fatal("the runner was never stopped")
	}
}

func twoGiBACore(vcpus int) uint64   { return uint64(max(vcpus*2, 4)) * gib }
func threeQuarters(vcpus int) uint64 { return max(twoGiBACore(vcpus)/4*3, 4*gib) }

// A job that does not fit at its size, beside the reserve, runs with less
// memory, rounded down; its VM is told how much.
func TestAJobThatDoesNotFitItsSizeRunsLeaner(t *testing.T) {
	log, logger := recording()
	runner := newFake()
	m := New(runner, logger)
	// The host that queued Versal's jobs: 12917 MB free, 4 GB reserved.
	m.Limit(limited(Limits{MaxJobs: 1, Available: 12917 << 20, Reserve: 4 * gib, GuestMemory: twoGiBACore, LeanestGuest: threeQuarters}))
	if reason := m.Offer("acme", sized("a-1", 4)); reason != "" {
		t.Fatalf("offer declined: %s", reason)
	}
	if err := m.Start(context.Background(), "acme", start("a-1")); err != nil {
		t.Fatal(err)
	}
	job := <-runner.started
	if want := uint64(7680) << 20; job.MemoryBytes != want {
		t.Fatalf("the job's VM was given %d MB, want %d", job.MemoryBytes>>20, want>>20)
	}
	record := log.find(t, "giving a job less memory than its size, to fit what is free")
	if record == nil || record["guest_mb"] != float64(7680) || record["size_mb"] != float64(8192) {
		t.Fatalf("the lean job was logged as %v", record)
	}
	runner.finish <- 0
	<-m.Exits("acme")
}

// Below three quarters of its size a job is declined, as before; at its
// size it is given its size.
func TestALeanJobHasAFloor(t *testing.T) {
	m := New(newFake(), quiet)
	m.Limit(limited(Limits{MaxJobs: 1, Available: 6*gib + MemoryMargin + 4*gib - 1, Reserve: 4 * gib, GuestMemory: twoGiBACore, LeanestGuest: threeQuarters}))
	if reason := m.Offer("acme", sized("a-1", 4)); reason != ReasonLowMemory {
		t.Fatalf("offer below the floor: %q, want low memory", reason)
	}

	runner := newFake()
	m = New(runner, quiet)
	m.Limit(limited(Limits{MaxJobs: 1, Available: 8*gib + MemoryMargin + 4*gib, Reserve: 4 * gib, GuestMemory: twoGiBACore, LeanestGuest: threeQuarters}))
	if reason := m.Offer("acme", sized("a-1", 4)); reason != "" {
		t.Fatalf("offer that fits: %q", reason)
	}
	if err := m.Start(context.Background(), "acme", start("a-1")); err != nil {
		t.Fatal(err)
	}
	if job := <-runner.started; job.MemoryBytes != 0 {
		t.Fatalf("a job that fits was given %d MB, want its size", job.MemoryBytes>>20)
	}
	runner.finish <- 0
	<-m.Exits("acme")
}

// The coordinator tells a machine that is full from one a job does not fit
// by these words (`full` in apps/coordinator/src/schedule.ts): only the
// first are passed over without counting against the machine.
func TestTheReasonsForBeingFullSaySo(t *testing.T) {
	for _, reason := range []string{ReasonBusy, ReasonMacOSVMs} {
		if !strings.Contains(reason, " is running as many ") {
			t.Errorf("%q does not say the machine is full", reason)
		}
	}
	for _, reason := range []string{ReasonLowMemory, ReasonNoCores, ReasonTooLarge, ReasonCoolingOff, ReasonNoRunner, ReasonWrongArch, ReasonNoMacOS} {
		if strings.Contains(reason, " is running as many ") {
			t.Errorf("%q reads as the machine being full", reason)
		}
	}
}
