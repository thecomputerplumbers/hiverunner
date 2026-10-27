//go:build linux

package host

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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

// AvailableMemory is what the kernel says can be given to a new program
// without swapping: MemAvailable. Zero means it could not be read.
func AvailableMemory() uint64 { return memAvailable("/proc/meminfo") }

func memAvailable(path string) uint64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		rest, ok := strings.CutPrefix(line, "MemAvailable:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0
		}
		kb, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0
		}
		return kb << 10
	}
	return 0
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
	lids, _ := filepath.Glob("/proc/acpi/button/lid/*/state")
	return Power{OnBattery: onBattery("/sys/class/power_supply"), LidClosed: lidClosed("/proc/acpi/button/lid"), HasLid: len(lids) > 0}
}

// StayAwake is how the owner keeps this machine awake with its lid closed.
const StayAwake = "set HandleLidSwitch and HandleLidSwitchExternalPower to ignore in /etc/systemd/logind.conf, then restart systemd-logind"

// StaysAwake reports whether this machine stays awake with its lid closed,
// which is logind's decision. known is false when logind's settings could
// not be read.
func StaysAwake() (awake, known bool) {
	settings, err := exec.Command("systemd-analyze", "cat-config", "systemd/logind.conf").Output()
	if err != nil {
		return false, false
	}
	return lidIgnored(string(settings)), true
}

// lidIgnored reads logind's settings, later lines winning as they do for
// logind. Unset, a closed lid suspends, and on mains it does what it does on
// battery.
func lidIgnored(settings string) bool {
	battery, mains := "suspend", ""
	for _, line := range strings.Split(settings, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "HandleLidSwitch":
			battery = strings.TrimSpace(value)
		case "HandleLidSwitchExternalPower":
			mains = strings.TrimSpace(value)
		}
	}
	if mains == "" {
		mains = battery
	}
	awake := func(action string) bool { return action == "ignore" || action == "lock" }
	return awake(battery) && awake(mains)
}

// Unattended looks at what stops this machine taking jobs with its lid
// closed or after a restart nobody was there for.
func Unattended() []Check {
	var checks []Check
	if ReadPower().HasLid {
		switch awake, known := StaysAwake(); {
		case !known:
			checks = append(checks, Check{Name: "sleep", Found: "logind's settings could not be read", Fix: StayAwake})
		case awake:
			checks = append(checks, Check{Name: "sleep", Found: "closing the lid does not suspend it", OK: true})
		default:
			checks = append(checks, Check{Name: "sleep", Found: "suspends when its lid closes", Fix: StayAwake})
		}
	}
	// A machine that sleeps under a job loses it, and logind holds off
	// sleep for a user service only if polkit is told to.
	switch allowed, known := awakeAllowed(); {
	case allowed:
		checks = append(checks, Check{Name: "kept awake", Found: "hived keeps it from sleeping while it runs a job", OK: true})
	case known:
		fix := awakeFix(os.Getenv("USER"))
		if fix == "" {
			fix = "allow your user org.freedesktop.login1.inhibit-block-sleep in a polkit rule"
		}
		checks = append(checks, Check{Name: "kept awake", Found: "polkit refuses hived a lock against sleep, so it can sleep under a job", Fix: fix})
	}
	// hived is a user service: without lingering it starts when its user
	// logs in, not at boot.
	user := os.Getenv("USER")
	linger, err := exec.Command("loginctl", "show-user", user, "--property=Linger").Output()
	if err == nil && strings.TrimSpace(string(linger)) == "Linger=yes" {
		checks = append(checks, Check{Name: "start at boot", Found: "lingering is on, so hived starts without a login", OK: true})
	} else {
		checks = append(checks, Check{Name: "start at boot", Found: "after a restart hived waits for " + user + " to log in", Fix: "sudo loginctl enable-linger " + user})
	}
	return checks
}

// Exposed looks at what this machine leaves open to the jobs it runs and to
// its network.
func Exposed(e Exposure) []Check { return local(e.Sudo).linux(e.Fence) }

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
