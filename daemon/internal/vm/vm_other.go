//go:build !darwin && !linux

package vm

import (
	"context"
	"errors"
	"log/slog"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/fence"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/jobs"
)

// FencesNetwork is false where nothing runs.
const FencesNetwork = false

// Runner is unavailable where there is neither Apple's hypervisor nor KVM.
// The type exists so the rest of the daemon still builds there.
type Runner struct {
	Image     Image
	WorkDir   string
	Log       *slog.Logger
	WarmVCPUs int
	Guest     *GuestService
	// OnUnusable is never called here: nothing here runs.
	OnUnusable func(reason string)
	// Network and PublicDNS are never asked here.
	Network   func() fence.Policy
	PublicDNS func() bool
}

var errUnsupported = errors.New("the VM runner needs macOS or Linux")

func (r *Runner) Run(context.Context, jobs.Job) (jobs.Result, error) {
	return jobs.Result{ExitCode: -1}, errUnsupported
}

func (r *Runner) SelfTest(context.Context, int) (string, error) { return "", errUnsupported }

func (r *Runner) Tend(context.Context, func() bool) {}

func (r *Runner) Unusable() string { return "" }

// Preflight reports why this host cannot run VMs.
func Preflight() error { return errUnsupported }
