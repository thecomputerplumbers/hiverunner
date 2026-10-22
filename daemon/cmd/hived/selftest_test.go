package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/update"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/vm"
)

func TestASelfTestDoesNotNeedTheDaemonStopped(t *testing.T) {
	image := t.TempDir()
	release, err := vm.LockWorkDir(workDir(image))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := refuseSelftestWhileBusy(workDir(image)); err != nil {
		t.Fatalf("an idle daemon blocked a self-test: %v", err)
	}
	got, err := vm.LockWorkDir(selftestDir(image))
	if err != nil {
		t.Fatalf("a self-test could not take its own directory while the daemon holds the jobs directory: %v", err)
	}
	got()
}

func TestASelfTestRefusesWhileAJobRuns(t *testing.T) {
	work := t.TempDir()
	clock := filepath.Join(work, "job-1", "in", "clock")
	if err := os.MkdirAll(filepath.Dir(clock), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := refuseSelftestWhileBusy(work)
	if err == nil || !strings.Contains(err.Error(), "a job is running") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "hived stop") {
		t.Fatalf("told the owner to stop the daemon under a job: %v", err)
	}
}

func TestInstalledImageVersionReadsWhatIsOnDisk(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HIVE_CONFIG", filepath.Join(t.TempDir(), "machine.json"))
	t.Setenv("HIVE_IMAGE", "")
	if got := installedImageVersion(); got != "" {
		t.Fatalf("no image: %q", got)
	}
	dir := t.TempDir()
	t.Setenv("HIVE_IMAGE", dir)
	kernel := "Image"
	if runtime.GOARCH == "amd64" {
		kernel = "vmlinuz"
	}
	write(t, filepath.Join(dir, "manifest.json"), []byte(
		`{"imageVersion":11,"runnerVersion":"2.337.0","arch":"`+runtime.GOARCH+`","kernel":"`+kernel+`","initrd":"initrd","disk":"rootfs.img","commandLine":"x"}`,
	))
	for _, name := range []string{kernel, "initrd", "rootfs.img"} {
		write(t, filepath.Join(dir, name), []byte("x"))
	}
	if got := installedImageVersion(); got != update.ImageID(11, "2.337.0") {
		t.Fatalf("got %q", got)
	}
}
