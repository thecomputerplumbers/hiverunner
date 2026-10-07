package vm

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/protocol"
)

// as makes the tests a machine of one architecture, wherever they run.
func as(t *testing.T, arch string) {
	t.Helper()
	was := hostArch
	hostArch = func() string { return arch }
	t.Cleanup(func() { hostArch = was })
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadImage(t *testing.T) {
	as(t, protocol.ArchARM64)
	dir := t.TempDir()
	if _, err := LoadImage(dir); !errors.Is(err, ErrNoImage) {
		t.Fatalf("empty directory: %v", err)
	}
	write(t, dir, "manifest.json", `{"imageVersion":1,"runnerVersion":"2.0.0","kernel":"Image","initrd":"initrd","disk":"rootfs.img","commandLine":"root=/dev/vda"}`)
	if _, err := LoadImage(dir); err == nil {
		t.Fatal("an image with missing files loaded")
	}
	for _, name := range []string{"Image", "initrd", "rootfs.img"} {
		write(t, dir, name, "x")
	}
	image, err := LoadImage(dir)
	if err != nil || image.RunnerVersion != "2.0.0" || image.Dir != dir {
		t.Fatalf("LoadImage() = %+v, %v", image, err)
	}
	// Every manifest written before there were two architectures is arm64.
	if image.Arch != protocol.ArchARM64 {
		t.Fatalf("a manifest that names no architecture loaded as %q", image.Arch)
	}
}

func TestAnImageIsForOneArchitecture(t *testing.T) {
	dir := t.TempDir()
	manifest := func(arch string) {
		write(t, dir, "manifest.json", `{"imageVersion":7,"runnerVersion":"2.0.0","arch":"`+arch+`","kernel":"vmlinuz","initrd":"initrd","disk":"rootfs.img","commandLine":"root=/dev/vda"}`)
	}
	for _, name := range []string{"vmlinuz", "initrd", "rootfs.img"} {
		write(t, dir, name, "x")
	}
	manifest("x64")
	as(t, protocol.ArchX64)
	image, err := LoadImage(dir)
	if err != nil || image.Arch != protocol.ArchX64 || image.Kernel != "vmlinuz" {
		t.Fatalf("an x64 image on an x64 machine: %+v, %v", image, err)
	}
	// The same image on a Mac is refused before anything tries to boot it.
	as(t, protocol.ArchARM64)
	if _, err := LoadImage(dir); !errors.Is(err, ErrWrongArch) {
		t.Fatalf("an x64 image on an arm64 machine: %v, want ErrWrongArch", err)
	}
	manifest("riscv64")
	if _, err := LoadImage(dir); err == nil || errors.Is(err, ErrWrongArch) {
		t.Fatalf("a manifest naming an architecture no guest is built for: %v", err)
	}
}

func TestAManifestCannotReachOutsideItsDirectory(t *testing.T) {
	as(t, protocol.ArchARM64)
	dir := t.TempDir()
	write(t, dir, "manifest.json", `{"kernel":"../../etc/passwd","initrd":"initrd","disk":"rootfs.img"}`)
	if _, err := LoadImage(dir); err == nil {
		t.Fatal("a manifest naming a file outside its directory loaded")
	}
}

func TestSweepRemovesOnlyLeftoverJobDirectories(t *testing.T) {
	dir := t.TempDir()
	if n, err := SweepWorkDir(filepath.Join(dir, "missing")); n != 0 || err != nil {
		t.Fatalf("a work directory that does not exist yet: %d, %v", n, err)
	}
	for _, name := range []string{"job-1", "job-2/share", "keep-me"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(dir, "job-2", "share"), "jitconfig", "a credential a crash left behind")
	write(t, dir, "job-notes.txt", "a file, not a job directory")

	n, err := SweepWorkDir(dir)
	if err != nil || n != 2 {
		t.Fatalf("SweepWorkDir() = %d, %v; want 2 removed", n, err)
	}
	left, _ := os.ReadDir(dir)
	var names []string
	for _, entry := range left {
		names = append(names, entry.Name())
	}
	if len(names) != 2 || names[0] != "job-notes.txt" || names[1] != "keep-me" {
		t.Fatalf("left behind %v; want only what is not a job directory", names)
	}
}

func TestOneDaemonPerWorkDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "jobs")
	release, err := LockWorkDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A second daemon is refused, and so never reaches the sweep that would
	// delete the first one's running jobs.
	if _, err := LockWorkDir(dir); !errors.Is(err, ErrWorkDirInUse) {
		t.Fatalf("second lock: %v, want ErrWorkDirInUse", err)
	}
	// The lock file is not a job directory; sweeping leaves it alone.
	if err := os.Mkdir(filepath.Join(dir, "job-live"), 0o700); err != nil {
		t.Fatal(err)
	}
	if n, err := SweepWorkDir(dir); n != 1 || err != nil {
		t.Fatalf("SweepWorkDir() = %d, %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".lock")); err != nil {
		t.Fatalf("the lock file was swept: %v", err)
	}
	release()
	again, err := LockWorkDir(dir)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	again()
}
