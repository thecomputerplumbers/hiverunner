//go:build !darwin || !arm64

package macvm

import (
	"context"
	"errors"
)

var errNotAppleSilicon = errors.New("macOS VMs run only on an Apple Silicon Mac")

func platformProblem() string { return errNotAppleSilicon.Error() }

func start(*instance) error { return errNotAppleSilicon }

// Create is for Apple Silicon Macs.
func Create(context.Context, CreateOptions) error { return errNotAppleSilicon }

// Open is for Apple Silicon Macs.
func Open(context.Context, OpenOptions) error { return errNotAppleSilicon }
