package update

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// The daemon's own update must never take a machine's job away: it stops
// new ones and waits for the ones in hand before it runs the installer.
func TestTheDaemonUpdatesItselfOnlyOnceItHasNoJob(t *testing.T) {
	t.Setenv("HIVE_CONFIG", filepath.Join(t.TempDir(), "machine.json"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"9.9.9","commit":"abc","image":{"version":"1-2.0.0"}}`))
	}))
	defer server.Close()
	t.Setenv("HIVE_RELEASES", server.URL)

	// Told already, so that the test does not put a notice on the screen.
	if err := SaveState(State{Notified: "9.9.9"}); err != nil {
		t.Fatal(err)
	}
	var busy, updating atomic.Bool
	var applied atomic.Int32
	busy.Store(true)
	options := Options{
		Version:    "9.9.8",
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		AutoUpdate: func() bool { return true },
		Busy:       busy.Load,
		Updating:   &updating,
		installed:  func() bool { return true },
		poll:       5 * time.Millisecond,
		apply: func(context.Context) error {
			if busy.Load() {
				t.Error("the installer was run while a job was running")
			}
			if !updating.Load() {
				t.Error("the installer was run while the machine could still be offered a job")
			}
			applied.Add(1)
			return nil
		},
	}
	done := make(chan struct{})
	go func() { tick(context.Background(), options); close(done) }()

	// A job is running: no new ones, and no installer.
	time.Sleep(80 * time.Millisecond)
	if applied.Load() != 0 {
		t.Fatal("updated under a running job")
	}
	if !updating.Load() {
		t.Fatal("still open to new jobs while waiting to update")
	}
	// It ends, and the update goes ahead.
	busy.Store(false)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("never updated once the machine was idle")
	}
	if applied.Load() != 1 {
		t.Fatalf("the installer ran %d times", applied.Load())
	}
}

func TestTheDaemonFetchesANewImageWhenTheDaemonIsCurrent(t *testing.T) {
	t.Setenv("HIVE_CONFIG", filepath.Join(t.TempDir(), "machine.json"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"0.4.21","commit":"abc","image":{"version":"11-2.337.0"}}`))
	}))
	defer server.Close()
	t.Setenv("HIVE_RELEASES", server.URL)

	var updating atomic.Bool
	var applied atomic.Int32
	tick(context.Background(), Options{
		Version: "0.4.21", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		AutoUpdate: func() bool { return true }, Busy: func() bool { return false },
		Image:    func() string { return "10-2.337.0" },
		Updating: &updating, installed: func() bool { return true }, poll: time.Millisecond,
		apply: func(context.Context) error { applied.Add(1); return nil },
	})
	if applied.Load() != 1 {
		t.Fatalf("the installer ran %d times", applied.Load())
	}
}

func TestTheDaemonLeavesItselfAloneWhenItIsCurrentOrToldTo(t *testing.T) {
	t.Setenv("HIVE_CONFIG", filepath.Join(t.TempDir(), "machine.json"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"9.9.9","commit":"abc","image":{"version":"1-2.0.0"}}`))
	}))
	defer server.Close()
	t.Setenv("HIVE_RELEASES", server.URL)
	for _, test := range []struct {
		name, version string
		auto          bool
	}{
		{"already the release", "9.9.9", true},
		{"newer than the release", "9.10.0", true},
		{"its owner updates by hand", "9.9.8", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := SaveState(State{Notified: "9.9.9"}); err != nil {
				t.Fatal(err)
			}
			var updating atomic.Bool
			tick(context.Background(), Options{
				Version: test.version, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
				AutoUpdate: func() bool { return test.auto }, Busy: func() bool { return false },
				Image:    func() string { return "1-2.0.0" },
				Updating: &updating, installed: func() bool { return true }, poll: time.Millisecond,
				apply: func(context.Context) error { t.Error("the installer was run"); return nil },
			})
			if updating.Load() {
				t.Error("stopped taking jobs for an update that is not happening")
			}
		})
	}
}

// A failed update is tried again within the hour, then less often each
// time it fails, and the waiting is remembered across a restart.
func TestAFailedUpdateIsTriedAgainSoon(t *testing.T) {
	t.Setenv("HIVE_CONFIG", filepath.Join(t.TempDir(), "machine.json"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"9.9.9","commit":"abc","image":{"version":"1-2.0.0"}}`))
	}))
	defer server.Close()
	t.Setenv("HIVE_RELEASES", server.URL)
	if err := SaveState(State{Notified: "9.9.9"}); err != nil {
		t.Fatal(err)
	}
	var updating atomic.Bool
	var applied atomic.Int32
	failing := Options{
		Version: "9.9.8", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		AutoUpdate: func() bool { return true }, Busy: func() bool { return false },
		Updating: &updating, installed: func() bool { return true }, poll: time.Millisecond,
		apply: func(context.Context) error { applied.Add(1); return errors.New("cut off") },
	}
	if retry := tick(context.Background(), failing); retry != time.Hour {
		t.Fatalf("the first failure is tried again in %v, want an hour", retry)
	}
	if updating.Load() {
		t.Fatal("still turning jobs away after the update failed")
	}
	// Asked again before then, as a restarted daemon would be: no try.
	if retry := tick(context.Background(), failing); retry <= 0 || retry > time.Hour || applied.Load() != 1 {
		t.Fatalf("tried again at once (%d tries), or waits %v", applied.Load(), retry)
	}
	for failures, want := range map[int]time.Duration{1: time.Hour, 2: 2 * time.Hour, 3: 4 * time.Hour, 6: Every, 40: Every} {
		if got := (State{Failures: failures}).RetryAfter(); got != want {
			t.Errorf("after %d failures: %v, want %v", failures, got, want)
		}
	}
	// Once current, the failures are forgotten.
	failing.Version = "9.9.9"
	failing.Image = func() string { return "1-2.0.0" }
	tick(context.Background(), failing)
	if state, _ := LoadState(); state.Failures != 0 || !state.Failed.IsZero() {
		t.Fatalf("a current daemon still remembers failing: %+v", state)
	}
}
