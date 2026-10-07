//go:build !darwin

package vm

import (
	"context"
	"errors"
	"log/slog"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/jobs"
)

// Runner is unavailable off macOS. The type exists so the rest of the daemon
// builds and tests in CI on Linux.
type Runner struct {
	Image     Image
	WorkDir   string
	Log       *slog.Logger
	WarmVCPUs int
	Guest     *GuestService
}

var errUnsupported = errors.New("the VM runner needs Apple's Virtualization.framework")

func (r *Runner) Run(context.Context, jobs.Job) (int, error) { return -1, errUnsupported }

func (r *Runner) SelfTest(context.Context, int) (string, error) { return "", errUnsupported }

func (r *Runner) Tend(context.Context, func() bool) {}

func (r *Runner) Unusable() string { return "" }
