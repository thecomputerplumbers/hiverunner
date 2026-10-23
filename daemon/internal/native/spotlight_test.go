//go:build unix

package native

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// mkfile writes a file and the directories it is in.
func mkfile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(raw)
}

func gone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("%s is still there (%v)", path, err)
	}
}

func TestLayoutKeepsTheJobsWhereSpotlightLooksOnlyWhenAskedTo(t *testing.T) {
	on, off := layout{home: "/Users/hw", noIndex: true}, layout{home: "/Users/hw"}
	if on.legacyRunner() != "/Users/hw/hive.noindex/runner" || on.jobs() != "/Users/hw/hive.noindex/jobs" || on.cache() != "/Users/hw/hive.noindex/cache" {
		t.Fatalf("on: %s %s %s", on.legacyRunner(), on.jobs(), on.cache())
	}
	if off.legacyRunner() != "/Users/hw/runner" || off.jobs() != "/Users/hw/jobs" {
		t.Fatalf("off: %s %s", off.legacyRunner(), off.jobs())
	}
	if env := off.env(); env != nil {
		t.Fatalf("off gave the job %q", env)
	}
	env := on.env()
	for _, want := range []string{"CP_HOME_DIR=/Users/hw/hive.noindex/cache/cocoapods", "npm_config_cache=/Users/hw/hive.noindex/cache/npm"} {
		if !slices.Contains(env, want) {
			t.Errorf("on gave %q, without %s", env, want)
		}
	}
	// GRADLE_USER_HOME would move gradle.properties too, which a job often
	// writes to ~/.gradle.
	for _, variable := range env {
		if strings.HasPrefix(variable, "GRADLE_USER_HOME=") || strings.HasPrefix(variable, "HOME=") {
			t.Errorf("on gave %s", variable)
		}
	}
}

func TestArrangeMovesTheCachesIntoHiveNoindexAndBack(t *testing.T) {
	home := t.TempDir()
	mkfile(t, filepath.Join(home, "runner", RunnerVersion, "run.sh"), "runner")
	mkfile(t, filepath.Join(home, "jobs", "left-123", "runner", "x"), "left")
	mkfile(t, filepath.Join(home, ".gradle", "caches", "modules-2", "jar"), "gradle")
	mkfile(t, filepath.Join(home, ".gradle", "gradle.properties"), "signing=yes")
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "Library"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(home, "Library", "pnpm")); err != nil {
		t.Fatal(err)
	}

	on := layout{home: home, noIndex: true}
	for range 2 {
		if err := on.arrange(); err != nil {
			t.Fatal(err)
		}
	}
	// The runner hiveworker unpacked for itself is not moved but removed:
	// jobs clone root's.
	gone(t, filepath.Join(home, "runner"))
	gone(t, on.legacyRunner())
	gone(t, filepath.Join(home, "jobs"))
	if info, err := os.Stat(on.base()); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("hive.noindex: %v %v", info, err)
	}
	// The cache moved, a symlink in its place; the settings beside it did not.
	if target, err := os.Readlink(filepath.Join(home, ".gradle", "caches")); err != nil || target != filepath.Join(on.cache(), "gradle-caches") {
		t.Fatalf("~/.gradle/caches points at %q (%v)", target, err)
	}
	if read(t, filepath.Join(home, ".gradle", "caches", "modules-2", "jar")) != "gradle" {
		t.Fatal("the Gradle cache was lost")
	}
	if read(t, filepath.Join(home, ".gradle", "gradle.properties")) != "signing=yes" {
		t.Fatal("gradle.properties moved")
	}
	// A cache that did not exist is made, linked, ready for its tool.
	if info, err := os.Stat(filepath.Join(home, "Library", "Developer", "Xcode", "DerivedData")); err != nil || !info.IsDir() {
		t.Fatalf("DerivedData: %v", err)
	}
	// Someone else's symlink is theirs.
	if target, _ := os.Readlink(filepath.Join(home, "Library", "pnpm")); target != elsewhere {
		t.Fatalf("~/Library/pnpm was repointed at %q", target)
	}
	if _, err := os.Stat(filepath.Join(home, markerFile)); err != nil {
		t.Fatalf("no %s: %v", markerFile, err)
	}
	if info, err := os.Stat(filepath.Join(on.cache(), "npm")); err != nil || !info.IsDir() {
		t.Fatalf("npm's cache was not made: %v", err)
	}

	// One unpacked in either layout goes.
	mkfile(t, filepath.Join(home, "runner", RunnerVersion, "run.sh"), "stale")
	mkfile(t, filepath.Join(on.legacyRunner(), RunnerVersion, "run.sh"), "stale")
	if err := on.arrange(); err != nil {
		t.Fatal(err)
	}
	gone(t, filepath.Join(home, "runner"))
	gone(t, on.legacyRunner())

	// Off puts everything back where it was.
	off := layout{home: home}
	if err := off.arrange(); err != nil {
		t.Fatal(err)
	}
	gone(t, filepath.Join(home, "runner"))
	if info, err := os.Lstat(filepath.Join(home, ".gradle", "caches")); err != nil || !info.IsDir() {
		t.Fatalf("~/.gradle/caches is not a directory again: %v %v", info, err)
	}
	if read(t, filepath.Join(home, ".gradle", "caches", "modules-2", "jar")) != "gradle" {
		t.Fatal("the Gradle cache was lost moving back")
	}
	gone(t, filepath.Join(home, markerFile))
	if target, _ := os.Readlink(filepath.Join(home, "Library", "pnpm")); target != elsewhere {
		t.Fatalf("~/Library/pnpm was repointed at %q", target)
	}
}

func TestArrangeLeavesAMarkerItDidNotWrite(t *testing.T) {
	home := t.TempDir()
	mkfile(t, filepath.Join(home, markerFile), "the owner's")
	if err := (layout{home: home}).arrange(); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(home, markerFile)) != "the owner's" {
		t.Fatal("a marker with something in it was removed")
	}
}

func TestIndexingReadsMdutil(t *testing.T) {
	for out, want := range map[string]string{
		"/System/Volumes/Data:\n\tIndexing enabled. \n":  "on",
		"/System/Volumes/Data:\n\tIndexing disabled.\n":  "off",
		"/System/Volumes/Data:\n\tError: unknown state.": "",
	} {
		if got := indexing(out); got != want {
			t.Errorf("%q read as %q", out, got)
		}
	}
}
