//go:build linux

package vm

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func answering(port uint32, said *[]string, lock *sync.Mutex) *GuestService {
	return &GuestService{
		Port: port,
		Handler: func(pool func() string) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				lock.Lock()
				*said = append(*said, pool())
				lock.Unlock()
				_, _ = w.Write([]byte("ok\n"))
			})
		},
		Enabled: func() bool { return true },
	}
}

// Two VMs on one Linux host can be given the same socket address: two
// daemons on one machine, or a VM that is not ours at all. The second VM
// takes another address rather than failing, and each is still answered as
// itself and nobody else.
func TestTwoVMsWantingOneSocketAddress(t *testing.T) {
	first := realRunner(t)
	var lock sync.Mutex
	var firstSaw, secondSaw []string
	first.Guest = answering(47081, &firstSaw, &lock)
	// The same work directory, so the same address for its first VM.
	second := &Runner{Image: first.Image, WorkDir: first.WorkDir, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	second.Guest = answering(47082, &secondSaw, &lock)

	held, err := first.boot(2, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer held.destroy()
	if !held.served {
		t.Fatal("the first VM has no socket to the host")
	}

	ctx, cancel := context.WithTimeout(context.Background(), patient(2*time.Minute))
	defer cancel()
	report, err := second.SelfTest(ctx, 2)
	if err != nil {
		t.Fatalf("the second VM did not run: %v\n%s", err, report)
	}
	if !strings.Contains(report, "CACHE=ok") {
		t.Fatalf("the second VM did not reach its host's service:\n%s", report)
	}
	lock.Lock()
	defer lock.Unlock()
	if len(secondSaw) == 0 || secondSaw[0] != "selftest" {
		t.Errorf("the second runner saw %v, want its own VM's health check", secondSaw)
	}
	if len(firstSaw) != 0 {
		t.Errorf("the first runner was asked %v by a VM that is not its own", firstSaw)
	}
}

// A process on the host, or any caller the runner did not start, is refused
// on the socket the guests dial.
func TestTheHostSocketRefusesStrangers(t *testing.T) {
	r := realRunner(t)
	var lock sync.Mutex
	var saw []string
	r.Guest = answering(47083, &saw, &lock)
	if err := r.listen(); err != nil {
		t.Skipf("no host socket on this machine: %v", err)
	}
	conn, err := dialVsockLoopback(47083)
	if err != nil {
		t.Skipf("this kernel has no loopback for VM sockets: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte("GET /hive/health HTTP/1.1\r\nHost: host\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	answer, _ := io.ReadAll(conn)
	if !strings.HasPrefix(string(answer), "HTTP/1.1 403") {
		t.Errorf("a stranger was answered %q, want 403", strings.SplitN(string(answer), "\r\n", 2)[0])
	}
	lock.Lock()
	defer lock.Unlock()
	if len(saw) != 0 {
		t.Errorf("a stranger reached the service: %v", saw)
	}
}
