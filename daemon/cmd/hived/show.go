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
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/config"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/host"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/settings"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/ui"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/vm"
)

// ownerCores is what every job leaves for the machine's owner. The pool
// applies the same rule when it chooses a machine.
const ownerCores = 2

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
	case facts.Cores < vcpus+ownerCores:
		return fmt.Sprintf("needs %d cores", vcpus+ownerCores)
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

	fmt.Println()
	fmt.Print(indent(screen.Masthead(
		"",
		screen.Bold("HiveWorker"),
		"hived "+version,
		screen.Dim(machine.Server),
	)))
	fmt.Println()
	fmt.Print(indent(screen.Box("This Mac",
		field("Name", facts.Name+screen.Dim("  "+facts.Model)),
		field("Hardware", fmt.Sprintf("%d cores, %d GB", facts.Cores, facts.MemoryBytes/gib)),
		field("Pools", pools),
		field("Daemon", screen.Dot(daemonUp)+" "+daemon),
		field("Jobs", state),
	)))
	fmt.Println()
	fmt.Print(indent(screen.Box("What you lend", lending(screen, facts, current)...)))
	fmt.Println()
	fmt.Print(indent(screen.Box("Your settings", settingRows(screen, current)...)))
	fmt.Println(indent(screen.Dim("Change one with: hived config set max-vcpus 4    Stop lending: hived pause")))
	return nil
}

// lending draws how much of the machine the largest job it takes would use,
// and which job sizes it takes at all.
func lending(screen ui.Screen, facts host.Facts, current settings.Settings) []string {
	largest := 0
	for _, vcpus := range settings.Sizes {
		if refusal(vcpus, facts, current) == "" {
			largest = vcpus
		}
	}
	memory := 0
	if largest > 0 {
		memory = int(vm.GuestMemoryBytes(largest, uint64(facts.MemoryBytes)) / gib)
	}
	const bar = 30
	rows := []string{
		field("Cores", screen.Bar(largest, facts.Cores, bar)+fmt.Sprintf("  %d of %d", largest, facts.Cores)),
		field("Memory", screen.Bar(memory, int(facts.MemoryBytes/gib), bar)+fmt.Sprintf("  %d of %d GB", memory, facts.MemoryBytes/gib)),
		field("", screen.Dim("At most, to the largest job it takes. One job at a time.")),
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
	row := func(key, value, meaning string) string {
		return ui.Pad(key, 12) + ui.Pad(screen.Honey(value), 14) + screen.Dim(meaning)
	}
	return []string{
		row("max-vcpus", limit, "the largest job this Mac takes"),
		row("warm", onOff(current.Warm), "keep one VM booted so jobs start sooner"),
		row("battery", onOff(current.Battery), "take jobs while unplugged"),
		row("hours", hours, "when jobs may start"),
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

  max-vcpus  2, 4, 8 or auto     the largest job this Mac takes
  warm       on or off           keep one VM booted so jobs start sooner
  battery    on or off           take jobs while unplugged
  hours      22:00-07:00 or any  when jobs may start, in local time
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
