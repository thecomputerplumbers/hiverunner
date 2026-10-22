//go:build darwin || linux

package vm

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestARunnerDeclinesJobsWhileTheMachineCannotStartAVM(t *testing.T) {
	var log bytes.Buffer
	var wrong error
	r := &Runner{Image: Image{ImageVersion: MinImageVersion}, Log: slog.New(slog.NewTextHandler(&log, nil)), preflight: func() error { return wrong }}
	if reason := r.Unusable(); reason != "" {
		t.Fatalf("a machine that can start a VM declined: %s", reason)
	}
	// /dev/kvm changes hands under a running daemon.
	wrong = errors.New("/dev/kvm belongs to group render, not kvm: run `sudo chgrp kvm /dev/kvm`")
	for range 3 {
		if reason := r.Unusable(); !strings.Contains(reason, "belongs to group render") {
			t.Fatalf("got %q", reason)
		}
	}
	// And is put back, with no restart.
	wrong = nil
	if reason := r.Unusable(); reason != "" {
		t.Fatalf("still declining once it could: %s", reason)
	}
	// Said when it changes, not each time it is asked.
	if said := strings.Count(log.String(), "declining jobs"); said != 1 {
		t.Errorf("said %d times that it was declining:\n%s", said, log.String())
	}
	if !strings.Contains(log.String(), "can run VMs again") {
		t.Errorf("did not say it had recovered:\n%s", log.String())
	}
}
