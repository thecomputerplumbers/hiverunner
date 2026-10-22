package native

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
)

// Spotlight indexes whatever is written under /Users, and a job writes a
// great deal: a checkout, node_modules, Pods, DerivedData, Gradle's outputs.
// Indexing it fights the compiler for the whole job, for files deleted when
// the job ends. Spotlight skips any folder whose name ends in .noindex, so
// with the spotlight-exclude setting on, as it is by default, the jobs live
// in one, and so do the caches a job keeps for the next. That needs no root
// and no system setting. GitHub's runner is root's, outside Home: see
// Runners.

// NoIndexDir is the folder in Home that Spotlight skips.
const NoIndexDir = "hive.noindex"

// markerFile asks Spotlight to leave the folder it is in alone. macOS has
// honoured it less reliably than a .noindex name, so it is only a second
// line, for the rest of Home.
const markerFile = ".metadata_never_index"

// linkedCaches are caches a tool keeps in the home folder, by where they
// are kept and what they are called in the cache folder. Each is moved there
// and a symlink left in its place, rather than moved with a variable: Gradle
// reads gradle.properties from GRADLE_USER_HOME, which a job often writes to
// ~/.gradle, and Xcode has no variable for DerivedData at all.
var linkedCaches = []struct{ home, cache string }{
	{".gradle/caches", "gradle-caches"},
	{".gradle/wrapper", "gradle-wrapper"},
	{"Library/Developer/Xcode/DerivedData", "DerivedData"},
	{"Library/Caches/CocoaPods", "CocoaPods"},
	{"Library/pnpm", "pnpm"},
}

// variableCaches are caches a variable moves without moving anything else
// with them, by variable and what they are called in the cache folder.
var variableCaches = []struct{ variable, cache string }{
	{"CP_HOME_DIR", "cocoapods"},
	{"npm_config_cache", "npm"},
}

// spotlightExcluded is the owner's spotlight-exclude setting. A settings
// file that cannot be read is the default, which is on.
func spotlightExcluded() bool {
	current, _ := settings.Load()
	return current.SpotlightExclude
}

// layout is where the child keeps its jobs and their caches: in Home, or in
// Home's hive.noindex.
type layout struct {
	home    string
	noIndex bool
}

func (l layout) base() string {
	if l.noIndex {
		return filepath.Join(l.home, NoIndexDir)
	}
	return l.home
}

func (l layout) jobs() string  { return filepath.Join(l.base(), "jobs") }
func (l layout) cache() string { return filepath.Join(l.home, NoIndexDir, "cache") }
func (l layout) other() layout { return layout{home: l.home, noIndex: !l.noIndex} }

// legacyRunner is where hiveworker unpacked GitHub's runner for itself,
// before it was root's in Runners. Nothing runs it; arrange and a clean
// remove it.
func (l layout) legacyRunner() string { return filepath.Join(l.base(), "runner") }

// env is what a job is given to keep the caches a variable moves out of
// the index. A job's own env: wins over it, as over anything the runner
// starts with.
func (l layout) env() []string {
	if !l.noIndex {
		return nil
	}
	var env []string
	for _, cache := range variableCaches {
		env = append(env, cache.variable+"="+filepath.Join(l.cache(), cache.cache))
	}
	return env
}

// arrange moves everything into this layout from the other: the linked
// caches; the caches a variable moves start empty. Job directories left
// behind are removed, and so is a runner hiveworker once unpacked for
// itself, in either. It must run only while no job does, and is safe to
// repeat. What it cannot do it says, and the rest is done anyway.
func (l layout) arrange() error {
	var problems []error
	if l.noIndex {
		if err := os.MkdirAll(l.base(), 0o700); err != nil {
			return err
		}
	}
	problems = append(problems, os.RemoveAll(l.legacyRunner()), os.RemoveAll(l.other().legacyRunner()))
	// No job is running, so whatever is in either is left from one that
	// did not finish.
	problems = append(problems, os.RemoveAll(l.other().jobs()), os.RemoveAll(l.jobs()))
	for _, cache := range variableCaches {
		if l.noIndex {
			problems = append(problems, os.MkdirAll(filepath.Join(l.cache(), cache.cache), 0o700))
		}
	}
	for _, cache := range linkedCaches {
		from, to := filepath.Join(l.home, cache.home), filepath.Join(l.cache(), cache.cache)
		if l.noIndex {
			problems = append(problems, link(from, to))
		} else {
			problems = append(problems, unlink(from, to))
		}
	}
	marker := filepath.Join(l.home, markerFile)
	if l.noIndex {
		if _, err := os.Lstat(marker); errors.Is(err, os.ErrNotExist) {
			problems = append(problems, os.WriteFile(marker, nil, 0o644))
		}
	} else if info, err := os.Lstat(marker); err == nil && info.Mode().IsRegular() && info.Size() == 0 {
		problems = append(problems, os.Remove(marker))
	}
	return errors.Join(problems...)
}

// move renames the directory from to to, whole or not at all. When to is
// already there, from is the stale copy and goes.
func move(from, to string) error {
	if _, err := os.Lstat(from); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if _, err := os.Lstat(to); err == nil {
		return os.RemoveAll(from)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	return os.Rename(from, to)
}

// link moves the cache directory at from to to and leaves a symlink to it
// behind, so the tool that keeps it at from still finds it. A symlink
// already there, ours or anyone's, and a file are left alone.
func link(from, to string) error {
	info, err := os.Lstat(from)
	switch {
	case err == nil && info.IsDir():
		if err := move(from, to); err != nil {
			return err
		}
	case err == nil:
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	if err := os.MkdirAll(to, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(from), 0o700); err != nil {
		return err
	}
	return os.Symlink(to, from)
}

// unlink undoes link: the symlink goes and the cache moves back. A symlink
// that points anywhere else is not ours and stays.
func unlink(from, to string) error {
	if target, err := os.Readlink(from); err != nil || target != to {
		return nil
	}
	if err := os.Remove(from); err != nil {
		return err
	}
	return move(to, from)
}

// SpotlightReport is what hived doctor says about Spotlight and the job
// directories.
type SpotlightReport struct {
	// Volume is the mount point that holds Home.
	Volume string
	// Indexing is what mdutil says of Volume: "on", "off" or "" when it
	// would not say.
	Indexing string
	// NoIndex is the spotlight-exclude setting.
	NoIndex bool
	// Moved is whether the runner is in hive.noindex yet, when that could
	// be seen from here: nil when it could not.
	Moved *bool
}

// indexing reads mdutil -s.
func indexing(out string) string {
	switch {
	case strings.Contains(out, "Indexing enabled"):
		return "on"
	case strings.Contains(out, "Indexing disabled"):
		return "off"
	}
	return ""
}
