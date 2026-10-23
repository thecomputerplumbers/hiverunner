package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// records is a log the test reads back, one JSON object a line.
type records struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (r *records) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

// find is the first record with this message, or nil.
func (r *records) find(t *testing.T, msg string) map[string]any {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for line := range strings.SplitSeq(strings.TrimSpace(r.buf.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if record["msg"] == msg {
			return record
		}
	}
	return nil
}

func recording() (*records, *slog.Logger) {
	r := &records{}
	return r, slog.New(slog.NewJSONHandler(r, nil))
}

// The decline names each part of what was needed, so the total is not
// mistaken for the size of the job.
func TestAMemoryDeclineBreaksDownWhatWasNeeded(t *testing.T) {
	log, logger := recording()
	m := New(newFake(), logger)
	m.Limit(limited(Limits{MaxJobs: 2, Available: 32 * gib, Reserve: 24 * gib, GuestMemory: fourGiB}))
	if reason := m.Offer("acme", sized("a-1", 2)); reason != "" {
		t.Fatalf("first job: %q", reason)
	}
	if reason := m.Offer("acme", sized("a-2", 2)); reason != ReasonLowMemory {
		t.Fatalf("second job: %q, want low memory", reason)
	}
	record := log.find(t, "declining a job: not enough free memory")
	if record == nil {
		t.Fatal("no decline was logged")
	}
	want := map[string]float64{
		"available_mb": 32 * 1024,
		"needed_mb":    (4 + 4 + 1 + 24) * 1024,
		"guest_mb":     4 * 1024,
		"held_mb":      4 * 1024,
		"margin_mb":    1024,
		"reserve_mb":   24 * 1024,
	}
	for field, value := range want {
		if record[field] != value {
			t.Errorf("%s = %v, want %v", field, record[field], value)
		}
	}
	if record["assignment"] != "a-2" || record["planned"] != false {
		t.Errorf("decline = %v", record)
	}
}

// peaked finishes every job having held a known amount of memory.
type peaked struct{ peak uint64 }

func (p peaked) Run(context.Context, Job) (Result, error) {
	return Result{ExitCode: 0, PeakGuestBytes: p.peak}, nil
}

func endOf(t *testing.T, runner Runner) map[string]any {
	t.Helper()
	log, logger := recording()
	m := New(runner, logger)
	if reason := m.Offer("acme", offer("a-1")); reason != "" {
		t.Fatal(reason)
	}
	if err := m.Start(context.Background(), "acme", start("a-1")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.Exits("acme"):
	case <-within(t, "exit"):
		t.Fatal("no result was delivered")
	}
	record := log.find(t, "job ended")
	if record == nil {
		t.Fatal("the end of the job was not logged")
	}
	return record
}

func TestTheEndOfAJobSaysHowMuchMemoryItsGuestPeakedAt(t *testing.T) {
	record := endOf(t, peaked{peak: 3*gib + 512<<20})
	if record["peak_guest_mb"] != float64(3*1024+512) {
		t.Fatalf("peak_guest_mb = %v", record["peak_guest_mb"])
	}
}

// A peak that could not be read is left out, not reported as nothing used.
func TestAnUnreadPeakIsLeftOut(t *testing.T) {
	record := endOf(t, peaked{})
	if _, ok := record["peak_guest_mb"]; ok {
		t.Fatalf("peak_guest_mb = %v, want it absent", record["peak_guest_mb"])
	}
}
