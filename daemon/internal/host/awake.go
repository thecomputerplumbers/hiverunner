package host

import (
	"bufio"
	"bytes"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
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
	// With signals, it writes a line to its stdout once it holds, and holds
	// nothing until then; without, it holds from its start.
	command func() *exec.Cmd
	signals bool
	stop    func(*exec.Cmd)
	// allow makes sure the inhibitor will be let hold, or says why not and
	// what to do about it. Nil needs nothing.
	allow func(*slog.Logger) bool

	mu   sync.Mutex
	held int
	cmd  *exec.Cmd
	done chan struct{}
	// holding is whether cmd holds yet. failing is whether the last
	// inhibitor failed and that was said, so that the same failure for
	// every job after it is not said again.
	holding bool
	failing bool
}

// NewAwake returns an Awake for this platform.
func NewAwake(log *slog.Logger) *Awake {
	return &Awake{log: log, command: awakeCommand, signals: awakeSignals, stop: stopAwake, allow: allowAwake}
}

// Prepare makes sure the inhibitor will be let hold, before the first job
// needs it: on Linux, that logind lets this user's service hold off sleep.
// When it will not, it says so once, with the fix, and the first job's
// failing hold is not said again.
func (a *Awake) Prepare() {
	if a.allow == nil || a.command == nil {
		return
	}
	if !a.allow(a.log) {
		a.mu.Lock()
		a.failing = true
		a.mu.Unlock()
	}
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
	// What it says when it cannot hold, such as logind's refusal.
	var said bytes.Buffer
	cmd.Stderr = &said
	// Whatever it leaves running when it ends does not keep Wait waiting
	// on a pipe.
	cmd.WaitDelay = time.Second
	var out *bufio.Reader
	if a.signals {
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			a.failed(err, "")
			return
		}
		out = bufio.NewReader(pipe)
	}
	if err := cmd.Start(); err != nil {
		a.failed(err, "")
		return
	}
	done := make(chan struct{})
	go func() {
		// Holds once it says so, or from the start if it never will.
		holds := true
		if out != nil {
			_, err := out.ReadString('\n')
			holds = err == nil
		}
		if holds {
			a.mu.Lock()
			current := a.cmd == cmd
			if current {
				a.holding, a.failing = true, false
			}
			a.mu.Unlock()
			if current {
				a.log.Info("keeping this machine awake while it runs jobs")
			}
		}
		// Reaps it, and says so if it ends while it should be holding.
		err := cmd.Wait()
		a.mu.Lock()
		stopped := a.cmd != cmd
		held := a.holding
		if !stopped {
			a.cmd, a.done, a.holding = nil, nil, false
		}
		a.mu.Unlock()
		switch {
		case stopped:
		case held:
			a.log.Warn("stopped keeping this machine awake while it runs jobs; it may sleep under one", "error", err, "said", strings.TrimSpace(said.String()))
		default:
			a.mu.Lock()
			a.failed(err, said.String())
			a.mu.Unlock()
		}
		close(done)
	}()
	a.cmd, a.done = cmd, done
}

// failed says that the inhibitor could not hold, unless the last one could
// not either and that was said. Called with mu held.
func (a *Awake) failed(err error, said string) {
	log := a.log.Warn
	if a.failing {
		log = a.log.Debug
	}
	a.failing = true
	log("could not keep this machine awake while it runs jobs; it may sleep under one", "error", err, "said", strings.TrimSpace(said))
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
	cmd, done, held := a.cmd, a.done, a.holding
	a.cmd, a.done, a.holding = nil, nil, false
	a.mu.Unlock()
	a.stop(cmd)
	<-done
	if held {
		a.log.Info("no job running; this machine may sleep again")
	}
}
