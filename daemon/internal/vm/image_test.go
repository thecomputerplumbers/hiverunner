package vm

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadImage(t *testing.T) {
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
}

func TestAManifestCannotReachOutsideItsDirectory(t *testing.T) {
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
