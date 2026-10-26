package main

import (
	"context"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/host"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
)

// A Mac's launch agent runs the daemon, and so every job and VM it starts,
// in the band its owner's priority setting names: background, for a laptop
// lending what it does not use, or standard, for a Mac kept for building
// (#120). launchd applies it as it starts the daemon, and children inherit
// it, so the daemon cannot change its own: it rewrites the agent's
// definition and has launchd start it again, once it has no job.

// priorityName is the priority setting as hived config shows it.
func priorityName(current settings.Settings) string {
	if current.RunsInBackground() {
		return settings.PriorityBackground
	}
	return settings.PriorityStandard
}

// priorityCheck is what hived status and hived doctor say of a Mac's
// priority. background is how launchd runs the daemon now, known whether
// that could be read. A Mac plugged in and lending with its lid closed, or
// a desktop plugged in, is kept for building, and is told how to stop
// holding its jobs back.
func priorityCheck(current settings.Settings, background, known bool, power host.Power) host.Check {
	check := host.Check{Name: "priority", OK: true}
	if !known {
		background = current.RunsInBackground()
	}
	switch {
	case known && background != current.RunsInBackground():
		check.Found = priorityName(current) + ", once the daemon starts again, when it has no job"
	case !background || !current.RunsInBackground():
		check.Found = "standard: jobs use every core"
	default:
		check.Found = "background: jobs use the efficiency cores, with the disk throttled"
		if !power.OnBattery && (current.Lid || !power.HasLid) {
			check.OK, check.Note = false, true
			check.Fix = "for a Mac kept for building: hived config set priority standard"
		}
	}
	return check
}

// keepPriority starts the daemon again, as the launch agent at the owner's
// priority, whenever the two differ and no job is running. updating is set
// first, so the pools offer it no job while it stops. It returns once it
// has asked launchd, or when ctx ends.
func keepPriority(ctx context.Context, log *slog.Logger, live func() settings.Settings, busy func() bool, updating *atomic.Bool) {
	waiting := false
	for {
		background := live().RunsInBackground()
		if err := setServicePriority(background); err != nil {
			log.Warn("could not write the priority setting into the launch agent", "error", err)
		} else if running, pid, ok := serviceSpawn(); ok && pid == os.Getpid() && running != background {
			if !waiting {
				log.Info("starting again at the priority setting once no job is running", "priority", priorityName(live()))
				waiting = true
			}
			if restartWhenIdle(busy, updating) {
				if err := restartService(); err != nil {
					log.Error("could not start again at the priority setting", "error", err)
					updating.Store(false)
				}
				return
			}
		} else {
			waiting = false
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}

// restartWhenIdle reports whether the daemon may stop now: no job, and none
// on the way, with updating set so that none is offered. An update under
// way has the machine already.
func restartWhenIdle(busy func() bool, updating *atomic.Bool) bool {
	if busy() || !updating.CompareAndSwap(false, true) {
		return false
	}
	// A job accepted as the flag went up is the one to wait for.
	if busy() {
		updating.Store(false)
		return false
	}
	return true
}

// processTypeKey is the launch agent's ProcessType as the installer writes
// it, with its value.
var processTypeKey = regexp.MustCompile(`(<key>ProcessType</key>\s*<string>)([A-Za-z]*)(</string>)`)

// withProcessType is a launch agent's definition with its ProcessType
// changed, and nothing else: an installer that writes the same definition
// finds it unchanged, and so leaves the daemon running. ok is false when
// it has no ProcessType written the installer's way.
func withProcessType(definition, processType string) (string, bool) {
	if len(processTypeKey.FindAllStringIndex(definition, -1)) != 1 {
		return definition, false
	}
	return processTypeKey.ReplaceAllString(definition, "${1}"+processType+"${3}"), true
}

// spawnBackground reads launchctl print: whether launchd started the
// service in the background band, and the pid it is running as, or 0.
func spawnBackground(printed string) (background bool, pid int) {
	for _, line := range strings.Split(printed, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), " = ")
		if !found {
			continue
		}
		switch key {
		case "pid":
			pid, _ = strconv.Atoi(strings.TrimSpace(value))
		case "spawn type":
			background = strings.HasPrefix(strings.TrimSpace(value), "background")
		}
	}
	return background, pid
}
