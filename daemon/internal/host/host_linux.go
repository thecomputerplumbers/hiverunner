//go:build linux

package host

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

// ReadFacts describes this machine.
func ReadFacts() (Facts, error) {
	var info unix.Sysinfo_t
	if err := unix.Sysinfo(&info); err != nil {
		return Facts{}, err
	}
	model := readModel()
	name, err := os.Hostname()
	if err != nil || name == "" {
		name = model
	}
	return Facts{
		Name:        name,
		Model:       model,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		Cores:       runtime.NumCPU(),
		MemoryBytes: int64(uint64(info.Totalram) * uint64(info.Unit)),
	}, nil
}

// readModel is what the hardware calls itself: the firmware's product name
// on a PC or server, the device tree's on a board. A machine that says
// neither is described by its processor architecture.
func readModel() string {
	for _, file := range []string{
		"/sys/class/dmi/id/product_name",
		"/sys/firmware/devicetree/base/model",
	} {
		raw, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		// The device tree's strings end in a NUL.
		model := strings.TrimSpace(strings.TrimRight(string(raw), "\x00"))
		// What a board says when its maker never filled the field in.
		switch strings.ToLower(model) {
		case "", "to be filled by o.e.m.", "system product name", "default string", "none":
			continue
		}
		if len(model) > 100 {
			model = model[:100]
		}
		return model
	}
	return "Linux " + runtime.GOARCH
}

// ReadPower asks the kernel about power and the lid. A machine with no
// battery and no lid, which is most Linux boxes, is always available.
func ReadPower() Power {
	return Power{OnBattery: onBattery("/sys/class/power_supply"), LidClosed: lidClosed("/proc/acpi/button/lid")}
}

// onBattery is true when the machine has a battery and no mains supply
// reports itself online.
func onBattery(root string) bool {
	supplies, _ := filepath.Glob(filepath.Join(root, "*"))
	battery, mains := false, false
	for _, supply := range supplies {
		kind, _ := os.ReadFile(filepath.Join(supply, "type"))
		switch strings.TrimSpace(string(kind)) {
		case "Battery":
			// A mouse or a UPS is a battery too; only the system's counts.
			scope, _ := os.ReadFile(filepath.Join(supply, "scope"))
			if strings.TrimSpace(string(scope)) != "Device" {
				battery = true
			}
		case "Mains", "USB", "USB_C", "USB_PD":
			online, _ := os.ReadFile(filepath.Join(supply, "online"))
			if strings.TrimSpace(string(online)) == "1" {
				mains = true
			}
		}
	}
	return battery && !mains
}

func lidClosed(root string) bool {
	states, _ := filepath.Glob(filepath.Join(root, "*", "state"))
	for _, state := range states {
		raw, _ := os.ReadFile(state)
		if strings.Contains(string(raw), "closed") {
			return true
		}
	}
	return false
}
