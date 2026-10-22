//go:build !darwin && !linux

package host

import (
	"errors"
	"runtime"
)

// ErrUnsupported is returned everywhere but macOS and Linux. The package
// still builds elsewhere so the rest of the daemon does.
var ErrUnsupported = errors.New("Hive Runner runs on macOS and Linux (this is " + runtime.GOOS + "/" + runtime.GOARCH + ")")

func ReadFacts() (Facts, error) { return Facts{}, ErrUnsupported }

// Unsupported hosts remain unavailable.
func ReadPower() Power { return Power{LidClosed: true} }

const StayAwake = ""

func StaysAwake() (awake, known bool) { return false, false }

func Unattended() []Check { return nil }

func Exposed(Exposure) []Check { return nil }

// AvailableMemory is unknown here: zero, so nothing is declined for it.
func AvailableMemory() uint64 { return 0 }
