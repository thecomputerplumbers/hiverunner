package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/host"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/ui"
)

func TestStatusSaysWhatAJobCanReach(t *testing.T) {
	for _, test := range []struct {
		name      string
		isolation string
		list      string
		fences    bool
		reaches   string
		fenced    bool
		fence     host.Fence
	}{
		{"blocked, on Linux", "block", "", true, "the internet only", true, host.Fence{On: true}},
		{"blocked with some allowed", "block", "192.168.1.20:5432,10.0.0.0/24", true, "the internet, and 192.168.1.20:5432, 10.0.0.0/24", true,
			host.Fence{On: true, Allow: []string{"192.168.1.20:5432", "10.0.0.0/24"}}},
		{"the default", "", "", true, "LAN, not its tailnet", true, host.Fence{On: true, LAN: true}},
		{"lan-allow", "lan-allow", "", true, "LAN, not its tailnet", true, host.Fence{On: true, LAN: true}},
		{"the owner's off switch", "off", "", true, "LAN, tailnet and " + thisMachine + " itself", false, host.Fence{}},
		{"blocked, where nothing fences it yet", "block", "", false, "not yet kept off them here", false, host.Fence{}},
		{"blocked, and all but opened", "block", "10.0.0.0/8,0.0.0.0/1,128.0.0.0/1", true, "opens nearly everything (0.0.0.0/1, 128.0.0.0/1)", true, host.Fence{}},
	} {
		current := settings.Default()
		current.NetworkIsolation, current.AllowNetwork = test.isolation, test.list
		state := networkOf(current, test.fences)
		if !strings.Contains(state.reaches(), test.reaches) || state.Fenced != test.fenced || state.Chosen != (test.isolation != "") {
			t.Errorf("%s: reaches %q, fenced %v", test.name, state.reaches(), state.Fenced)
		}
		got := state.fenced()
		if got.On != test.fence.On || got.LAN != test.fence.LAN || strings.Join(got.Allow, ",") != strings.Join(test.fence.Allow, ",") {
			t.Errorf("%s: doctor is told %+v", test.name, got)
		}
		// Open where hived could keep jobs off is said with why and how.
		if !got.On && test.fences && (got.Why == "" || got.Fix == "") {
			t.Errorf("%s: open with no why or fix: %+v", test.name, got)
		}
		for _, row := range networkRows(ui.Screen{}, state) {
			if ui.Width(row) > ui.Inner {
				t.Errorf("%s: %d columns, wider than the box: %q", test.name, ui.Width(row), row)
			}
		}
	}
}

func TestEverySettingFitsTheBox(t *testing.T) {
	t.Setenv("HIVE_CONFIG", filepath.Join(t.TempDir(), "machine.json"))
	current := settings.Default()
	current.CacheGB = 40
	for _, list := range []string{"", "10.0.0.0/8", "192.168.1.20:5432,10.0.0.0/24,[fd00::5]:80,[fd12:3456:789a::/48]:8443,172.16.0.0/12"} {
		current.AllowNetwork = list
		rows := settingRows(ui.Screen{}, current)
		text := strings.Join(rows, "\n")
		if !strings.Contains(text, "network-isolation") || !strings.Contains(text, "dns") || !strings.Contains(text, "allow-network") || !strings.Contains(text, "/56") {
			t.Errorf("the network settings are not shown:\n%s", text)
		}
		for _, row := range rows {
			if ui.Width(row) > ui.Inner {
				t.Errorf("%d columns, wider than the box: %q", ui.Width(row), row)
			}
		}
	}
}
