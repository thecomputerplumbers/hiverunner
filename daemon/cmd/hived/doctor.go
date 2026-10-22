package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/api"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/config"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/connection"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/host"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/native"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/ui"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/vm"
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
// as FileVault, is said and does not fail it, and nor does anything it says
// about how exposed the machine is to its jobs and its network. With --sudo
// it reads what only root can, saying so first; it never uses sudo
// otherwise, not even on credentials sudo has cached.
func doctor(args []string) error {
	withSudo := false
	for _, arg := range args {
		switch arg {
		case "--sudo":
			withSudo = true
		default:
			return errors.New("usage: hived doctor [--sudo]")
		}
	}
	current, err := settings.Load()
	if err != nil {
		return err
	}
	power := host.ReadPower()
	var checks []host.Check

	if machine, err := config.Load(); err != nil {
		checks = append(checks, host.Check{Name: "sign-in", Found: "this machine is not in a hive", Fix: "hived login"})
	} else {
		checks = append(checks, reach(machine)...)
	}
	if home, err := os.UserHomeDir(); err == nil {
		definition, _ := servicePaths(home)
		if _, err := os.Stat(definition); err != nil {
			checks = append(checks, host.Check{Name: "service", Found: "not installed, so hived runs only while you run it", Fix: "run the installer again"})
		} else {
			checks = append(checks, host.Check{Name: "service", Found: "installed", OK: true})
			checks = append(checks, serviceChecks(definition)...)
		}
	}
	if reason, fix := current.Holding(power, time.Now()), current.Fix(power, time.Now()); reason != "" {
		checks = append(checks, host.Check{Name: "jobs", Found: "not taking jobs: " + reason, Fix: fix})
		// Said as well, so that resuming is not the way to find it out.
		if reason, fix := cannotRunVMs(); reason != "" {
			checks = append(checks, host.Check{Name: "VMs", Found: "cannot start one: " + reason, Fix: fix})
		}
	} else if reason, fix := cannotRunVMs(); reason != "" {
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
	if facts, err := host.ReadFacts(); err == nil {
		if problem, fix := overcommit(facts, current, host.AvailableMemory()); problem != "" {
			checks = append(checks, host.Check{Name: "memory", Found: problem, Fix: fix})
		}
		if problem, fix := reserveProblem(facts, current); problem != "" {
			checks = append(checks, host.Check{Name: "reserve", Found: problem, Fix: fix})
		}
	}
	// A disk the owner keeps the image or the cache on has to be attached.
	if gone := absent(current); len(gone) > 0 {
		checks = append(checks, host.Check{Name: "storage", Found: strings.Join(gone, " and ") + " is not there, so nothing kept in it can be used",
			Fix: "attach its disk, or run hived config set data-dir default (cache-dir for the cache)"})
	} else if current.DataDir != "" || current.CacheDir != "" {
		kept := []string{}
		if current.DataDir != "" {
			kept = append(kept, "the image in "+current.DataDir)
		}
		if current.CacheDir != "" {
			kept = append(kept, "the cache in "+current.CacheDir)
		}
		checks = append(checks, host.Check{Name: "storage", Found: strings.Join(kept, ", "), OK: true})
	}
	checks = append(checks, host.Unattended()...)

	screen := ui.For(os.Stdout)
	rows, problems := checkRows(screen, checks, true)
	fmt.Println()
	fmt.Print(indent(screen.Box("Unattended", rows...)))
	if problems == 0 {
		fmt.Println(indent(screen.Dim(thisMachine + " keeps taking jobs with nobody at it.")))
	}
	exposure := host.Exposure{Fence: jobsFence(current)}
	if withSudo && runtime.GOOS != "windows" {
		exposure.Sudo = authorise()
	}
	// Every warning here is a note, so a note is drawn as what it is.
	if exposed, _ := checkRows(screen, append(host.Exposed(exposure), macOSJobs(current, runtime.GOOS, macOSInVM(current))...), false); len(exposed) > 0 {
		fmt.Print(indent(screen.Box("Security", exposed...)))
	}
	switch problems {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("one thing would stop %s taking jobs with nobody at it", thisMachine)
	}
	return fmt.Errorf("%d things would stop %s taking jobs with nobody at it", problems, thisMachine)
}

// checkRows draws checks as a box's rows, with how many of them are wrong.
// notesPass draws a note as a pass, as in a box where a note is the
// exception.
func checkRows(screen ui.Screen, checks []host.Check, notesPass bool) (rows []string, problems int) {
	for _, check := range checks {
		const name = 14
		for i, line := range wrap(check.Found, ui.Inner-name-2) {
			if i == 0 {
				rows = append(rows, screen.Dot(check.OK || check.Note && notesPass)+" "+ui.Pad(check.Name, name)+line)
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
	return rows, problems
}

// authorise is --sudo: it says that sudo is about to be used and what for,
// and has sudo ask for a password if it would. Whether sudo may be used.
func authorise() bool {
	fmt.Println()
	fmt.Println("Reading the firewall rules and sshd's settings with sudo, as --sudo asks.")
	validate := exec.Command("sudo", "-v")
	validate.Stdin, validate.Stdout, validate.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := validate.Run(); err != nil {
		fmt.Println("sudo refused, so they are read as this user can.")
		return false
	}
	return true
}

// jobsFence is what keeps a job's VM off the networks around this machine,
// for the network check: the one network row, in the Security box.
func jobsFence(current settings.Settings) host.Fence {
	return networkOf(current, vm.FencesNetwork).fenced()
}

// macOSInVM is whether a hive-macos job runs in a throwaway VM of its own
// rather than as native.User on the Mac. It cannot yet.
func macOSInVM(settings.Settings) bool { return false }

// macOSJobs is the macos jobs check. A macOS job runs as the one native.User
// with the one home, so what a job leaves there, such as a package it
// installed, is still there for the next job, whichever hive sends it. In a
// VM of its own, it leaves nothing for the next.
func macOSJobs(current settings.Settings, system string, inVM bool) []host.Check {
	switch {
	case system != "darwin":
		return nil
	case current.MacOSJobs && inVM:
		return []host.Check{{Name: "macos jobs", Found: "on: each macOS job runs in a throwaway VM cloned from the image, and leaves nothing for the next", OK: true}}
	case current.MacOSJobs:
		return []host.Check{{Name: "macos jobs", Found: "on: every macOS job runs as " + native.User + ", and finds what the jobs before it left in its home",
			Note: true, Fix: "serve only hives whose macOS jobs may share a home, or hived config set macos-jobs off"}}
	}
	return []host.Check{{Name: "macos jobs", Found: "off", OK: true}}
}

// reach is whether this machine can find and talk to the server and the
// coordinator its jobs arrive through. A machine that cannot takes no jobs
// and, but for this, says so only in its log.
func reach(machine config.Machine) []host.Check {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	const network = "check this machine's network and its DNS; nothing needs restarting, the daemon keeps trying"
	name := func(address string) string {
		if parsed, err := url.Parse(address); err == nil && parsed.Hostname() != "" {
			return parsed.Hostname()
		}
		return address
	}
	// Looked up first and by itself, so a name that does not resolve is
	// said to be that and not a server that is down.
	resolves := func(hostname string) bool {
		if net.ParseIP(hostname) != nil || hostname == "localhost" {
			return true
		}
		_, err := net.DefaultResolver.LookupHost(ctx, hostname)
		return err == nil
	}
	server := name(machine.Server)
	if !resolves(server) {
		return []host.Check{{Name: "server", Found: "cannot look up " + server + ": DNS has no answer for it here", Fix: network}}
	}
	assignment, err := api.New(machine.Server).Me(ctx, machine.Token)
	switch {
	case errors.Is(err, api.ErrUnknownMachine):
		return []host.Check{{Name: "sign-in", Found: "the server no longer knows this machine", Fix: "hived logout, then hived login"}}
	case err != nil:
		return []host.Check{{Name: "server", Found: "cannot reach " + server + ": " + connection.Reason(err.Error()), Fix: network}}
	}
	checks := []host.Check{{Name: "server", Found: "reaches " + server, OK: true}}
	if len(assignment.Pools) == 0 {
		checks = append(checks, host.Check{Name: "sign-in", Found: "signed in, but in no hive", Fix: "hived link"})
	} else {
		checks = append(checks, host.Check{Name: "sign-in", Found: "in a hive", OK: true})
	}
	if tunnel := name(assignment.TunnelURL); tunnel != server && !resolves(tunnel) {
		checks = append(checks, host.Check{Name: "tunnel", Found: "cannot look up " + tunnel + ", which jobs arrive through: DNS has no answer for it here", Fix: network})
		return checks
	}
	// What the running daemon says of each hive's tunnel.
	if remembered, err := connection.Load(); err == nil && remembered.Live() {
		for _, hive := range remembered.Hives {
			switch {
			case hive.Connected:
				checks = append(checks, host.Check{Name: "tunnel", Found: "connected to " + hive.Name + " since " + when(hive.Since, time.Now()), OK: true})
			case hive.Error != "":
				checks = append(checks, host.Check{Name: "tunnel", Found: "not connected to " + hive.Name + " since " + when(hive.Since, time.Now()) + ": " + connection.Reason(hive.Error), Fix: network})
			}
		}
	}
	return checks
}

// cannotRunVMs is why this machine cannot start a VM right now and what
// would put it right, or two empty strings if it can. The daemon declines
// jobs for as long as it is so, whatever the owner's settings say.
func cannotRunVMs() (reason, fix string) {
	remembered, _ := connection.Load()
	return whyNoVMs(vm.Preflight(), remembered)
}

// whyNoVMs is cannotRunVMs from what this shell found and what the running
// daemon last said. The two differ when the service may not open what the
// shell may: a user's services keep the groups the user had when they
// started, so a kvm group added since reaches the shell and not the daemon.
func whyNoVMs(here error, daemon connection.State) (reason, fix string) {
	if here != nil {
		// The hypervisor's complaints say what to do after a colon.
		reason, fix, _ = strings.Cut(here.Error(), ": ")
		return reason, fix
	}
	if daemon.Unusable == "" || !daemon.Live() {
		return "", ""
	}
	said := strings.TrimPrefix(daemon.Unusable, "this machine cannot run a VM: ")
	reason, fix, _ = strings.Cut(said, ": ")
	return "the running daemon, though not this shell, finds " + reason, fix
}
