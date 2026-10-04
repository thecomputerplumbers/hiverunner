//go:build darwin

package host

import (
	"os"
	"os/exec"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

// ReadFacts describes this Mac.
func ReadFacts() (Facts, error) {
	model, err := unix.Sysctl("hw.model")
	if err != nil {
		return Facts{}, err
	}
	memory, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return Facts{}, err
	}
	name, err := os.Hostname()
	if err != nil || name == "" {
		name = model
	}
	return Facts{
		Name:        strings.TrimSuffix(name, ".local"),
		Model:       model,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		Cores:       runtime.NumCPU(),
		MemoryBytes: int64(memory),
	}, nil
}

// ReadPower asks macOS about power and the lid. An unreadable lid pauses jobs.
func ReadPower() Power {
	battery, err := exec.Command("/usr/bin/pmset", "-g", "batt").Output()
	onBattery := err != nil || !strings.Contains(string(battery), "AC Power")

	// Desktops have no clamshell key at all, which reads as "not closed".
	lid, err := exec.Command("/usr/sbin/ioreg", "-r", "-k", "AppleClamshellState", "-d", "4").Output()
	lidClosed := err != nil || strings.Contains(string(lid), `"AppleClamshellState" = Yes`)

	return Power{OnBattery: onBattery, LidClosed: lidClosed}
}
