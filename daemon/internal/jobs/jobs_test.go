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
	if !m.Busy() || m.Current("acme") != "a-1" || m.Current("globex") != "" {
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

func TestReleaseStopsARunningJobQuietly(t *testing.T) {
	runner := newFake()
	m := New(runner, quiet)
	m.Offer("acme", offer("a-1"))
	if err := m.Start(context.Background(), "acme", start("a-1")); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	m.Release("acme", "a-1")
	select {
	case <-runner.stopped:
	case <-within(t, "stop"):
		t.Fatal("the runner was not stopped")
	}
	if m.Busy() {
		t.Fatal("the slot was not freed")
	}
	select {
	case exit := <-m.Exits("acme"):
		t.Fatalf("a released job reported %+v", exit)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestTheJobTimeoutDestroysTheRunner(t *testing.T) {
	runner := newFake()
	m := New(runner, quiet)
	short := offer("a-1")
	short.TimeoutMinutes = 0
	m.Offer("acme", short)
	m.mu.Lock()
	m.current.timeout = 30 * time.Millisecond
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
