// Package update keeps the daemon current without nagging. Once a day, with
// some jitter so a fleet does not ask all at once, it reads the release
// manifest the installer reads, remembering its ETag so an unchanged
// manifest costs one small request. A newer version is said once: in the
// log, in `hived status`, and as a desktop notification. If the owner lets
// it, the daemon then updates itself the way it was installed, by running
// the installer, and only while it has no job.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/config"
)

// DefaultBase is where releases are published; the installer reads the same.
const DefaultBase = "https://get.hiveworkr.thecomputerplumbers.com"

// Base is the releases origin, or HIVE_RELEASES for a test server.
func Base() string {
	if override := os.Getenv("HIVE_RELEASES"); override != "" {
		return strings.TrimRight(override, "/")
	}
	return DefaultBase
}

// Every is how often the manifest is read, before jitter.
const Every = 24 * time.Hour

// Latest is the release manifest, as much of it as the daemon reads.
type Latest struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Image   struct {
		Version string `json:"version"`
	} `json:"image"`
}

// State is what is remembered between checks, in update.json beside the
// machine file. A missing file is the zero value.
type State struct {
	CheckedAt time.Time `json:"checkedAt"`
	ETag      string    `json:"etag,omitempty"`
	// Latest is the newest version seen, whether or not it is newer than
	// this daemon.
	Latest string `json:"latest,omitempty"`
	// Notified is the version the owner was last told about, so they are
	// told about each one once.
	Notified string `json:"notified,omitempty"`
	// Failed is when applying an update last failed; another attempt waits
	// a day from then.
	Failed time.Time `json:"failed,omitzero"`
}

func statePath() (string, error) {
	machine, err := config.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(machine), "update.json"), nil
}

// LoadState reads what was remembered; nothing is not an error.
func LoadState() (State, error) {
	path, err := statePath()
	if err != nil {
		return State{}, err
	}
	var state State
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, nil
	}
	return state, nil
}

// SaveState writes it in one step.
func SaveState(state State) error {
	path, err := statePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// Check reads the manifest, using the remembered ETag, and updates the
// state. ok is false when the manifest has not changed since last time, in
// which case latest is what was remembered.
func Check(ctx context.Context, state *State) (latest Latest, changed bool, err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, Base()+"/latest.json", nil)
	if err != nil {
		return latest, false, err
	}
	if state.ETag != "" && state.Latest != "" {
		request.Header.Set("If-None-Match", state.ETag)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return latest, false, err
	}
	defer response.Body.Close()
	state.CheckedAt = time.Now()
	if response.StatusCode == http.StatusNotModified {
		latest.Version = state.Latest
		return latest, false, nil
	}
	if response.StatusCode != http.StatusOK {
		return latest, false, fmt.Errorf("the release manifest answered %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return latest, false, err
	}
	if err := json.Unmarshal(raw, &latest); err != nil || latest.Version == "" {
		return latest, false, errors.New("the release manifest is not readable")
	}
	state.ETag = response.Header.Get("ETag")
	state.Latest = latest.Version
	return latest, true, nil
}

// Newer reports whether latest is a release newer than current. A daemon
// built by hand, whose version is not a number, is never behind.
func Newer(current, latest string) bool {
	now, ok := parse(current)
	if !ok {
		return false
	}
	then, ok := parse(latest)
	if !ok {
		return false
	}
	for i := range 3 {
		if then[i] != now[i] {
			return then[i] > now[i]
		}
	}
	return false
}

func parse(version string) (parts [3]int, ok bool) {
	fields := strings.Split(strings.TrimPrefix(strings.TrimSpace(version), "v"), ".")
	if len(fields) != 3 {
		return parts, false
	}
	for i, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil || n < 0 {
			return parts, false
		}
		parts[i] = n
	}
	return parts, true
}

// Installed reports whether this hived is the one the installer put in
// place. A build run from somewhere else is a developer's, and is left
// alone: it is told about updates, not replaced by one.
func Installed() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	return exe == filepath.Join(home, ".local", "bin", "hived")
}

// Notify tells the owner on their desktop, where there is one. It is the
// one prompt the daemon makes, and Watch makes it once per version.
func Notify(version string) {
	title := "HiveWorker " + version + " is available"
	body := "Run: hived update"
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf("display notification %q with title %q", body, title)
		_ = exec.Command("/usr/bin/osascript", "-e", script).Run()
	case "linux":
		if path, err := exec.LookPath("notify-send"); err == nil {
			_ = exec.Command(path, "--app-name=HiveWorker", title, body).Run()
		}
	}
}

// Apply updates the way the machine was installed: it downloads the
// installer from the releases origin and runs it. The installer checks each
// download against its published checksum, swaps the daemon and the image,
// and restarts the service, which ends the daemon that started it when
// detached is true. Detached runs leave their output in a log file whose
// path is returned; attached runs share this process's terminal.
func Apply(ctx context.Context, detached bool) (logPath string, err error) {
	dir, err := os.MkdirTemp("", "hiveworker-update-")
	if err != nil {
		return "", err
	}
	script := filepath.Join(dir, "install.sh")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, Base()+"/install.sh", nil)
	if err != nil {
		return "", err
	}
	response, err := (&http.Client{Timeout: time.Minute}).Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the installer answered %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(string(raw), "#!/bin/sh") {
		return "", errors.New("what was downloaded is not the installer")
	}
	if err := os.WriteFile(script, raw, 0o700); err != nil {
		return "", err
	}

	cmd := exec.Command("/bin/sh", script)
	cmd.Env = append(os.Environ(), "HIVE_RELEASES="+Base())
	if !detached {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		err := cmd.Run()
		os.RemoveAll(dir)
		return "", err
	}
	// Its own session: the service manager stops this daemon's process
	// group when the installer restarts the service, and the installer
	// must outlive that.
	logPath = filepath.Join(dir, "update.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer logFile.Close()
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return logPath, err
	}
	// Wait for it. If it succeeds this daemon is stopped before it hears;
	// if it fails, the daemon carries on and says so.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return logPath, fmt.Errorf("the installer failed: %w (see %s)", err, logPath)
		}
		return logPath, nil
	case <-time.After(15 * time.Minute):
		return logPath, errors.New("the installer is still running after fifteen minutes; see " + logPath)
	case <-ctx.Done():
		return logPath, ctx.Err()
	}
}

// Options is what Watch needs from the daemon.
type Options struct {
	Version string
	Log     *slog.Logger
	// AutoUpdate is the owner's setting, asked each time it matters.
	AutoUpdate func() bool
	// Busy is whether a job is running or held. An update waits for none.
	Busy func() bool
	// Updating is set while an update is pending or under way, so the
	// daemon stops taking jobs; it is cleared if the update fails.
	Updating *atomic.Bool
	// For tests: how long to wait before the first check, and between them.
	First, Every time.Duration
}

// Watch runs until ctx is cancelled: a check soon after start and then once
// a day, each at a slightly random time.
func Watch(ctx context.Context, options Options) {
	first := options.First
	if first == 0 {
		first = time.Minute + rand.N(2*time.Minute)
	}
	every := options.Every
	if every == 0 {
		every = Every
	}
	timer := time.NewTimer(first)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		tick(ctx, options)
		// Within two hours either side of a day, so a fleet spreads out.
		wait := every
		if options.Every == 0 {
			wait += rand.N(4*time.Hour) - 2*time.Hour
		}
		timer.Reset(wait)
	}
}

func tick(ctx context.Context, options Options) {
	state, err := LoadState()
	if err != nil {
		options.Log.Warn("could not read the update state", "error", err)
	}
	latest, _, err := Check(ctx, &state)
	if err != nil {
		options.Log.Warn("could not check for an update", "error", err)
		_ = SaveState(state)
		return
	}
	defer func() { _ = SaveState(state) }()
	if !Newer(options.Version, latest.Version) {
		return
	}
	if state.Notified != latest.Version {
		options.Log.Warn("a newer HiveWorker is available", "installed", options.Version, "available", latest.Version, "update", "hived update")
		Notify(latest.Version)
		state.Notified = latest.Version
	}
	if options.AutoUpdate == nil || !options.AutoUpdate() || !Installed() {
		return
	}
	if !state.Failed.IsZero() && time.Since(state.Failed) < Every {
		return
	}
	// No new jobs from here; wait for the ones in hand, but not forever.
	options.Updating.Store(true)
	deadline := time.Now().Add(6 * time.Hour)
	for options.Busy != nil && options.Busy() {
		if time.Now().After(deadline) || ctx.Err() != nil {
			options.Updating.Store(false)
			return
		}
		select {
		case <-ctx.Done():
			options.Updating.Store(false)
			return
		case <-time.After(10 * time.Second):
		}
	}
	options.Log.Info("updating HiveWorker", "from", options.Version, "to", latest.Version)
	_ = SaveState(state)
	if _, err := Apply(ctx, true); err != nil {
		options.Log.Error("the update did not take; carrying on with this version", "error", err)
		state.Failed = time.Now()
		options.Updating.Store(false)
		return
	}
	// The installer restarted the service, which means this process is
	// about to end, or has ended already as far as the service manager is
	// concerned. Nothing more to do.
}
