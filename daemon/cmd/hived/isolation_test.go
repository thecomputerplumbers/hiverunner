package main

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/jobs"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/macvm"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/native"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/update"
)

// way is a way of running macOS jobs that says which it is.
type way struct {
	name, unusable string
}

func (w way) Run(context.Context, jobs.Job) (jobs.Result, error) {
	return jobs.Result{Detail: w.name}, nil
}

func (w way) Unusable() string { return w.unusable }

// The owner's setting picks the way each job runs while the daemon runs.
func TestMacOSIsolationPicksTheWayAJobRuns(t *testing.T) {
	current := settings.Default()
	runner := &macosRunner{
		user:     way{name: "user"},
		vm:       way{name: "vm", unusable: "the macOS image is not sealed yet"},
		settings: func() settings.Settings { return current },
	}
	result, err := runner.Run(context.Background(), jobs.Job{})
	if err != nil || result.Detail != "user" || runner.Unusable() != "" {
		t.Fatalf("by default: ran %q, %v, unusable %q", result.Detail, err, runner.Unusable())
	}

	current.MacOSIsolation = settings.IsolationVM
	if got := runner.Unusable(); got != "the macOS image is not sealed yet" {
		t.Errorf("in a VM: unusable %q", got)
	}
	result, _ = runner.Run(context.Background(), jobs.Job{})
	if result.Detail != "vm" {
		t.Errorf("in a VM: ran %q", result.Detail)
	}
}

func TestAMissingWaySaysWhy(t *testing.T) {
	current := settings.Default()
	current.MacOSIsolation = settings.IsolationVM
	runner := &macosRunner{user: way{name: "user"}, vmMissing: "the macOS image is not available", settings: func() settings.Settings { return current }}
	if got := runner.Unusable(); got != "the macOS image is not available" {
		t.Errorf("Unusable() = %q", got)
	}
	if _, err := runner.Run(context.Background(), jobs.Job{}); err == nil || err.Error() != "the macOS image is not available" {
		t.Errorf("Run() = %v", err)
	}
}

// Turning macOS jobs on with macos-isolation vm needs a sealed image, and
// status says that is what is missing.
func TestMacOSJobsInAVMWaitForASealedImage(t *testing.T) {
	current, _, _ := aMachine(t)
	if err := current.Set("macos-isolation", "vm"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(current); err != nil {
		t.Fatal(err)
	}
	if err := configure([]string{"set", "macos-jobs", "on"}); err == nil || !strings.Contains(err.Error(), "no macOS image") {
		t.Fatalf("macos-jobs on with no image: %v", err)
	}
	if after, _ := settings.Load(); after.MacOSJobs {
		t.Fatal("macos-jobs was turned on")
	}
	state := macosState(current)
	if state.Takes || !strings.Contains(state.Reason, "hived macos image") {
		t.Fatalf("status: %+v", state)
	}
	if err := macosImage(context.Background(), []string{"create"}); err == nil || !strings.Contains(err.Error(), "--ipsw") {
		t.Fatalf("create with no IPSW: %v", err)
	}
	if err := macosImage(context.Background(), []string{"seal"}); !errors.Is(err, macvm.ErrNoImage) {
		t.Fatalf("seal with no image: %v", err)
	}
}

// The image lives beside the Linux one, so a data-dir that names another
// disk takes both.
func TestTheMacOSImageIsKeptWithTheData(t *testing.T) {
	current, image, _ := aMachine(t)
	dir, err := macosImageDir(current)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(filepath.Dir(image), "macos-image") {
		t.Fatalf("the macOS image is in %s, the Linux one in %s", dir, image)
	}
}

// A job accepted for a VM runs in one even if the owner switches to the
// hiveworker user before it starts; the manager runs what Choose gave it.
func TestTheWayIsChosenWhenTheJobIsOffered(t *testing.T) {
	current := settings.Default()
	current.MacOSIsolation = settings.IsolationVM
	runner := &macosRunner{user: way{name: "user"}, vm: way{name: "vm"}, settings: func() settings.Settings { return current }}
	chosen := runner.Choose()
	current.MacOSIsolation = ""
	result, _ := chosen.Run(context.Background(), jobs.Job{})
	if result.Detail != "vm" {
		t.Fatalf("a job chosen for a VM ran as %q", result.Detail)
	}
	if again, _ := runner.Choose().Run(context.Background(), jobs.Job{}); again.Detail != "user" {
		t.Fatalf("the next job ran as %q", again.Detail)
	}
}

// Auto-update waits for the owner only where it would stop macOS jobs this
// Mac takes now as native.User (#130).
func TestAnUpdateWaitsForSetupOnlyWhereItWouldStopMacOSJobs(t *testing.T) {
	newer := update.Latest{Version: "9.9.9", MacOSSetup: native.SetupVersion + 1}
	same := update.Latest{Version: "9.9.9", MacOSSetup: native.SetupVersion}
	user := settings.Settings{MacOSJobs: true}
	vm := settings.Settings{MacOSJobs: true, MacOSIsolation: settings.IsolationVM}
	if why := holdForSetup(user, true, newer); !strings.Contains(why, "hived macos setup") {
		t.Fatalf("a Mac taking macOS jobs updates to one needing setup: %q", why)
	}
	for name, why := range map[string]string{
		"not taking macOS jobs": holdForSetup(user, false, newer),
		"in VMs":                holdForSetup(vm, true, newer),
		"no new setup":          holdForSetup(user, true, same),
		"an older manifest":     holdForSetup(user, true, update.Latest{Version: "9.9.9"}),
	} {
		if why != "" {
			t.Errorf("%s: held, %q", name, why)
		}
	}
}

// Each change in whether macOS jobs are taken is said once: stopping is a
// warning, not silence, and starting again is said too.
func TestTheLogSaysWhenMacOSJobsStopAndStart(t *testing.T) {
	var said strings.Builder
	log := slog.New(slog.NewTextHandler(&said, nil))
	var n notice
	for _, reason := range []string{"", "", "needs setup", "needs setup", "", ""} {
		n.say(log, reason)
	}
	out := said.String()
	if strings.Count(out, "level=WARN") != 1 || !strings.Contains(out, `reason="needs setup"`) || strings.Count(out, "takes macOS jobs again") != 1 {
		t.Fatalf("logged %q", out)
	}
}
