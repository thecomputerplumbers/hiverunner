package host

import (
	"log/slog"
	"os/exec"
	"sync"
)

// Awake keeps the machine from sleeping when idle while it holds a job. A
// machine that sleeps under a job freezes its VM: the runner stops answering
// GitHub, which gives the job up as "lost communication with the server"
// about ten minutes later, and the VM finishes the job for nobody when the
// machine wakes. Nobody at the keyboard is exactly when an owner lends it,
// so idle sleep is what catches it.
//
// Each job holds it once, from when it starts until its VM is gone. The
// first hold starts the platform's own inhibitor and the last release stops
// it; the inhibitor ends with the daemon too, so a daemon that dies never
// leaves the machine kept awake. It cannot keep a MacBook awake with its lid
// closed: only `pmset disablesleep` does that (see StaysAwake).
type Awake struct {
	log *slog.Logger
	// command is the inhibitor, or nil where there is none; stop ends one.
	command func() *exec.Cmd
	stop    func(*exec.Cmd)

	mu   sync.Mutex
	held int
	cmd  *exec.Cmd
	done chan struct{}
}

// NewAwake returns an Awake for this platform.
func NewAwake(log *slog.Logger) *Awake {
	return &Awake{log: log, command: awakeCommand, stop: stopAwake}
}

// Hold keeps the machine awake until a matching Release.
func (a *Awake) Hold() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.held++
	// Started by the first hold, or again by a later one if it ended.
	if a.cmd != nil || a.command == nil {
		return
	}
	cmd := a.command()
	if cmd == nil {
		return
	}
	if err := cmd.Start(); err != nil {
		a.log.Warn("could not keep this machine awake while it runs jobs; it may sleep under one", "error", err)
		return
	}
	done := make(chan struct{})
	go func() {
		// Reaps it, and says so if it ends while it should be holding.
		err := cmd.Wait()
		a.mu.Lock()
		stopped := a.cmd != cmd
		if !stopped {
			a.cmd, a.done = nil, nil
		}
		a.mu.Unlock()
		if !stopped {
			a.log.Warn("stopped keeping this machine awake while it runs jobs; it may sleep under one", "error", err)
		}
		close(done)
	}()
	a.cmd, a.done = cmd, done
	a.log.Info("keeping this machine awake while it runs jobs")
}

// Release ends one Hold. The last one lets the machine sleep again.
func (a *Awake) Release() {
	a.mu.Lock()
	if a.held == 0 {
		a.mu.Unlock()
		return
	}
	a.held--
	if a.held > 0 || a.cmd == nil {
		a.mu.Unlock()
		return
	}
	cmd, done := a.cmd, a.done
	a.cmd, a.done = nil, nil
	a.mu.Unlock()
	a.stop(cmd)
	<-done
	a.log.Info("no job running; this machine may sleep again")
}
