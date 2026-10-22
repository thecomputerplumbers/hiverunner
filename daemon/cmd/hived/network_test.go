package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/ui"
)

func TestStatusSaysWhatAJobCanReach(t *testing.T) {
	current := settings.Default()
	for _, test := range []struct {
		name     string
		allow    bool
		list     string
		fences   bool
		reaches  string
		fenced   bool
		doctorOK bool
	}{
		{"blocked, on Linux", false, "", true, "the internet only", true, true},
		{"blocked with some allowed", false, "192.168.1.20:5432,10.0.0.0/24", true, "the internet, and 192.168.1.20:5432, 10.0.0.0/24", true, true},
		{"the owner's off switch", true, "", true, "the internet, and " + thisMachine + "'s LAN and tailnet", false, false},
		{"blocked, where nothing fences it yet", false, "", false, "not yet kept off them here", false, false},
	} {
		current.PrivateNetwork, current.AllowNetwork = test.allow, test.list
		state := networkOf(current, test.fences)
		if !strings.Contains(state.reaches(), test.reaches) || state.Fenced != test.fenced {
			t.Errorf("%s: reaches %q, fenced %v", test.name, state.reaches(), state.Fenced)
		}
		check := state.check()
		if check.OK != test.doctorOK || (!check.OK && !check.Note) {
			t.Errorf("%s: doctor says %+v; an open network is the owner's choice or the platform's, never a failure", test.name, check)
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
		if !strings.Contains(text, "private-network") || !strings.Contains(text, "allow-network") || !strings.Contains(text, "/56") {
			t.Errorf("the network settings are not shown:\n%s", text)
		}
		for _, row := range rows {
			if ui.Width(row) > ui.Inner {
				t.Errorf("%d columns, wider than the box: %q", ui.Width(row), row)
			}
		}
	}
}
