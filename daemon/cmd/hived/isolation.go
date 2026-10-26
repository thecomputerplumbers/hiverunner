package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/jobs"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/macvm"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/native"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/update"
)

// macosWay is one way of running a hive-macos job.
type macosWay interface {
	jobs.Runner
	jobs.Unusable
}

// macosRunner runs hive-macos jobs the way the owner's macos-isolation
// setting says: as the hiveworker user on the Mac itself, or in a throwaway
// macOS VM. The way is chosen when a job is offered (jobs.Chooser), and
// that is the way it runs, whatever the setting says by the time it starts.
// Either way may be missing, with the reason it is.
type macosRunner struct {
	user, vm               macosWay
	userMissing, vmMissing string
	settings               func() settings.Settings
	// machines is the VM way itself, for claiming its work directory.
	machines *macvm.Runner
}

var _ macosWay = (*macosRunner)(nil)
var _ jobs.Chooser = (*macosRunner)(nil)

func (m *macosRunner) way() (macosWay, string) {
	if m.settings().MacOSInVM() {
		return m.vm, m.vmMissing
	}
	return m.user, m.userMissing
}

// Choose implements jobs.Chooser.
func (m *macosRunner) Choose() jobs.Runner {
	way, missing := m.way()
	if way == nil {
		return missingWay(missing)
	}
	return way
}

// Run implements jobs.Runner, for a job no way was chosen for: the way the
// setting says now.
func (m *macosRunner) Run(ctx context.Context, job jobs.Job) (jobs.Result, error) {
	return m.Choose().Run(ctx, job)
}

// Unusable implements jobs.Unusable.
func (m *macosRunner) Unusable() string {
	return m.Choose().(jobs.Unusable).Unusable()
}

// missingWay is a way that is not there, and says why.
type missingWay string

func (w missingWay) Run(context.Context, jobs.Job) (jobs.Result, error) {
	return jobs.Result{ExitCode: -1}, errors.New(string(w))
}

func (w missingWay) Unusable() string { return string(w) }

// claim makes the macOS VMs' work directory this daemon's and clears what
// one that died left there, whichever way jobs run now: a clone left behind
// can hold what its job wrote.
func (m *macosRunner) claim() error {
	if m.machines == nil {
		return nil
	}
	return m.machines.Claim()
}

// newMacOSRunner builds both ways, for a daemon that may be asked either.
func newMacOSRunner(log *slog.Logger, live func() settings.Settings) *macosRunner {
	runner := &macosRunner{settings: live}
	if user, err := native.New(log); err != nil {
		log.Warn("macOS jobs are not available", "error", err)
		runner.userMissing = "macOS jobs are not available: " + err.Error()
	} else {
		runner.user = user
	}
	if dir, err := macosImageDir(live()); err != nil {
		runner.vmMissing = "the macOS image is not available: " + err.Error()
	} else {
		runner.machines = &macvm.Runner{Dir: dir, WorkDir: workDir(dir), Log: log, HostMemory: hostMemory(),
			Most: func() int { return live().MacOSVMs() }}
		runner.vm = runner.machines
	}
	return runner
}

// keep has the hiveworker way keep what it runs up to date, with no sudo:
// see native.Runner.Keep. A download that fails, offline say, is tried
// again a quarter of an hour later, until it is done or the daemon stops.
func (m *macosRunner) keep(ctx context.Context, log *slog.Logger) {
	user, ok := m.user.(*native.Runner)
	if !ok {
		return
	}
	for {
		err := user.Keep(ctx)
		if err == nil || ctx.Err() != nil {
			return
		}
		log.Warn("could not bring what macOS jobs run up to date; trying again in 15 minutes", "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Minute):
		}
	}
}

// notice is what the log last said about whether this Mac takes the macOS
// jobs its owner turned on, so that each change is said once.
type notice struct {
	mu    sync.Mutex
	said  string
	heard bool
}

// say logs reason, why macOS jobs are not taken, if it is not what was said
// last; "" is that they are.
func (n *notice) say(log *slog.Logger, reason string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.heard && n.said == reason {
		return
	}
	first := !n.heard
	n.heard, n.said = true, reason
	switch {
	case reason != "":
		log.Warn("macos-jobs is on, but this Mac cannot take macOS jobs", "reason", reason)
	case !first:
		log.Info("this Mac takes macOS jobs again")
	}
}

// holdForSetup is why an update to latest waits for the owner: a Mac that
// takes macOS jobs as native.User now would take none after it until they
// ran hived macos setup again, which needs their password (#130). "" when
// it need not wait.
func holdForSetup(current settings.Settings, takes bool, latest update.Latest) string {
	if !takes || current.MacOSInVM() || !native.NeedsSetup(latest.MacOSSetup) {
		return ""
	}
	return "hived " + latest.Version + " takes macOS jobs only once hived macos setup has run again; run: hived update, then hived macos setup"
}
