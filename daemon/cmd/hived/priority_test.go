package main

import (
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/host"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
)

// The launch agent as release/install.sh writes it.
const agent = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.thecomputerplumbers.hiveworker</string>
	<key>ThrottleInterval</key>
	<integer>30</integer>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>/Users/ada/Library/Logs/HiveWorker/hived.log</string>
</dict>
</plist>
`

// Only the ProcessType changes, so the installer, writing the same agent
// for the same setting, finds nothing new and leaves the daemon running.
func TestTheAgentsPriorityIsChangedAndNothingElse(t *testing.T) {
	standard, ok := withProcessType(agent, "Standard")
	if !ok || standard != strings.Replace(agent, "<string>Background</string>", "<string>Standard</string>", 1) {
		t.Fatalf("ok %v, wrote %s", ok, standard)
	}
	if back, _ := withProcessType(standard, "Background"); back != agent {
		t.Fatalf("back again: %s", back)
	}
	if _, ok := withProcessType("<plist><dict></dict></plist>", "Standard"); ok {
		t.Fatal("changed an agent with no ProcessType")
	}
	// The installer's heredoc names the same key and the same values.
	raw, err := os.ReadFile("../../release/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<key>ProcessType</key>\n\t<string>$process_type</string>", "process_type=Background", "process_type=Standard", "config get priority"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("install.sh lacks %q", want)
		}
	}
}

func TestSpawnBackground(t *testing.T) {
	printed := "gui/501/com.thecomputerplumbers.hiveworker = {\n\tactive count = 1\n\tpid = 4321\n\tspawn type = background (5)\n}\n"
	if background, pid := spawnBackground(printed); !background || pid != 4321 {
		t.Fatalf("background %v, pid %d", background, pid)
	}
	if background, pid := spawnBackground("\tspawn type = daemon (3)\n"); background || pid != 0 {
		t.Fatalf("standard read as background %v, pid %d", background, pid)
	}
}

func TestPriorityCheck(t *testing.T) {
	plugged := host.Power{HasLid: true}
	build := settings.Settings{Lid: true}
	// A Mac kept for building is told how to use every core, and fails
	// nothing for it.
	if check := priorityCheck(build, true, true, plugged); check.OK || !check.Note || check.Fix != "for a Mac kept for building: hived config set priority standard" {
		t.Fatalf("a build Mac: %+v", check)
	}
	if check := priorityCheck(settings.Settings{}, true, true, host.Power{}); check.OK {
		t.Fatalf("a desktop, plugged in: %+v", check)
	}
	// A laptop lending idle time is as it should be.
	for name, power := range map[string]host.Power{"on battery": {HasLid: true, OnBattery: true}, "lid off": plugged} {
		current := settings.Settings{}
		if name == "on battery" {
			current.Lid = true
		}
		if check := priorityCheck(current, true, true, power); !check.OK || !strings.HasPrefix(check.Found, "background") {
			t.Fatalf("%s: %+v", name, check)
		}
	}
	standard := settings.Settings{Priority: settings.PriorityStandard}
	if check := priorityCheck(standard, false, true, plugged); !check.OK || !strings.HasPrefix(check.Found, "standard") {
		t.Fatalf("standard: %+v", check)
	}
	// Chosen, and not yet taken up.
	if check := priorityCheck(standard, true, true, plugged); !check.OK || check.Found != "standard, once the daemon starts again, when it has no job" {
		t.Fatalf("pending: %+v", check)
	}
	// No running service to ask: the setting is what it will be.
	if check := priorityCheck(standard, false, false, plugged); !strings.HasPrefix(check.Found, "standard") {
		t.Fatalf("not running: %+v", check)
	}
}

// The daemon stops for its priority only with no job in hand, and stops
// taking jobs first.
func TestRestartWhenIdle(t *testing.T) {
	var updating atomic.Bool
	if restartWhenIdle(func() bool { return true }, &updating) || updating.Load() {
		t.Fatal("stopped with a job")
	}
	updating.Store(true)
	if restartWhenIdle(func() bool { return false }, &updating) {
		t.Fatal("stopped under an update")
	}
	updating.Store(false)
	calls := 0
	if restartWhenIdle(func() bool { calls++; return calls > 1 }, &updating) || updating.Load() {
		t.Fatal("stopped with a job accepted as it went")
	}
	if !restartWhenIdle(func() bool { return false }, &updating) || !updating.Load() {
		t.Fatal("did not stop, idle")
	}
}
