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
