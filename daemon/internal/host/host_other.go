//go:build !darwin

package host

import (
	"errors"
	"runtime"
)

// ErrUnsupported is returned everywhere but macOS. The package still builds
// elsewhere so the rest of the daemon can be tested in CI on Linux.
var ErrUnsupported = errors.New("HiveWorker runs on Apple Silicon Macs for now (this is " + runtime.GOOS + "/" + runtime.GOARCH + ")")

func ReadFacts() (Facts, error) { return Facts{}, ErrUnsupported }

// Unsupported hosts remain unavailable.
func ReadPower() Power { return Power{LidClosed: true} }
