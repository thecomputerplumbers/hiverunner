package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
)

// A machine is drained before its daemon is replaced: it stops taking jobs,
// the ones it has are waited for, and only then is the daemon restarted.
// Restarting it at once cancels them, and GitHub runs nothing again by
// itself.
//
// It is done through the owner's own pause setting, because that is what a
// daemon of any age obeys, and through what a running job leaves on disk,
// because that is what a daemon of any age writes. A mark beside the
// settings says the pause is the drain's and not the owner's, so the next
// daemon to start lifts it.

// drainSettle is how long after pausing a look that finds nothing is not
// yet believed: a job the daemon accepted just before has no VM for a few
// seconds.
const drainSettle = 10 * time.Second

func drainMark() (string, error) {
	path, err := settings.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "draining"), nil
}

// drainCommand is `hived drain`, which the installer runs before it
// replaces a running daemon. It returns once the machine has no job, with
// the machine still paused, or fails with the machine as it found it.
func drainCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("drain", flag.ExitOnError)
	limit := flags.Duration("timeout", 6*time.Hour, "give up, and leave the daemon as it is, after this long")
	end := flags.Bool("end", false, "lift the pause a drain put on, for when no daemon started to do it")
	_ = flags.Parse(args)
	if *end {
		return endDrain()
	}
	current, err := settings.Load()
	if err != nil {
		return err
	}
	if !daemonRunning(current) {
		return nil
	}
	dir, err := imageDir(current)
	if err != nil {
		return err
	}
	return drain(ctx, drainOptions{
		Limit: *limit, Settle: drainSettle, Every: 2 * time.Second,
		Jobs: func() int { return jobsInHand(workDir(dir)) },
		Say:  func(text string) { fmt.Println(text) },
	})
}

type drainOptions struct {
	Limit, Settle, Every time.Duration
	// Jobs is how many jobs the running daemon has.
	Jobs func() int
	Say  func(string)
}

func drain(ctx context.Context, options drainOptions) error {
	mark, err := drainMark()
	if err != nil {
		return err
	}
	current, err := settings.Load()
	if err != nil {
		return err
	}
	// Paused by its owner already: nothing to undo, and nothing new has
	// been arriving. Paused by an earlier drain: carried on with.
	ours := false
	settled := time.Now()
	if info, err := os.Stat(mark); err == nil {
		ours = true
		settled = info.ModTime().Add(options.Settle)
	} else if !current.Paused {
		if err := os.WriteFile(mark, []byte("an update is waiting for this machine's jobs to finish\n"), 0o600); err != nil {
			return err
		}
		current.Paused = true
		if err := settings.Save(current); err != nil {
			_ = os.Remove(mark)
			return err
		}
		ours = true
		settled = time.Now().Add(options.Settle)
	}
	undo := func() {
		if ours {
			_ = endDrain()
		}
	}
	deadline := time.Now().Add(options.Limit)
	said, quiet := -1, 0
	for {
		count := options.Jobs()
		if count > 0 && count != said {
			jobs := "1 job"
			if count > 1 {
				jobs = fmt.Sprintf("%d jobs", count)
			}
			options.Say("Waiting for " + jobs + " on this machine to finish before the daemon is restarted. It takes no new ones meanwhile.")
			options.Say("To restart it now, and cancel what it is running: hived update --now")
			said = count
		}
		// Twice running, so that one look between a job ending and the
		// next thing the daemon does is not taken for idle.
		if count == 0 && !time.Now().Before(settled) {
			if quiet++; quiet >= 2 {
				if said > 0 {
					options.Say("No job is running now.")
				}
				return nil
			}
		} else {
			quiet = 0
		}
		if time.Now().After(deadline) {
			undo()
			return fmt.Errorf("this machine still had a job after %s; the daemon was left as it is", options.Limit)
		}
		select {
		case <-ctx.Done():
			undo()
			return errors.New("stopped waiting; the daemon was left as it is")
		case <-time.After(options.Every):
		}
	}
}

// endDrain lifts a pause that a drain put on, and only that: an owner's
// own pause has no mark beside it.
func endDrain() error {
	mark, err := drainMark()
	if err != nil {
		return err
	}
	if _, err := os.Stat(mark); err != nil {
		return nil
	}
	current, err := settings.Load()
	if err != nil {
		return err
	}
	if current.Paused {
		current.Paused = false
		if err := settings.Save(current); err != nil {
			return err
		}
	}
	return os.Remove(mark)
}

// jobsInHand is how many jobs the daemon holding this work directory is
// running, from what they leave where anyone can see.
func jobsInHand(work string) int {
	count := vmJobs(work)
	if runtime.GOOS == "darwin" {
		// A macOS job is a process, started through sudo.
		if out, err := exec.Command("/bin/ps", "-axo", "command=").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				// The job itself, not the sudo that started it.
				if strings.HasSuffix(line, "hived macos-job") && !strings.Contains(line, "sudo") {
					count++
				}
			}
		}
	}
	return count
}

// vmJobs is how many VMs in a work directory have been handed a job. One
// that has carries the time of the handing over in what it was handed; one
// kept warm for the next job does not. An older daemon, which the installer
// may be draining, kept it in a share named share.
func vmJobs(work string) int {
	count := 0
	entries, _ := os.ReadDir(work)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "job-") {
			continue
		}
		for _, handed := range []string{"in", "share"} {
			// Not followed: a guest that could write there may have
			// left a link, and a link is not the host's word.
			if info, err := os.Lstat(filepath.Join(work, entry.Name(), handed, "clock")); err == nil && info.Mode().IsRegular() {
				count++
				break
			}
		}
	}
	return count
}
