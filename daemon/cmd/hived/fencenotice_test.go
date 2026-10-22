package main

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/vm"
)

func TestAnOwnerWhoHasNotChosenIsToldOnceWhatTheFenceChanged(t *testing.T) {
	t.Setenv("HIVE_CONFIG", filepath.Join(t.TempDir(), "machine.json"))
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	fenceNotice(log, settings.Default())
	if vm.FencesNetwork != strings.Contains(logged.String(), "level=WARN") {
		t.Fatalf("first start, fenced %v, logged %q", vm.FencesNetwork, logged.String())
	}
	if vm.FencesNetwork && (!strings.Contains(logged.String(), "network-isolation block") || !strings.Contains(logged.String(), "network-isolation off")) {
		t.Errorf("the notice does not say how to choose: %q", logged.String())
	}
	logged.Reset()
	fenceNotice(log, settings.Default())
	if logged.Len() != 0 {
		t.Errorf("said again: %q", logged.String())
	}
	chosen := settings.Default()
	chosen.NetworkIsolation = settings.IsolationBlock
	t.Setenv("HIVE_CONFIG", filepath.Join(t.TempDir(), "machine.json"))
	fenceNotice(log, chosen)
	if logged.Len() != 0 {
		t.Errorf("an owner who chose was told: %q", logged.String())
	}
}
