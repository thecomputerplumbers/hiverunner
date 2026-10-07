//go:build !darwin && !linux

package vm

import (
	"context"
	"errors"
	"log/slog"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/jobs"
)

// Runner is unavailable where there is neither Apple's hypervisor nor KVM.
// The type exists so the rest of the daemon still builds there.
type Runner struct {
	Image     Image
	WorkDir   string
	Log       *slog.Logger
	WarmVCPUs int
	Guest     *GuestService
}

var errUnsupported = errors.New("the VM runner needs macOS or Linux")

func (r *Runner) Run(context.Context, jobs.Job) (int, error) { return -1, errUnsupported }

func (r *Runner) SelfTest(context.Context, int) (string, error) { return "", errUnsupported }

func (r *Runner) Tend(context.Context, func() bool) {}

func (r *Runner) Unusable() string { return "" }

// Preflight reports why this host cannot run VMs.
func Preflight() error { return errUnsupported }
