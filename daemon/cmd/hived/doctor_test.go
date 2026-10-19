package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/connection"
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
