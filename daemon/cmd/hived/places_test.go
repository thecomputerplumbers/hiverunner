package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/vm"
)

// aMachine is a home directory with an image and a cache in the default
// places, and nothing of the real machine's in reach.
func aMachine(t *testing.T) (current settings.Settings, image, cached string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "HIVE_CONFIG", "HIVE_IMAGE", "HIVE_CACHE"} {
		t.Setenv(name, "")
	}
	current = settings.Default()
	image, err := imageDir(current)
	if err != nil {
		t.Fatal(err)
	}
	cached, err = cacheDir(current)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(image, "manifest.json"), []byte("{}"))
	write(t, filepath.Join(cached, "pools", "one", "entry"), []byte("cached"))
	return current, image, cached
}

func write(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

// set is `hived config set` without the printing.
func set(t *testing.T, current settings.Settings, key, value string) settings.Settings {
	t.Helper()
	next := current
	if err := next.Set(key, value); err != nil {
		t.Fatal(err)
	}
	if err := relocate(key, current, next); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(next); err != nil {
		t.Fatal(err)
	}
	return next
}

func TestSettingADirMovesWhatIsThere(t *testing.T) {
	current, image, cached := aMachine(t)
	big := filepath.Join(t.TempDir(), "big disk")

	current = set(t, current, "data-dir", big)
	current = set(t, current, "cache-dir", big)
	if moved, err := settle(current); err != nil || len(moved) != 2 {
		t.Fatalf("settle: %v %v", moved, err)
	}

	if got, _ := imageDir(current); got != filepath.Join(big, "image") {
		t.Errorf("the image is looked for in %s", got)
	}
	if got, _ := cacheDir(current); got != filepath.Join(big, "actions-cache") {
		t.Errorf("the cache is looked for in %s", got)
	}
	for _, there := range []string{filepath.Join(big, "image", "manifest.json"), filepath.Join(big, "actions-cache", "pools", "one", "entry")} {
		if _, err := os.Stat(there); err != nil {
			t.Errorf("not moved: %v", err)
		}
	}
	for _, gone := range []string{image, cached} {
		if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s was left behind", gone)
		}
	}
	if pending := loadMoves(); pending.Data != nil || pending.Cache != nil {
		t.Errorf("a move is still waiting: %+v", pending)
	}

	// And back again.
	current = set(t, current, "data-dir", "default")
	if _, err := settle(current); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(image, "manifest.json")); err != nil {
		t.Errorf("default did not bring the image back: %v", err)
	}
}

func TestADirCanBeSetBeforeAnythingIsInstalled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "HIVE_CONFIG", "HIVE_IMAGE", "HIVE_CACHE"} {
		t.Setenv(name, "")
	}
	big := t.TempDir()
	current := set(t, settings.Default(), "data-dir", big)
	if _, err := settle(current); err != nil {
		t.Fatal(err)
	}
	if got, _ := imageDir(current); got != filepath.Join(big, "image") {
		t.Errorf("the image is looked for in %s", got)
	}
}

func TestAMoveWaitsForARunningDaemon(t *testing.T) {
	current, image, _ := aMachine(t)
	big := t.TempDir()
	release, err := vm.LockWorkDir(workDir(image))
	if err != nil {
		t.Fatal(err)
	}

	current = set(t, current, "data-dir", big)
	if moved, err := settle(current); err == nil || len(moved) != 0 {
		t.Fatalf("moved from under a running daemon: %v", moved)
	}
	// Until it is moved, the image is where it was, and is found there.
	if got, _ := imageDir(current); got != image {
		t.Errorf("the image is looked for in %s while it is still in %s", got, image)
	}

	release()
	if _, err := settle(current); err != nil {
		t.Fatal(err)
	}
	if got, _ := imageDir(current); got != filepath.Join(big, "image") {
		t.Errorf("after the daemon stopped the image is looked for in %s", got)
	}
}

func TestADiskThatIsNotAttachedIsNotStoodInFor(t *testing.T) {
	current, _, _ := aMachine(t)
	missing := filepath.Join(t.TempDir(), "detached", "HiveWorker")
	current.DataDir, current.CacheDir = missing, missing

	var gone errAway
	if _, err := imageDir(current); !errors.As(err, &gone) {
		t.Errorf("the image: got %v", err)
	}
	if _, err := openCache(func() settings.Settings { return current }); !errors.As(err, &gone) {
		t.Errorf("the cache: got %v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Error("a directory was made where the disk should be")
	}
	if got := absent(current); len(got) != 1 || got[0] != missing {
		t.Errorf("absent: %v", got)
	}
}

func TestAMoveToADiskThatIsNotAttachedStaysWaiting(t *testing.T) {
	current, image, _ := aMachine(t)
	big := filepath.Join(t.TempDir(), "disk")
	current = set(t, current, "data-dir", big)
	if err := os.Remove(big); err != nil {
		t.Fatal(err)
	}
	if _, err := settle(current); err == nil {
		t.Fatal("moved to a disk that is not there")
	}
	if got, _ := imageDir(current); got != image {
		t.Errorf("the image is looked for in %s", got)
	}
	if loadMoves().Data == nil {
		t.Error("the move was forgotten")
	}
}

func TestACopyKeepsHolesAndContents(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "disk"), filepath.Join(dir, "copy")
	file, err := os.Create(from)
	if err != nil {
		t.Fatal(err)
	}
	// Data, a long hole, data, and a hole to the end.
	const size = 64 << 20
	file.WriteAt([]byte("start"), 0)
	file.WriteAt([]byte("middle"), 40<<20)
	file.Truncate(size)
	file.Close()

	if err := copyFile(from, to, 0o600); err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(from)
	got, _ := os.ReadFile(to)
	if !bytes.Equal(want, got) {
		t.Errorf("the copy differs: %d bytes against %d", len(got), len(want))
	}
}

// The macOS image goes where the Linux one goes, since a job's VM is a
// clone of it and a clone is only made within one volume.
func TestTheMacOSImageMovesWithTheImage(t *testing.T) {
	current, _, _ := aMachine(t)
	macOS, err := macosImageDir(current)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(macOS, "manifest.json"), []byte("{}"))
	write(t, filepath.Join(workDir(macOS), "job-1", "disk.img"), []byte("a job's clone"))
	big := filepath.Join(t.TempDir(), "big disk")
	current = set(t, current, "data-dir", big)
	if _, err := settle(current); err != nil {
		t.Fatal(err)
	}
	if got, _ := macosImageDir(current); got != filepath.Join(big, "macos-image") {
		t.Errorf("the macOS image is looked for in %s", got)
	}
	if _, err := os.Stat(filepath.Join(big, "macos-image", "manifest.json")); err != nil {
		t.Errorf("not moved: %v", err)
	}
	for _, gone := range []string{macOS, workDir(macOS)} {
		if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s was left behind", gone)
		}
	}
}
