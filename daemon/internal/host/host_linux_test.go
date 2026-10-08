//go:build linux

package host

import (
	"os"
	"path/filepath"
	"testing"
)

func supply(t *testing.T, root, name string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, content := range files {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestABoxWithNoBatteryIsNeverOnBattery(t *testing.T) {
	root := t.TempDir()
	if onBattery(root) {
		t.Error("an empty power_supply directory read as on battery")
	}
	// A wireless mouse's battery is not the machine's.
	supply(t, root, "hidpp_battery_0", map[string]string{"type": "Battery", "scope": "Device"})
	if onBattery(root) {
		t.Error("a peripheral's battery read as the machine being on battery")
	}
}

func TestALaptopIsOnBatteryOnlyWhenUnplugged(t *testing.T) {
	root := t.TempDir()
	supply(t, root, "BAT0", map[string]string{"type": "Battery"})
	supply(t, root, "AC", map[string]string{"type": "Mains", "online": "0"})
	if !onBattery(root) {
		t.Error("unplugged laptop read as on mains")
	}
	supply(t, root, "AC", map[string]string{"type": "Mains", "online": "1"})
	if onBattery(root) {
		t.Error("plugged-in laptop read as on battery")
	}
}

func TestTheLid(t *testing.T) {
	root := t.TempDir()
	if lidClosed(root) {
		t.Error("a machine with no lid read as closed")
	}
	supply(t, root, "LID0", map[string]string{"state": "state:      open"})
	if lidClosed(root) {
		t.Error("an open lid read as closed")
	}
	supply(t, root, "LID0", map[string]string{"state": "state:      closed"})
	if !lidClosed(root) {
		t.Error("a closed lid read as open")
	}
}

func TestLogindDecidesWhetherAClosedLidSuspends(t *testing.T) {
	for settings, want := range map[string]bool{
		"":                                   false,
		"[Login]\n#HandleLidSwitch=ignore\n": false,
		"[Login]\nHandleLidSwitch=ignore\n":  true,
		"HandleLidSwitch=ignore\nHandleLidSwitchExternalPower=suspend\n": false,
		"HandleLidSwitch=suspend\nHandleLidSwitchExternalPower=ignore\n": false,
		"HandleLidSwitch=ignore\nHandleLidSwitch=suspend\n":              false,
		"HandleLidSwitch=lock\nHandleLidSwitchExternalPower=ignore\n":    true,
	} {
		if got := lidIgnored(settings); got != want {
			t.Errorf("%q: got %v, want %v", settings, got, want)
		}
	}
}
