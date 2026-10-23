package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/connection"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/host"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/native"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/ui"
)

func TestDoctorSaysWhatTheRunningDaemonCannotOpen(t *testing.T) {
	// The shell joined the kvm group; the service started before it did.
	denied := "this machine cannot run a VM: your user's services started before your user joined the kvm group, so cannot open /dev/kvm: run `sudo tee /etc/udev/rules.d/99-hiverunner-$USER.rules && sudo setfacl -m u:ada:rw /dev/kvm`"
	daemon := connection.State{Unusable: denied, WrittenBy: os.Getpid()}
	reason, fix := whyNoVMs(nil, daemon)
	if !strings.Contains(reason, "the running daemon") || !strings.Contains(reason, "cannot open /dev/kvm") || !strings.Contains(fix, "99-hiverunner") || !strings.Contains(fix, "setfacl") {
		t.Fatalf("said %q, %q", reason, fix)
	}

	wiped := "this machine cannot run a VM: your udev rule /etc/udev/rules.d/70-hiverunner-seth.rules grants /dev/kvm, but Ubuntu's 73-seat-late.rules (uaccess) runs after it and wipes that ACL when a VM starts: run `sudo rm -f /etc/udev/rules.d/70-hiverunner-$USER.rules`"
	reason, fix = whyNoVMs(nil, connection.State{Unusable: wiped, WrittenBy: os.Getpid()})
	if !strings.Contains(reason, "the running daemon") || !strings.Contains(reason, "70-hiverunner-seth.rules") || !strings.Contains(fix, "rm -f") {
		t.Fatalf("wiped ACL said %q, %q", reason, fix)
	}

	// What this shell finds comes first: it is what the owner can check.
	reason, fix = whyNoVMs(errors.New("there is no /dev/kvm: turn on virtualization"), daemon)
	if reason != "there is no /dev/kvm" || fix != "turn on virtualization" {
		t.Fatalf("said %q, %q", reason, fix)
	}

	// A daemon that has stopped said what was true then, not now.
	gone := connection.State{Unusable: denied, WrittenBy: 1 << 30}
	if reason, _ := whyNoVMs(nil, gone); reason != "" {
		t.Fatalf("a stopped daemon's word was taken: %q", reason)
	}
	if reason, _ := whyNoVMs(nil, connection.State{WrittenBy: os.Getpid()}); reason != "" {
		t.Fatalf("a daemon that can run VMs was said not to: %q", reason)
	}
}

func TestDoctorSaysHowExposedAMacIsWithoutFailing(t *testing.T) {
	on := macOSJobs(settings.Settings{MacOSJobs: true}, "darwin", false, "", "")
	if len(on) != 1 || on[0].OK || !on[0].Note || !strings.Contains(on[0].Found, "hiveworker") || !strings.Contains(on[0].Fix, "macos-jobs off") {
		t.Fatalf("macOS jobs on said %+v", on)
	}
	if off := macOSJobs(settings.Settings{}, "darwin", false, "", ""); len(off) != 1 || !off[0].OK {
		t.Fatalf("macOS jobs off said %+v", off)
	}
	// A job in a throwaway VM of its own leaves nothing for the next.
	if vm := macOSJobs(settings.Settings{MacOSJobs: true}, "darwin", true, "/Users/ada", "readable by everyone"); len(vm) != 1 || !vm[0].OK || vm[0].Note || strings.Contains(vm[0].Found, "hiveworker") {
		t.Fatalf("macOS jobs in VMs said %+v", vm)
	}
	if linux := macOSJobs(settings.Settings{MacOSJobs: true}, "linux", false, "/home/ada", "readable by everyone"); len(linux) != 0 {
		t.Fatalf("a Linux machine was told about macOS jobs: %+v", linux)
	}

	// A home folder native.User can see into is said, with how to close it,
	// and fails nothing.
	open := macOSJobs(settings.Settings{MacOSJobs: true}, "darwin", false, "/Users/ada", "readable by its group, which hiveworker is in")
	if len(open) != 2 || open[1].OK || !open[1].Note || !strings.Contains(open[1].Found, "/Users/ada is readable by its group") || open[1].Fix != "chmod 700 ~" {
		t.Fatalf("an open home folder said %+v", open)
	}

	// What is said and not counted shows its fix, and makes nothing exit 1.
	// In the Security box it is drawn as the warning it is.
	notes := append(on, host.Check{Name: "firewall", Found: "off", Note: true, Fix: "turn it on"}, host.Check{Name: "ssh", Found: "keys only", OK: true})
	rows, problems := checkRows(ui.Screen{}, notes, false)
	if problems != 0 || !strings.Contains(strings.Join(rows, "\n"), "turn it on") {
		t.Fatalf("notes counted %d, drew %q", problems, rows)
	}
	if strings.Count(strings.Join(rows, "\n"), "○") != 2 || strings.Count(strings.Join(rows, "\n"), "●") != 1 {
		t.Fatalf("warnings drawn as passes: %q", rows)
	}
	if rows, _ := checkRows(ui.Screen{}, notes, true); strings.Contains(strings.Join(rows, "\n"), "○") {
		t.Fatalf("FileVault's kind of note drawn as a problem: %q", rows)
	}
	if _, problems := checkRows(ui.Screen{}, []host.Check{{Name: "sleep", Found: "on", Fix: "sudo pmset -a disablesleep 1"}}, true); problems != 1 {
		t.Fatalf("a problem counted %d", problems)
	}
}

func TestDoctorSaysWhetherSpotlightIndexesMacOSJobsWithoutFailing(t *testing.T) {
	moved, notYet := true, false
	volume := "/System/Volumes/Data"
	for _, test := range []struct {
		report native.SpotlightReport
		ok     bool
		says   string
	}{
		{native.SpotlightReport{Volume: volume, Indexing: "off", NoIndex: true}, true, "not indexing"},
		{native.SpotlightReport{Volume: volume, Indexing: "", NoIndex: true}, false, "mdutil -s " + volume},
		{native.SpotlightReport{Volume: volume, Indexing: "on"}, false, "spotlight-exclude on"},
		{native.SpotlightReport{Volume: volume, Indexing: "on", NoIndex: true, Moved: &notYet}, false, "when no job is running"},
		{native.SpotlightReport{Volume: volume, Indexing: "on", NoIndex: true, Moved: &moved}, false, "Search Privacy"},
		{native.SpotlightReport{Volume: volume, Indexing: "on", NoIndex: true}, false, "hive.noindex"},
	} {
		checks := spotlightChecks(test.report)
		if len(checks) != 1 || checks[0].Name != "spotlight" || checks[0].OK != test.ok || !test.ok && !checks[0].Note {
			t.Fatalf("%+v said %+v", test.report, checks)
		}
		if said := checks[0].Found + " " + checks[0].Fix; !strings.Contains(said, test.says) {
			t.Errorf("%+v said %q, not %q", test.report, said, test.says)
		}
		if rows, problems := checkRows(ui.Screen{}, checks, true); problems != 0 {
			t.Errorf("%+v made doctor fail: %q", test.report, rows)
		}
	}
	if checks := spotlight(settings.Settings{MacOSJobs: true, SpotlightExclude: true}, "linux", false); checks != nil {
		t.Fatalf("a Linux machine was told about Spotlight: %+v", checks)
	}
	if checks := spotlight(settings.Settings{MacOSJobs: true}, "darwin", true); checks != nil {
		t.Fatalf("a Mac running macOS jobs in VMs was told about its own Spotlight: %+v", checks)
	}
	if checks := spotlight(settings.Settings{}, "darwin", false); checks != nil {
		t.Fatalf("a Mac taking no macOS jobs was told about Spotlight: %+v", checks)
	}
}
