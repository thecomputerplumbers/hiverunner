package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/cache"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/vm"
)

// Where hived keeps what takes room, which an owner may put on another disk:
// the guest image with each job's disk beside it (`data-dir`), and the
// Actions cache (`cache-dir`). The machine's token and its settings never
// move: they stay beside the machine file, in the home directory.
//
// A setting names a home, and hived keeps one directory of its own inside
// it, so both settings may name the same place.
const (
	imageName = "image"
	cacheName = "actions-cache"
	// macosImageName is the owner's macOS image, for macos-isolation vm.
	// It goes where the Linux image goes, so a job's VM is a clone of it.
	macosImageName = "macos-image"
)

// A move is a home that changed while what it holds was still in the old
// one. Until it is settled the old home is the one in force, so a daemon
// that is running, or a move that failed, carries on from where things are.
type move struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type moves struct {
	Data  *move `json:"data,omitempty"`
	Cache *move `json:"cache,omitempty"`
}

func movesPath() (string, error) {
	path, err := settings.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "moves.json"), nil
}

// loadMoves reads the moves still to settle. None, and a file that cannot
// be read, are the same: nothing is waiting.
func loadMoves() moves {
	var pending moves
	path, err := movesPath()
	if err != nil {
		return pending
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return pending
	}
	_ = json.Unmarshal(raw, &pending)
	return pending
}

func saveMoves(pending moves) error {
	path, err := movesPath()
	if err != nil {
		return err
	}
	if pending.Data == nil && pending.Cache == nil {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func defaultDataHome() (string, error) {
	dir, err := vm.DefaultImageDir()
	if err != nil {
		return "", err
	}
	return filepath.Dir(dir), nil
}

func defaultCacheHome() (string, error) {
	dir, err := cache.DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Dir(dir), nil
}

// chosenHome is the home a setting asks for: the owner's, or the default.
func chosenHome(chosen string, fallback func() (string, error)) (string, error) {
	if chosen != "" {
		return chosen, nil
	}
	return fallback()
}

// dataHome is where the image and the job disks are now.
func dataHome(current settings.Settings) (string, error) {
	if pending := loadMoves().Data; pending != nil {
		return pending.From, nil
	}
	return chosenHome(current.DataDir, defaultDataHome)
}

// cacheHome is where the Actions cache is now.
func cacheHome(current settings.Settings) (string, error) {
	if pending := loadMoves().Cache; pending != nil {
		return pending.From, nil
	}
	return chosenHome(current.CacheDir, defaultCacheHome)
}

// errAway means a home its owner chose is not there, which is what a disk
// that is not attached looks like. Nothing is created in its place: that
// would quietly fill the disk the owner was keeping clear.
type errAway struct{ key, home string }

func (e errAway) Error() string {
	return fmt.Sprintf("%s is not there. Attach its disk, or run: hived config set %s default", e.home, e.key)
}

func away(key, home string) error {
	if _, err := os.Stat(home); err != nil {
		return errAway{key, home}
	}
	return nil
}

// imageDir is the guest image's directory. HIVE_IMAGE, which development
// and the tests use, comes before anything the owner set.
func imageDir(current settings.Settings) (string, error) {
	if os.Getenv("HIVE_IMAGE") != "" {
		return vm.DefaultImageDir()
	}
	home, err := dataHome(current)
	if err != nil {
		return "", err
	}
	if fallback, _ := defaultDataHome(); home != fallback {
		if err := away("data-dir", home); err != nil {
			return "", err
		}
	}
	return filepath.Join(home, imageName), nil
}

// macosImageDir is the macOS image's directory, beside the Linux one.
func macosImageDir(current settings.Settings) (string, error) {
	home, err := dataHome(current)
	if err != nil {
		return "", err
	}
	if fallback, _ := defaultDataHome(); home != fallback {
		if err := away("data-dir", home); err != nil {
			return "", err
		}
	}
	return filepath.Join(home, macosImageName), nil
}

// cacheDir is the Actions cache's directory. HIVE_CACHE comes first, as
// HIVE_IMAGE does.
func cacheDir(current settings.Settings) (string, error) {
	if os.Getenv("HIVE_CACHE") != "" {
		return cache.DefaultDir()
	}
	home, err := cacheHome(current)
	if err != nil {
		return "", err
	}
	if fallback, _ := defaultCacheHome(); home != fallback {
		if err := away("cache-dir", home); err != nil {
			return "", err
		}
	}
	return filepath.Join(home, cacheName), nil
}

// workDir is where a job's disk and share live while it runs: beside the
// image, so the disk is a clone and not a copy, and named after it, so two
// daemons on different images (as in development) keep separate job
// directories and separate network identities.
func workDir(image string) string { return image + "-jobs" }

// macosWorkDir is where the macOS VMs' jobs live, or "" if the data
// directory is not there to hold any.
func macosWorkDir(current settings.Settings) string {
	dir, err := macosImageDir(current)
	if err != nil {
		return ""
	}
	return workDir(dir)
}

// relocate records that a home changed, to be settled now or by the daemon.
// It makes the new home and checks it can be written to, so a typo or a
// read-only disk is said at once rather than found by a daemon later.
func relocate(key string, before, after settings.Settings) error {
	var from, to string
	var err error
	if key == "data-dir" {
		if from, err = dataHome(before); err == nil {
			to, err = chosenHome(after.DataDir, defaultDataHome)
		}
	} else {
		if from, err = cacheHome(before); err == nil {
			to, err = chosenHome(after.CacheDir, defaultCacheHome)
		}
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(to, 0o700); err != nil {
		return fmt.Errorf("could not make %s: %w", to, err)
	}
	probe, err := os.CreateTemp(to, ".hived-")
	if err != nil {
		return fmt.Errorf("%s cannot be written to: %w", to, err)
	}
	probe.Close()
	os.Remove(probe.Name())

	pending := loadMoves()
	next := &move{From: from, To: to}
	if from == to {
		next = nil
	}
	if key == "data-dir" {
		pending.Data = next
	} else {
		pending.Cache = next
	}
	return saveMoves(pending)
}

// settle carries out the moves that are waiting and reports what it moved.
// A move it cannot make stays waiting, and its old home stays in force.
//
// Nothing is moved while a hived holds the work directory, so never from
// under a running daemon or its jobs. The daemon calls this as it starts,
// before it opens either place, and `hived config set` calls it too, which
// does the move there and then when no daemon runs.
func settle(current settings.Settings) (moved []string, err error) {
	pending := loadMoves()
	if pending.Data == nil && pending.Cache == nil {
		return nil, nil
	}
	if daemonRunning(current) {
		return nil, errors.New("a running hived is using what is there")
	}
	var failures []error
	if next := pending.Data; next != nil {
		from := filepath.Join(next.From, imageName)
		macOS := filepath.Join(next.From, macosImageName)
		err := carry(from, filepath.Join(next.To, imageName), next.To)
		if err == nil {
			err = carry(macOS, filepath.Join(next.To, macosImageName), next.To)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("the image: %w", err))
		} else {
			// What a job left behind is not worth carrying.
			_ = os.RemoveAll(workDir(from))
			_ = os.RemoveAll(workDir(macOS))
			_ = os.Remove(next.From)
			moved = append(moved, "the image and job disks are now in "+next.To)
			pending.Data = nil
		}
	}
	if next := pending.Cache; next != nil {
		if err := carry(filepath.Join(next.From, cacheName), filepath.Join(next.To, cacheName), next.To); err != nil {
			failures = append(failures, fmt.Errorf("the Actions cache: %w", err))
		} else {
			_ = os.Remove(next.From)
			moved = append(moved, "the Actions cache is now in "+next.To)
			pending.Cache = nil
		}
	}
	if err := saveMoves(pending); err != nil {
		failures = append(failures, err)
	}
	return moved, errors.Join(failures...)
}

// carry moves a directory into a home that must already be there. Something
// already at the destination wins and the source is dropped: the installer
// puts a fresh image straight into a new home. Across disks it is a copy
// under another name, swapped in whole, so a destination is never half there.
func carry(from, to, home string) error {
	if _, err := os.Stat(home); err != nil {
		return fmt.Errorf("%s is not there", home)
	}
	if _, err := os.Stat(from); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if _, err := os.Stat(to); err == nil {
		return os.RemoveAll(from)
	}
	if os.Rename(from, to) == nil {
		return nil
	}
	partial := to + ".moving"
	if err := os.RemoveAll(partial); err != nil {
		return err
	}
	if err := copyTree(from, partial); err != nil {
		os.RemoveAll(partial)
		return err
	}
	if err := os.Rename(partial, to); err != nil {
		os.RemoveAll(partial)
		return err
	}
	return os.RemoveAll(from)
}

func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			return copyFile(path, target, info.Mode().Perm())
		}
		return nil
	})
}

// copyFile copies a file's data and leaves its holes as holes: a guest disk
// is mostly empty, and copied whole it would take the room it only claims.
func copyFile(from, to string, mode fs.FileMode) error {
	source, err := os.Open(from)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	info, err := source.Stat()
	if err != nil {
		target.Close()
		return err
	}
	if err := copyData(source, target, info.Size()); err != nil {
		target.Close()
		return err
	}
	if err := target.Truncate(info.Size()); err != nil {
		target.Close()
		return err
	}
	return target.Close()
}

func copyData(source, target *os.File, size int64) error {
	for offset := int64(0); offset < size; {
		data, err := unix.Seek(int(source.Fd()), offset, unix.SEEK_DATA)
		if errors.Is(err, unix.ENXIO) {
			// Nothing but a hole from here to the end.
			return nil
		}
		if err != nil {
			// A filesystem that cannot say where its holes are.
			data = offset
		}
		hole := size
		if err == nil {
			if hole, err = unix.Seek(int(source.Fd()), data, unix.SEEK_HOLE); err != nil {
				hole = size
			}
		}
		if _, err := source.Seek(data, io.SeekStart); err != nil {
			return err
		}
		if _, err := target.Seek(data, io.SeekStart); err != nil {
			return err
		}
		if _, err := io.CopyN(target, source, hole-data); err != nil {
			return err
		}
		offset = hole
	}
	return nil
}

// absent lists the homes the owner chose, or a move is bound for, that are
// not there now.
func absent(current settings.Settings) []string {
	var gone []string
	pending := loadMoves()
	homes := []string{current.DataDir, current.CacheDir}
	if pending.Data != nil {
		homes = append(homes, pending.Data.From)
	}
	if pending.Cache != nil {
		homes = append(homes, pending.Cache.From)
	}
	seen := map[string]bool{}
	for _, home := range homes {
		if home == "" || seen[home] {
			continue
		}
		seen[home] = true
		if _, err := os.Stat(home); err != nil {
			gone = append(gone, home)
		}
	}
	return gone
}
