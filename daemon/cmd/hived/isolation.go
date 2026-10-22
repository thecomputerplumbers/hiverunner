package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/jobs"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/macvm"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/native"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
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
		runner.machines = &macvm.Runner{Dir: dir, WorkDir: workDir(dir), Log: log, HostMemory: hostMemory()}
		runner.vm = runner.machines
	}
	return runner
}
