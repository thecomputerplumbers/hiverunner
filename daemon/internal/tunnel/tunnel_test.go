package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/jobs"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/protocol"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// coordinator is a stand-in that runs `serve` for each accepted tunnel.
func coordinator(t *testing.T, serve func(ctx context.Context, c *websocket.Conn, attempt int)) string {
	t.Helper()
	var attempts atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer hwm_good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		serve(r.Context(), c, int(attempts.Add(1)))
	}))
	t.Cleanup(s.Close)
	return "ws" + strings.TrimPrefix(s.URL, "http")
}

func write(ctx context.Context, c *websocket.Conn, message string) {
	_ = c.Write(ctx, websocket.MessageText, []byte(message))
}

func read(ctx context.Context, t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	for {
		_, raw, err := c.Read(ctx)
		if err != nil {
			t.Errorf("coordinator read: %v", err)
			return nil
		}
		if string(raw) == protocol.Ping {
			write(ctx, c, protocol.Pong)
			continue
		}
		var message map[string]any
		_ = json.Unmarshal(raw, &message)
		return message
	}
}

func options(url, token string, presence *atomic.Value) Options {
	return Options{
		URL: url, Token: token, DaemonVersion: "test", Log: quiet,
		PresenceEvery: 10 * time.Millisecond, PingEvery: 20 * time.Millisecond,
		Presence: func() protocol.Presence { return presence.Load().(protocol.Presence) },
	}
}

func TestHelloPresenceAndDecliningWithoutARunner(t *testing.T) {
	var presence atomic.Value
	presence.Store(protocol.Online)
	done := make(chan struct{})
	url := coordinator(t, func(ctx context.Context, c *websocket.Conn, _ int) {
		defer close(done)
		write(ctx, c, `{"type":"welcome","machineId":"m","protocol":3}`)
		if hello := read(ctx, t, c); hello["type"] != "hello" || hello["presence"] != "online" {
			t.Errorf("first frame = %v", hello)
		}
		presence.Store(protocol.Paused)
		if update := read(ctx, t, c); update["type"] != "presence" || update["presence"] != "paused" {
			t.Errorf("presence frame = %v", update)
		}
		write(ctx, c, `{"type":"job.assign","assignmentId":"a-1","label":"hive","vcpus":2,"timeoutMinutes":60}`)
		if answer := read(ctx, t, c); answer["type"] != "job.rejected" || answer["assignmentId"] != "a-1" {
			t.Errorf("answer to job.assign = %v", answer)
		}
		// An unknown frame is ignored, not fatal: the next ping still arrives.
		write(ctx, c, `{"type":"something-new"}`)
	})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- Run(ctx, options(url, "hwm_good", &presence)) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the exchange did not complete")
	}
	cancel()
	if err := <-finished; err != nil {
		t.Fatalf("Run() after cancel = %v", err)
	}
}

func TestVerdictsThatReconnectingCannotFix(t *testing.T) {
	var presence atomic.Value
	presence.Store(protocol.Online)
	for code, want := range map[websocket.StatusCode]error{
		protocol.CloseRevoked:     ErrRevoked,
		protocol.CloseReplaced:    ErrReplaced,
		protocol.CloseUnsupported: ErrUnsupported,
	} {
		url := coordinator(t, func(ctx context.Context, c *websocket.Conn, _ int) {
			_ = c.Close(code, "")
		})
		if err := Run(context.Background(), options(url, "hwm_good", &presence)); !errors.Is(err, want) {
			t.Errorf("close %d: Run() = %v, want %v", code, err, want)
		}
	}
	url := coordinator(t, func(context.Context, *websocket.Conn, int) {})
	if err := Run(context.Background(), options(url, "hwm_bad", &presence)); !errors.Is(err, ErrRevoked) {
		t.Errorf("refused token: Run() = %v, want %v", err, ErrRevoked)
	}
}

func TestADroppedConnectionIsRetried(t *testing.T) {
	var presence atomic.Value
	presence.Store(protocol.Online)
	second := make(chan struct{})
	url := coordinator(t, func(ctx context.Context, c *websocket.Conn, attempt int) {
		write(ctx, c, `{"type":"welcome","machineId":"m","protocol":3}`)
		read(ctx, t, c)
		if attempt == 1 {
			return // Drop it, as a deploy would.
		}
		close(second)
		<-ctx.Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = Run(ctx, options(url, "hwm_good", &presence)) }()
	select {
	case <-second:
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon did not reconnect")
	}
}

// instant is a runner that finishes at once with a fixed code.
type instant struct{ code int }

func (r instant) Run(context.Context, jobs.Job) (int, error) { return r.code, nil }

// held is a runner that runs until told to finish.
type held struct{ finish chan int }

func (r held) Run(ctx context.Context, _ jobs.Job) (int, error) {
	select {
	case code := <-r.finish:
		return code, nil
	case <-ctx.Done():
		return -1, ctx.Err()
	}
}

func TestOfferAcceptStartExit(t *testing.T) {
	var presence atomic.Value
	presence.Store(protocol.Online)
	done := make(chan struct{})
	url := coordinator(t, func(ctx context.Context, c *websocket.Conn, _ int) {
		defer close(done)
		write(ctx, c, `{"type":"welcome","machineId":"m","protocol":3}`)
		if hello := read(ctx, t, c); hello["assignmentId"] != nil {
			t.Errorf("an idle machine claimed a job in hello: %v", hello)
		}
		write(ctx, c, `{"type":"job.assign","assignmentId":"a-1","label":"hive","vcpus":2,"timeoutMinutes":60}`)
		if answer := read(ctx, t, c); answer["type"] != "job.accepted" {
			t.Errorf("answer to the offer = %v", answer)
		}
		// A start for a job that was never offered is refused, not run.
		write(ctx, c, `{"type":"job.start","assignmentId":"zz","jitConfig":"x","runnerName":"r"}`)
		if answer := read(ctx, t, c); answer["type"] != "job.exited" || answer["assignmentId"] != "zz" || answer["exitCode"] != float64(-1) {
			t.Errorf("answer to a stray start = %v", answer)
		}
		write(ctx, c, `{"type":"job.start","assignmentId":"a-1","jitConfig":"x","runnerName":"hw-m-a-1"}`)
		if exit := read(ctx, t, c); exit["type"] != "job.exited" || exit["assignmentId"] != "a-1" || exit["exitCode"] != float64(7) {
			t.Errorf("job result = %v", exit)
		}
		// A job shorter than a presence tick still ends with the machine
		// saying it is free, or the pool would think it busy for ever.
		if after := read(ctx, t, c); after["type"] != "presence" || after["presence"] != "online" {
			t.Errorf("after the job the daemon sent %v, want presence online", after)
		}
		// The same after a release, including of an offer it never held.
		write(ctx, c, `{"type":"job.release","assignmentId":"never-held"}`)
		if after := read(ctx, t, c); after["type"] != "presence" || after["presence"] != "online" {
			t.Errorf("after a release the daemon sent %v, want presence online", after)
		}
	})
	o := options(url, "hwm_good", &presence)
	o.PoolID = "acme"
	o.Jobs = jobs.New(instant{code: 7}, quiet)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = Run(ctx, o) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the exchange did not complete")
	}
}

// The tunnel drops mid-job. The job carries on; the new connection says what
// it is holding, and delivers the result when the job ends.
func TestAJobSurvivesAReconnect(t *testing.T) {
	var presence atomic.Value
	presence.Store(protocol.Online)
	runner := held{finish: make(chan int)}
	done := make(chan struct{})
	url := coordinator(t, func(ctx context.Context, c *websocket.Conn, attempt int) {
		write(ctx, c, `{"type":"welcome","machineId":"m","protocol":3}`)
		hello := read(ctx, t, c)
		if attempt == 1 {
			write(ctx, c, `{"type":"job.assign","assignmentId":"a-1","label":"hive","vcpus":2,"timeoutMinutes":60}`)
			read(ctx, t, c)
			write(ctx, c, `{"type":"job.start","assignmentId":"a-1","jitConfig":"x","runnerName":"hw-m-a-1"}`)
			time.Sleep(100 * time.Millisecond)
			return // Drop the connection with the job running.
		}
		defer close(done)
		if holding, _ := hello["holding"].([]any); len(holding) != 1 || holding[0] != "a-1" {
			t.Errorf("reconnect hello = %v, want it to name the running job", hello)
		}
		runner.finish <- 0
		if exit := read(ctx, t, c); exit["type"] != "job.exited" || exit["assignmentId"] != "a-1" {
			t.Errorf("after reconnect got %v", exit)
		}
	})
	o := options(url, "hwm_good", &presence)
	o.PoolID = "acme"
	o.Jobs = jobs.New(runner, quiet)
	o.JobContext = context.Background()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = Run(ctx, o) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the job's result never arrived after the reconnect")
	}
}

// The owner's limit travels in hello, so changing it must reach the pool
// without anyone restarting the daemon.
func TestChangingTheJobSizeLimitSaysHelloAgain(t *testing.T) {
	var presence atomic.Value
	presence.Store(protocol.Online)
	var limit atomic.Int32
	limit.Store(4)
	done := make(chan struct{})
	url := coordinator(t, func(ctx context.Context, c *websocket.Conn, attempt int) {
		write(ctx, c, `{"type":"welcome","machineId":"m","protocol":3}`)
		hello := read(ctx, t, c)
		switch attempt {
		case 1:
			if hello["maxVcpus"] != float64(4) {
				t.Errorf("first hello = %v", hello)
			}
			limit.Store(0)
			// The daemon hangs up to reconnect; wait for it.
			_, _, _ = c.Read(ctx)
		case 2:
			if _, limited := hello["maxVcpus"]; limited {
				t.Errorf("hello after the limit was lifted = %v", hello)
			}
			close(done)
		}
	})
	o := options(url, "hwm_good", &presence)
	o.Limits = func() jobs.Limits { return jobs.Limits{MaxVCPUs: int(limit.Load())} }
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- Run(ctx, o) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon did not say hello again")
	}
	cancel()
	if err := <-finished; err != nil {
		t.Fatalf("Run() after cancel = %v", err)
	}
}
