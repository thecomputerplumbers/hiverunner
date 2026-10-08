// Package settings is what a machine's owner decides about lending it: how
// large a job it takes, when, and whether it takes any at all. The file is
// theirs to edit, by hand or with `hived config`, and a running daemon picks
// up a change without being restarted.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/config"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/host"
)

// Settings are the owner's choices. The zero value is not the default: use
// Default, which Load also starts from, so a key missing from the file keeps
// its default.
type Settings struct {
	// Paused stops new jobs until the owner resumes.
	Paused bool `json:"paused"`
	// MaxVCPUs is the largest job this machine takes. Zero means as large as
	// the machine allows.
	MaxVCPUs int `json:"maxVcpus"`
	// MaxJobs is how many jobs this machine runs at once.
	MaxJobs int `json:"maxJobs"`
	// Warm keeps one VM booted while idle, so a job skips the boot.
	Warm bool `json:"warm"`
	// Battery allows jobs while the machine is unplugged.
	Battery bool `json:"battery"`
	// Cache keeps an Actions cache on this machine for the jobs it runs.
	Cache bool `json:"cache"`
	// CacheGB is the most disk the cache may use, in gigabytes. Zero means
	// a size chosen from the disk's: see cache.AutoLimit.
	CacheGB int `json:"cacheGb"`
	// Hours is when jobs may run, as "22:00-07:00" in local time. Empty
	// means any time. A window may cross midnight.
	Hours string `json:"hours"`
	// MacOSJobs takes hive-macos jobs, which run on this Mac itself as a
	// separate user rather than in a VM. `hived macos setup` turns it on
	// once the Mac is ready; it is never on by default.
	MacOSJobs bool `json:"macosJobs"`
}

// Sizes are the job sizes a machine can be offered, by guest vCPUs.
var Sizes = []int{2, 4, 8}

// MostCacheGB is the largest cache an owner can ask for.
const MostCacheGB = 2000

// MostJobs is the most jobs any machine runs at once, whatever its owner
// asks for. The pool refuses a daemon that claims more.
const MostJobs = 8

// Default is a machine whose owner has decided nothing: it behaves as the
// daemon did before there were settings.
func Default() Settings {
	return Settings{MaxJobs: 1, Warm: true, Battery: true, Cache: true}
}

// Path is the settings file, beside the machine file.
func Path() (string, error) {
	machine, err := config.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(machine), "settings.json"), nil
}

// Load reads the settings file. No file is not an error: it is the defaults.
func Load() (Settings, error) {
	path, err := Path()
	if err != nil {
		return Default(), err
	}
	return read(path)
}

func read(path string) (Settings, error) {
	current := Default()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return current, nil
	}
	if err != nil {
		return current, err
	}
	if err := json.Unmarshal(raw, &current); err != nil {
		return Default(), fmt.Errorf("%s is not readable: %w", path, err)
	}
	// A file written by hand may leave it out as zero.
	if current.MaxJobs == 0 {
		current.MaxJobs = 1
	}
	if err := current.validate(); err != nil {
		return Default(), fmt.Errorf("%s: %w", path, err)
	}
	return current, nil
}

// Save replaces the settings file in one step.
func Save(next Settings) error {
	if err := next.validate(); err != nil {
		return err
	}
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func (s Settings) validate() error {
	if s.MaxVCPUs != 0 && !isSize(s.MaxVCPUs) {
		return fmt.Errorf("max-vcpus must be 2, 4, 8 or auto")
	}
	if s.MaxJobs < 1 || s.MaxJobs > MostJobs {
		return fmt.Errorf("max-jobs must be from 1 to %d", MostJobs)
	}
	if s.CacheGB < 0 || s.CacheGB > MostCacheGB {
		return fmt.Errorf("cache-size must be auto or from 1 to %d", MostCacheGB)
	}
	_, _, err := parseHours(s.Hours)
	return err
}

func isSize(vcpus int) bool {
	for _, size := range Sizes {
		if size == vcpus {
			return true
		}
	}
	return false
}

// Keys are the settings `hived config set` accepts, in the order shown.
var Keys = []string{"max-jobs", "max-vcpus", "warm", "battery", "hours", "cache", "cache-size", "macos-jobs"}

// Set changes one setting from what was typed on a command line.
func (s *Settings) Set(key, value string) error {
	value = strings.ToLower(strings.TrimSpace(value))
	switch key {
	case "max-jobs":
		jobs, err := strconv.Atoi(value)
		if err != nil || jobs < 1 || jobs > MostJobs {
			return fmt.Errorf("max-jobs takes a number from 1 to %d", MostJobs)
		}
		s.MaxJobs = jobs
	case "max-vcpus":
		if value == "auto" {
			s.MaxVCPUs = 0
			return nil
		}
		vcpus, err := strconv.Atoi(value)
		if err != nil || !isSize(vcpus) {
			return errors.New("max-vcpus takes 2, 4, 8 or auto")
		}
		s.MaxVCPUs = vcpus
	case "warm", "battery", "cache", "macos-jobs":
		on, err := parseSwitch(value)
		if err != nil {
			return fmt.Errorf("%s takes on or off", key)
		}
		switch key {
		case "warm":
			s.Warm = on
		case "battery":
			s.Battery = on
		case "macos-jobs":
			s.MacOSJobs = on
		default:
			s.Cache = on
		}
	case "cache-size":
		if value == "auto" {
			s.CacheGB = 0
			return nil
		}
		gigabytes, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(value, "b"), "g"))
		if err != nil || gigabytes < 1 || gigabytes > MostCacheGB {
			return fmt.Errorf("cache-size takes a number of gigabytes from 1 to %d, or auto", MostCacheGB)
		}
		s.CacheGB = gigabytes
	case "hours":
		if value == "any" {
			value = ""
		}
		if _, _, err := parseHours(value); err != nil {
			return err
		}
		s.Hours = value
	default:
		return fmt.Errorf("there is no setting called %q; the settings are %s", key, strings.Join(Keys, ", "))
	}
	return nil
}

func parseSwitch(value string) (bool, error) {
	switch value {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	}
	return false, errors.New("not a switch")
}

var errHours = errors.New(`hours takes a window like 22:00-07:00, or any`)

// parseHours returns the window's ends as minutes past midnight.
func parseHours(window string) (from, to int, err error) {
	if window == "" {
		return 0, 0, nil
	}
	start, end, ok := strings.Cut(window, "-")
	if !ok {
		return 0, 0, errHours
	}
	if from, err = parseClock(start); err != nil {
		return 0, 0, errHours
	}
	if to, err = parseClock(end); err != nil || from == to {
		return 0, 0, errHours
	}
	return from, to, nil
}

func parseClock(clock string) (int, error) {
	at, err := time.Parse("15:04", strings.TrimSpace(clock))
	if err != nil {
		return 0, err
	}
	return at.Hour()*60 + at.Minute(), nil
}

// InHours reports whether now falls inside the window jobs may run in.
func (s Settings) InHours(now time.Time) bool {
	from, to, err := parseHours(s.Hours)
	if err != nil || s.Hours == "" {
		return true
	}
	minute := now.Hour()*60 + now.Minute()
	if from < to {
		return minute >= from && minute < to
	}
	// The window crosses midnight.
	return minute >= from || minute < to
}

// Holding is why the machine is not taking jobs right now, in the owner's
// terms, or "" if it is. The first reason that applies is the one given.
func (s Settings) Holding(power host.Power, now time.Time) string {
	switch {
	case s.Paused:
		return "paused by you"
	case power.LidClosed:
		return "lid closed"
	case power.OnBattery && !s.Battery:
		return "on battery"
	case !s.InHours(now):
		return "outside " + s.Hours
	}
	return ""
}

// Takes reports whether a job of this size is within the owner's limit.
func (s Settings) Takes(vcpus int) bool {
	return s.MaxVCPUs == 0 || vcpus <= s.MaxVCPUs
}

// Live is the settings file as a running daemon sees it: read again only
// when the file has changed, so asking is cheap enough to do on a timer.
type Live struct {
	path string

	mu      sync.Mutex
	stamp   time.Time
	size    int64
	current Settings
	// OnError is told when the file cannot be read. The last good settings
	// stay in force: a typo must not change what the machine is lending.
	OnError func(error)
}

// Watch returns the live settings, read once now.
func Watch() (*Live, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	current, err := read(path)
	if err != nil {
		return nil, err
	}
	live := &Live{path: path, current: current}
	if info, err := os.Stat(path); err == nil {
		live.stamp, live.size = info.ModTime(), info.Size()
	}
	return live, nil
}

// Get returns the settings in force.
func (l *Live) Get() Settings {
	l.mu.Lock()
	defer l.mu.Unlock()
	info, err := os.Stat(l.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if !l.stamp.IsZero() {
			l.stamp, l.size, l.current = time.Time{}, 0, Default()
		}
	case err == nil && (!info.ModTime().Equal(l.stamp) || info.Size() != l.size):
		l.stamp, l.size = info.ModTime(), info.Size()
		next, err := read(l.path)
		if err != nil {
			if l.OnError != nil {
				l.OnError(err)
			}
			break
		}
		l.current = next
	}
	return l.current
}
