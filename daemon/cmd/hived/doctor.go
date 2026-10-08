package main

import (
	"fmt"
	"os"
	"time"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/config"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/host"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/settings"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/ui"
)

// asleep is what to say when the owner lends with the lid closed but the
// machine would not stay awake closed, and what to do about it. Both are ""
// when there is nothing to say.
func asleep(current settings.Settings, power host.Power) (problem, fix string) {
	if !current.Lid || !power.HasLid {
		return "", ""
	}
	switch awake, known := host.StaysAwake(); {
	case !known:
		return "lid is on, but hived could not check that " + thisMachine + " stays awake closed", host.StayAwake
	case !awake && power.LidClosed:
		// Closed, awake and able to say so: a display is holding it awake.
		return "lid is on, but closed " + thisMachine + " is awake only while a display and power are attached", host.StayAwake
	case !awake:
		return "lid is on, but " + thisMachine + " sleeps when its lid closes, and takes no jobs asleep", host.StayAwake
	}
	return "", ""
}

// doctor is `hived doctor`: whether this machine keeps taking jobs with
// nobody at it, through a closed lid and a restart. It fails if not, so a
// script setting a machine up can ask. What the owner may have chosen, such
// as FileVault, is said and does not fail it.
func doctor() error {
	current, err := settings.Load()
	if err != nil {
		return err
	}
	power := host.ReadPower()
	var checks []host.Check

	if _, err := config.Load(); err != nil {
		checks = append(checks, host.Check{Name: "sign-in", Found: "this machine is not in a hive", Fix: "hived login"})
	} else {
		checks = append(checks, host.Check{Name: "sign-in", Found: "in a hive", OK: true})
	}
	if home, err := os.UserHomeDir(); err == nil {
		definition, _ := servicePaths(home)
		if _, err := os.Stat(definition); err != nil {
			checks = append(checks, host.Check{Name: "service", Found: "not installed, so hived runs only while you run it", Fix: "run the installer again"})
		} else {
			checks = append(checks, host.Check{Name: "service", Found: "installed", OK: true})
		}
	}
	if reason, fix := current.Holding(power, time.Now()), current.Fix(power, time.Now()); reason != "" {
		checks = append(checks, host.Check{Name: "jobs", Found: "not taking jobs: " + reason, Fix: fix})
	} else {
		checks = append(checks, host.Check{Name: "jobs", Found: "taking jobs", OK: true})
	}
	switch {
	case !power.OnBattery:
		checks = append(checks, host.Check{Name: "power", Found: "plugged in", OK: true})
	case current.Battery:
		checks = append(checks, host.Check{Name: "power", Found: "on battery, and taking jobs until it runs out", Note: true, Fix: "plug it in"})
	default:
		checks = append(checks, host.Check{Name: "power", Found: "on battery", Fix: "plug it in or run hived config set battery on"})
	}
	if power.HasLid {
		if current.Lid {
			checks = append(checks, host.Check{Name: "lid", Found: "takes jobs with the lid closed", OK: true})
		} else {
			checks = append(checks, host.Check{Name: "lid", Found: "jobs stop when the lid closes", Fix: "hived config set lid on"})
		}
	}
	checks = append(checks, host.Unattended()...)

	screen := ui.For(os.Stdout)
	var rows []string
	problems := 0
	for _, check := range checks {
		const name = 14
		for i, line := range wrap(check.Found, ui.Inner-name-2) {
			if i == 0 {
				rows = append(rows, screen.Dot(check.OK || check.Note)+" "+ui.Pad(check.Name, name)+line)
				continue
			}
			rows = append(rows, "  "+ui.Pad("", name)+line)
		}
		if !check.OK {
			if !check.Note {
				problems++
			}
			for _, line := range wrap(check.Fix, ui.Inner-name-2) {
				rows = append(rows, "  "+ui.Pad("", name)+screen.Dim(line))
			}
		}
	}
	fmt.Println()
	fmt.Print(indent(screen.Box("Unattended", rows...)))
	switch problems {
	case 0:
		fmt.Println(indent(screen.Dim(thisMachine + " keeps taking jobs with nobody at it.")))
		return nil
	case 1:
		return fmt.Errorf("one thing would stop %s taking jobs with nobody at it", thisMachine)
	}
	return fmt.Errorf("%d things would stop %s taking jobs with nobody at it", problems, thisMachine)
}
