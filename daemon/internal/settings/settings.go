// Package settings is what a machine's owner decides about lending it: how
// large a job it takes, when, and whether it takes any at all. The file is
// theirs to edit, by hand or with `hived config`, and a running daemon picks
// up a change without being restarted.
package settings

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/config"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/fence"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/host"
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
	// Lid allows jobs while the lid is closed. The machine has to stay
	// awake closed for that to mean anything: see host.StaysAwake.
	Lid bool `json:"lid"`
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
	// MacOSIsolation is how a hive-macos job is kept from the Mac and from
	// the jobs before it: IsolationUser, as the hiveworker user on the Mac
	// itself, or IsolationVM, in a throwaway macOS VM cloned from an image
	// the owner made. Empty is IsolationUser.
	MacOSIsolation string `json:"macosIsolation,omitempty"`
	// MaxMacOSVMs is how many macOS VMs run at once under IsolationVM, up
	// to MostMacOSVMs. Zero is one: two VMs share one vmnet network, on
	// which a job, root in its own, can come between the other and its
	// gateway, so a second is the owner's choice to make.
	MaxMacOSVMs int `json:"maxMacOSVMs,omitempty"`
	// AutoUpdate lets the daemon update itself when a release is out and
	// it has no job, by running the installer. Off, it only says so.
	AutoUpdate bool `json:"autoUpdate"`
	// ReserveMemory is the memory HiveRunner leaves for everything else on
	// the host, as "24G" or "40%". Empty means none is held back. Use
	// ReserveBytes for what it comes to.
	ReserveMemory string `json:"reserveMemory,omitempty"`
	// NetworkIsolation is how much of the networks around this machine a
	// job is kept off, beyond the internet: IsolationBlock, its LAN, its
	// tailnet and the machine itself; IsolationLAN, the tailnet and the
	// machine but not the LAN; or IsolationOff, nothing, as it was before
	// there was a fence. Empty is the owner having chosen nothing, which is
	// IsolationLAN: see Isolation. See package fence.
	NetworkIsolation string `json:"networkIsolation,omitempty"`
	// DNS is the resolvers a job's guest is given: DNSHost, those this
	// machine uses, as far as a guest can use them; or DNSPublic,
	// Cloudflare's and Google's, which leaves a LAN's resolver and the names
	// it knows out of reach. Empty is DNSHost.
	DNS string `json:"dns,omitempty"`
	// AllowNetwork is somewhere private that jobs may reach all the same,
	// as `hived config set allow-network` takes it:
	// "192.168.1.20:5432,10.0.0.0/24". Empty means nowhere.
	AllowNetwork string `json:"allowNetwork,omitempty"`
	// IPv6Site is the length of the IPv6 prefix this machine's site has:
	// 56 for most homes, 48 for an office, 64 for a machine whose /64 is
	// its own. Jobs are kept off the whole of it. Zero is the default,
	// fence.DefaultSite. See fence.Policy.Site.
	IPv6Site int `json:"ipv6Site"`
	// DataDir is where the guest image and each job's disk are kept, for an
	// owner who wants them on another disk. Empty means beside the machine
	// file. The two cannot be apart: a job's disk is a clone of the image.
	DataDir string `json:"dataDir,omitempty"`
	// CacheDir is where the Actions cache is kept. Empty means with the
	// user's other caches.
	CacheDir string `json:"cacheDir,omitempty"`
}

// What NetworkIsolation may be.
const (
	IsolationBlock = "block"
	IsolationLAN   = "lan-allow"
	IsolationOff   = "off"
)

// What DNS may be.
const (
	DNSHost   = "host"
	DNSPublic = "public"
)

// Isolation is NetworkIsolation, the default for an owner who has chosen
// nothing included. That default is lan-allow, so that a machine updating
// to a hived with the fence keeps the jobs that use a registry or a cache on
// its LAN working, while they lose the machine itself, its tailnet and a
// cloud's metadata service, which a job has no business with. hived login
// chooses block for a machine enrolled from then on, which has no jobs yet
// to break.
func (s Settings) Isolation() string { return cmp.Or(s.NetworkIsolation, IsolationLAN) }

// What MacOSIsolation may be.
const (
	IsolationUser = "user"
	IsolationVM   = "vm"
)

// MostMacOSVMs is how many macOS VMs Apple's licence lets a Mac run at
// once, and so the most max-macos-vms takes.
const MostMacOSVMs = 2

// MacOSVMs is MaxMacOSVMs, one if the owner has not said.
func (s Settings) MacOSVMs() int { return max(s.MaxMacOSVMs, 1) }

// MacOSInVM reports whether hive-macos jobs run in a throwaway macOS VM.
func (s Settings) MacOSInVM() bool { return s.MacOSIsolation == IsolationVM }

// Sizes are the job sizes a machine can be offered, by guest vCPUs.
var Sizes = []int{2, 4, 8}

// MostCacheGB is the largest cache an owner can ask for.
const MostCacheGB = 2000

// MostReserveGB and MostReservePercent are the largest reserve an owner can
// ask for. A reserve that takes the whole host leaves no job at all.
const (
	MostReserveGB      = 4096
	MostReservePercent = 90
)

// MostJobs is the most jobs any machine runs at once, whatever its owner
// asks for. The pool refuses a daemon that claims more.
const MostJobs = 8

// Default is a machine whose owner has decided nothing: it behaves as the
// daemon did before there were settings.
func Default() Settings {
	return Settings{MaxJobs: 1, Warm: true, Battery: true, Cache: true, AutoUpdate: true, IPv6Site: fence.DefaultSite}
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
	if current.IPv6Site == 0 {
		current.IPv6Site = fence.DefaultSite
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
	for key, dir := range map[string]string{"data-dir": s.DataDir, "cache-dir": s.CacheDir} {
		if dir != "" && !filepath.IsAbs(dir) {
			return fmt.Errorf("%s must be a full path, or left out for the default", key)
		}
	}
	if _, err := parseReserve(s.ReserveMemory); err != nil {
		return err
	}
	if _, err := fence.ParseAllowances(s.AllowNetwork); err != nil {
		return err
	}
	if s.NetworkIsolation != "" && !slices.Contains([]string{IsolationBlock, IsolationLAN, IsolationOff}, s.NetworkIsolation) {
		return errIsolation
	}
	if s.DNS != "" && s.DNS != DNSHost && s.DNS != DNSPublic {
		return errDNS
	}
	// Zero, from settings made before there was one, is the default.
	if s.IPv6Site != 0 && !slices.Contains(fence.Sites, s.IPv6Site) {
		return errIPv6Site
	}
	if s.MacOSIsolation != "" && s.MacOSIsolation != IsolationUser && s.MacOSIsolation != IsolationVM {
		return errMacOSIsolation
	}
	if s.MaxMacOSVMs < 0 || s.MaxMacOSVMs > MostMacOSVMs {
		return errMaxMacOSVMs
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
var Keys = []string{"max-jobs", "max-vcpus", "warm", "battery", "lid", "hours", "cache", "cache-size", "reserve-memory", "network-isolation", "allow-network", "dns", "ipv6-site", "macos-jobs", "macos-isolation", "max-macos-vms", "auto-update", "data-dir", "cache-dir"}

// Set changes one setting from what was typed on a command line.
func (s *Settings) Set(key, value string) error {
	// A path is taken as typed: its case is part of it.
	if key == "data-dir" || key == "cache-dir" {
		dir, err := parseDir(key, value)
		if err != nil {
			return err
		}
		if key == "data-dir" {
			s.DataDir = dir
		} else {
			s.CacheDir = dir
		}
		return nil
	}
	value = strings.ToLower(strings.TrimSpace(value))
	switch key {
	case "max-jobs":
		jobs, err := strconv.Atoi(value)
		if err != nil || jobs < 1 || jobs > MostJobs {
			return fmt.Errorf("max-jobs takes a number from 1 to %d", MostJobs)
		}
		s.MaxJobs = jobs
	case "max-macos-vms":
		vms, err := strconv.Atoi(value)
		if err != nil || vms < 1 || vms > MostMacOSVMs {
			return errMaxMacOSVMs
		}
		s.MaxMacOSVMs = vms
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
	case "warm", "battery", "lid", "cache", "macos-jobs", "auto-update":
		on, err := parseSwitch(value)
		if err != nil {
			return fmt.Errorf("%s takes on or off", key)
		}
		switch key {
		case "warm":
			s.Warm = on
		case "battery":
			s.Battery = on
		case "lid":
			s.Lid = on
		case "macos-jobs":
			s.MacOSJobs = on
		case "auto-update":
			s.AutoUpdate = on
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
	case "reserve-memory":
		if value == "off" {
			s.ReserveMemory = ""
			return nil
		}
		reserve, err := parseReserve(value)
		if err != nil {
			return err
		}
		s.ReserveMemory = reserve.text()
	case "network-isolation":
		if !slices.Contains([]string{IsolationBlock, IsolationLAN, IsolationOff}, value) {
			return errIsolation
		}
		s.NetworkIsolation = value
	case "dns":
		if value != DNSHost && value != DNSPublic {
			return errDNS
		}
		s.DNS = value
	case "allow-network":
		if value == "none" {
			s.AllowNetwork = ""
			return nil
		}
		allowed, err := fence.ParseAllowances(value)
		if err != nil {
			return err
		}
		s.AllowNetwork = fence.FormatAllowances(allowed)
	case "ipv6-site":
		length, err := strconv.Atoi(strings.TrimPrefix(value, "/"))
		if err != nil || !slices.Contains(fence.Sites, length) {
			return errIPv6Site
		}
		s.IPv6Site = length
	case "macos-isolation":
		switch value {
		case IsolationUser:
			s.MacOSIsolation = ""
		case IsolationVM:
			s.MacOSIsolation = IsolationVM
		default:
			return errMacOSIsolation
		}
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

var (
	errMacOSIsolation = errors.New("macos-isolation takes user or vm")
	errMaxMacOSVMs    = fmt.Errorf("max-macos-vms takes 1 or %d: Apple's licence lets a Mac run %d macOS VMs at once", MostMacOSVMs, MostMacOSVMs)
)

// reserve is a parsed reserve-memory: gigabytes, or a percent of the host's
// memory. The zero value holds nothing back.
type reserve struct {
	gigabytes, percent int
}

var errReserve = fmt.Errorf("reserve-memory takes an amount like 24G, a share like 40%%, or off")

// parseReserve reads "24G", "24GB", "40%" or "" (and "off").
func parseReserve(value string) (reserve, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || value == "off" {
		return reserve{}, nil
	}
	if number, ok := strings.CutSuffix(value, "%"); ok {
		percent, err := strconv.Atoi(strings.TrimSpace(number))
		if err != nil || percent < 1 || percent > MostReservePercent {
			return reserve{}, fmt.Errorf("reserve-memory takes a share from 1%% to %d%%, an amount like 24G, or off", MostReservePercent)
		}
		return reserve{percent: percent}, nil
	}
	number := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(value, "b"), "g"))
	if number == value {
		return reserve{}, errReserve
	}
	gigabytes, err := strconv.Atoi(number)
	if err != nil || gigabytes < 1 || gigabytes > MostReserveGB {
		return reserve{}, fmt.Errorf("reserve-memory takes a number of gigabytes from 1 to %d like 24G, a share like 40%%, or off", MostReserveGB)
	}
	return reserve{gigabytes: gigabytes}, nil
}

func (r reserve) text() string {
	switch {
	case r.percent > 0:
		return fmt.Sprintf("%d%%", r.percent)
	case r.gigabytes > 0:
		return fmt.Sprintf("%dG", r.gigabytes)
	}
	return ""
}

// ReserveBytes is the memory to leave for everything else on a host with
// this much in all. It is zero when no reserve is set.
func (s Settings) ReserveBytes(total uint64) uint64 {
	r, err := parseReserve(s.ReserveMemory)
	if err != nil {
		return 0
	}
	if r.percent > 0 {
		return total * uint64(r.percent) / 100
	}
	return uint64(r.gigabytes) << 30
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

// parseDir is a directory as typed, or "" for "default".
func parseDir(key, value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "default") {
		return "", nil
	}
	if rest, ok := strings.CutPrefix(value, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, rest)
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s takes a full path, like /Volumes/Big/HiveWorker, or default", key)
	}
	return filepath.Clean(value), nil
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
	reason, _ := s.holding(power, now)
	return reason
}

// Fix is what the owner can do about Holding's reason, or "" when the
// machine is taking jobs.
func (s Settings) Fix(power host.Power, now time.Time) string {
	_, fix := s.holding(power, now)
	return fix
}

func (s Settings) holding(power host.Power, now time.Time) (reason, fix string) {
	switch {
	case s.Paused:
		return "paused by you", "run hived resume"
	case power.LidClosed && !s.Lid:
		return "lid closed", "open the lid or run hived config set lid on"
	case power.OnBattery && !s.Battery:
		return "on battery", "plug it in or run hived config set battery on"
	case !s.InHours(now):
		return "outside " + s.Hours, "wait for the window or run hived config set hours any"
	}
	return "", ""
}

var (
	errIsolation = errors.New("network-isolation takes block, lan-allow or off")
	errDNS       = errors.New("dns takes host or public")
)

var errIPv6Site = errors.New("ipv6-site takes 48, 56 or 64: the length of the IPv6 prefix this machine's site has")

// Network is what the owner lets a job's guest reach beyond the internet.
func (s Settings) Network() fence.Policy {
	allowed, _ := fence.ParseAllowances(s.AllowNetwork)
	return fence.Policy{Open: s.Isolation() == IsolationOff, LAN: s.Isolation() == IsolationLAN, Allow: allowed, Site: s.IPv6Site}
}

// WideAllowances are the allowances that take in so much they all but open
// the fence, as allow-network writes them.
func (s Settings) WideAllowances() []string {
	allowed, _ := fence.ParseAllowances(s.AllowNetwork)
	var wide []string
	for _, allowance := range allowed {
		if allowance.Wide() {
			wide = append(wide, allowance.String())
		}
	}
	return wide
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
