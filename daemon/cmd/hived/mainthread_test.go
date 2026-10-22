package main

import (
	"strings"
	"testing"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
)

func TestOnlyImageOpenHoldsTheMainThread(t *testing.T) {
	for args, want := range map[string]bool{
		"hived macos image open":                true,
		"hived macos image open --share /Xcode": true,
		"hived run":                             false,
		"hived macos image seal":                false,
		"hived macos":                           false,
		"hived":                                 false,
	} {
		if got := opensWindow(strings.Fields(args)); got != want {
			t.Errorf("%q: %v, want %v", args, got, want)
		}
	}
}

func TestAMacPlansItsMemoryOnlyWhereItMatters(t *testing.T) {
	const host = 16 << 30
	vm := settings.Default()
	vm.MacOSIsolation = settings.IsolationVM
	reserved := settings.Default()
	reserved.ReserveMemory = "4G"
	for name, c := range map[string]struct {
		current   settings.Settings
		available uint64
		want      uint64
	}{
		"Linux reads what is free":       {vm, 8 << 30, 0},
		"a Mac with macOS VMs":           {vm, 0, host},
		"a Mac with a reserve":           {reserved, 0, host},
		"a Mac lending Linux jobs alone": {settings.Default(), 0, 0},
	} {
		if got := plannedMemory(c.current, c.available, host); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
}
