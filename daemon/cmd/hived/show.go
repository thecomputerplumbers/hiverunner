package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/api"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/cache"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/config"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/host"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/jobs"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/native"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/settings"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/ui"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/update"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/vm"
)

const gib = 1 << 30

// labelFor is the runs-on label for a job size.
func labelFor(vcpus int) string {
	if vcpus == 2 {
		return "hive"
	}
	return "hive-" + strconv.Itoa(vcpus) + "cpu"
}

// refusal is why this machine does not take a job of this size, or "".
func refusal(vcpus int, facts host.Facts, current settings.Settings) string {
	switch {
	case facts.Cores < vcpus+jobs.OwnerCores:
		return fmt.Sprintf("needs %d cores", vcpus+jobs.OwnerCores)
	case !current.Takes(vcpus):
		return "above your max-vcpus"
	}
	return ""
}

func status() error {
	machine, err := config.Load()
	if err != nil {
		return err
	}
	current, err := settings.Load()
	if err != nil {
		return err
	}
	facts, err := host.ReadFacts()
	if err != nil {
		return err
	}
	screen := ui.For(os.Stdout)
	power := host.ReadPower()

	pools := "unknown"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if assignment, err := api.New(machine.Server).Me(ctx, machine.Token); err != nil {
		pools = screen.Dim("could not ask the server")
	} else if len(assignment.Pools) == 0 {
		pools = screen.Dim("none; add this machine to one from the dashboard")
	} else {
		names := make([]string, len(assignment.Pools))
		for i, pool := range assignment.Pools {
			names[i] = pool.Name
		}
		pools = strings.Join(names, ", ")
	}

	daemon, daemonUp := "not running; start it with: hived run", false
	if dir, err := vm.DefaultImageDir(); err != nil {
		daemon = "unknown"
	} else if _, err := vm.LoadImage(dir); err != nil {
		daemon = "no runner image installed, so it cannot run jobs"
	} else if vm.WorkDirInUse(dir + "-jobs") {
		daemon, daemonUp = "running", true
	}

	holding := current.Holding(power, time.Now())
	state := screen.Dot(holding == "") + " taking jobs"
	if holding != "" {
		state = screen.Dot(false) + " not taking jobs: " + holding
	}

	// What the daemon last learned, and a fresh look if that was a while
	// ago; never long enough to hold the rest of the page up.
	release := ""
	if remembered, err := update.LoadState(); err == nil {
		if time.Since(remembered.CheckedAt) > update.Every {
			quick, cancel := context.WithTimeout(ctx, 3*time.Second)
			_, _, _ = update.Check(quick, &remembered)
			cancel()
			_ = update.SaveState(remembered)
		}
		if update.Newer(version, remembered.Latest) {
			release = screen.Honey(remembered.Latest+" is available") + screen.Dim("  update with: hived update")
			if current.AutoUpdate {
				release = screen.Honey(remembered.Latest+" is available") + screen.Dim("  installs itself when idle, or now with: hived update")
			}
		}
	}

	fmt.Println()
	fmt.Print(indent(screen.Masthead(
		"",
		screen.Bold("HiveWorker"),
		"hived "+version,
		screen.Dim(machine.Server),
	)))
	fmt.Println()
	rows := []string{
		field("Name", facts.Name+screen.Dim("  "+facts.Model)),
		field("Hardware", fmt.Sprintf("%d cores, %d GB", facts.Cores, facts.MemoryBytes/gib)),
		field("Hives", pools),
		field("Daemon", screen.Dot(daemonUp)+" "+daemon),
		field("Jobs", state),
	}
	if release != "" {
		rows = append(rows, field("Update", release))
	}
	fmt.Print(indent(screen.Box(strings.ToUpper(thisMachine[:1])+thisMachine[1:], rows...)))
	fmt.Println()
	fmt.Print(indent(screen.Box("What you lend", lending(screen, facts, current)...)))
	fmt.Println()
	fmt.Print(indent(screen.Box("Actions cache", cacheRows(screen, current, false)...)))
	fmt.Println()
	fmt.Print(indent(screen.Box("Your settings", settingRows(screen, current)...)))
	fmt.Println(indent(screen.Dim("Change one with: hived config set max-jobs 2     Stop lending: hived pause")))
	return nil
}

// most is the fullest this machine gets: the jobs that use the most cores
// at once within the owner's limits, by size. Sizes double, so taking the
// largest that still fits each time is the best that can be done.
func most(facts host.Facts, current settings.Settings) []int {
	var jobsAtOnce []int
	free := facts.Cores - jobs.OwnerCores
	for len(jobsAtOnce) < current.MaxJobs {
		taken := 0
		for _, vcpus := range settings.Sizes {
			if vcpus <= free && refusal(vcpus, facts, current) == "" {
				taken = vcpus
			}
		}
		if taken == 0 {
			break
		}
		jobsAtOnce = append(jobsAtOnce, taken)
		free -= taken
	}
	return jobsAtOnce
}

// lending draws how much of the machine its jobs use when it is as full as
// its owner allows, and which job sizes it takes at all.
func lending(screen ui.Screen, facts host.Facts, current settings.Settings) []string {
	cores, memory := 0, 0
	fullest := most(facts, current)
	for _, vcpus := range fullest {
		cores += vcpus
		memory += int(vm.GuestMemoryBytes(vcpus, uint64(facts.MemoryBytes)) / gib)
	}
	atOnce := "One job at a time."
	if current.MaxJobs > 1 {
		atOnce = fmt.Sprintf("Up to %d jobs at a time.", current.MaxJobs)
	}
	const bar = 30
	rows := []string{
		field("Cores", screen.Bar(cores, facts.Cores, bar)+fmt.Sprintf("  %d of %d", cores, facts.Cores)),
		field("Memory", screen.Bar(memory, int(facts.MemoryBytes/gib), bar)+fmt.Sprintf("  %d of %d GB", memory, facts.MemoryBytes/gib)),
		field("", screen.Dim("At its fullest. "+atOnce)),
		"",
	}
	for _, vcpus := range settings.Sizes {
		size := fmt.Sprintf("%d vCPU  %2d GB", vcpus, vm.GuestMemoryBytes(vcpus, uint64(facts.MemoryBytes))/gib)
		verdict := screen.Dot(true) + " takes these"
		if reason := refusal(vcpus, facts, current); reason != "" {
			verdict = screen.Dot(false) + " " + screen.Dim(reason)
		}
		rows = append(rows, ui.Pad(screen.Honey(labelFor(vcpus)), 12)+ui.Pad(size, 18)+verdict)
	}
	return rows
}

func settingRows(screen ui.Screen, current settings.Settings) []string {
	limit := "auto"
	if current.MaxVCPUs != 0 {
		limit = strconv.Itoa(current.MaxVCPUs)
	}
	hours := "any"
	if current.Hours != "" {
		hours = current.Hours
	}
	cacheSize := strconv.Itoa(current.CacheGB) + " GB"
	if current.CacheGB == 0 {
		cacheSize = "auto"
		if dir, err := cache.DefaultDir(); err == nil {
			cacheSize = "auto, " + bytesText(cacheLimit(current, dir))
		}
	}
	row := func(key, value, meaning string) string {
		return ui.Pad(key, 12) + ui.Pad(screen.Honey(value), 16) + screen.Dim(meaning)
	}
	return []string{
		row("max-jobs", strconv.Itoa(current.MaxJobs), "how many jobs run at once"),
		row("max-vcpus", limit, "the largest job "+thisMachine+" takes"),
		row("warm", onOff(current.Warm), "keep one VM booted so jobs start sooner"),
		row("battery", onOff(current.Battery), "take jobs while unplugged"),
		row("hours", hours, "when jobs may start"),
		row("cache", onOff(current.Cache), "keep an Actions cache on "+thisMachine),
		row("cache-size", cacheSize, "the most disk the cache may use"),
		row("macos-jobs", onOff(current.MacOSJobs), "take hive-macos jobs on this Mac itself"),
		row("auto-update", onOff(current.AutoUpdate), "update to new releases when idle"),
		row("paused", yesNo(current.Paused), "hived pause, hived resume"),
	}
}

func field(name, value string) string { return ui.Pad(name, 12) + value }

func indent(block string) string {
	lines := strings.Split(strings.TrimSuffix(block, "\n"), "\n")
	return "  " + strings.Join(lines, "\n  ") + "\n"
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func yesNo(yes bool) string {
	if yes {
		return "yes"
	}
	return "no"
}

const configUsage = `hived config                    Show this machine's settings
hived config set <name> <value>  Change one

  max-jobs   1 to 8              how many jobs run at once
  max-vcpus  2, 4, 8 or auto     the largest job this machine takes
  warm       on or off           keep one VM booted so jobs start sooner
  battery    on or off           take jobs while unplugged
  hours      22:00-07:00 or any  when jobs may start, in local time
  cache      on or off           keep an Actions cache on this machine
  cache-size 1 to 2000 or auto   the most disk the cache may use, in GB
  macos-jobs on or off           take hive-macos jobs, run on this Mac itself
                                 as a separate user; hived macos setup first
  auto-update on or off          update to new releases when no job is running;
                                 off, hived status and a notification say so
`

func configure(args []string) error {
	current, err := settings.Load()
	if err != nil {
		return err
	}
	screen := ui.For(os.Stdout)
	switch {
	case len(args) == 0:
		fmt.Println()
		fmt.Print(indent(screen.Box("Your settings", settingRows(screen, current)...)))
		if path, err := settings.Path(); err == nil {
			fmt.Println(indent(screen.Dim("Kept in " + path)))
		}
		fmt.Println(indent(screen.Dim("Change one with: hived config set max-vcpus 4")))
		return nil
	case len(args) == 3 && args[0] == "set":
		if err := current.Set(args[1], args[2]); err != nil {
			return err
		}
		if args[1] == "macos-jobs" && current.MacOSJobs {
			// On means jobs run here as another user: the Mac has to be
			// set up for that first, and the setup command is what says
			// what it will do.
			runner, err := native.New(nil)
			if err != nil {
				return err
			}
			if report := runner.Status(); report.Problem != "" {
				return errors.New(report.Problem)
			}
		}
		return saved(current, args[1]+" is now "+strings.ToLower(args[2]))
	case args[0] == "help" || args[0] == "-h" || args[0] == "--help":
		fmt.Print(configUsage)
		return nil
	}
	return errors.New("usage: hived config set <name> <value>; see hived config help")
}

func pause(paused bool) error {
	current, err := settings.Load()
	if err != nil {
		return err
	}
	current.Paused = paused
	if paused {
		return saved(current, "Paused. A job already running finishes; no new ones start. Undo with: hived resume")
	}
	return saved(current, "Resumed.")
}

func saved(next settings.Settings, said string) error {
	if err := settings.Save(next); err != nil {
		return err
	}
	fmt.Println(said)
	fmt.Println("A running daemon picks this up within a few seconds.")
	return nil
}
