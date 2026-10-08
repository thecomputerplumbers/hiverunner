package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/api"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/config"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/native"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/settings"
)

const macosUsage = `hived macos setup    Make this Mac take macOS jobs (runs-on: hive-macos)
hived macos status   Say whether it can, and what is missing
hived macos remove   Undo setup: the sudo rule and the ` + native.User + ` user go

A macOS job runs on this Mac itself, as the ` + native.User + ` user, not in a VM.
It can use Xcode and whatever else is installed here. Only hives whose owners
allow macOS jobs send any. Turn it off with: hived config set macos-jobs off
`

// macos is `hived macos ...`: setting a Mac up for jobs that need macOS.
func macos(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Print(macosUsage)
		return nil
	}
	options := native.SetupOptions{Stdin: os.Stdin, Stdout: os.Stdout}
	for _, arg := range args[1:] {
		if arg == "--yes" || arg == "-y" {
			options.Yes = true
		}
	}
	switch args[0] {
	case "setup":
		if err := native.Setup(ctx, options); err != nil {
			return err
		}
		current, err := settings.Load()
		if err != nil {
			return err
		}
		if !current.MacOSJobs {
			current.MacOSJobs = true
			if err := saved(current, "macos-jobs is now on"); err != nil {
				return err
			}
		}
		fmt.Println()
		hiveLines(ctx)
		return nil
	case "status":
		runner, err := native.New(nil)
		if err != nil {
			return err
		}
		report := runner.Status()
		current, _ := settings.Load()
		fmt.Println(field("platform", report.Platform))
		fmt.Println(field("xcode", orDash(report.Xcode)))
		fmt.Println(field("user", yesNo(report.UserExists)))
		fmt.Println(field("sudo rule", yesNo(report.Sudo)))
		fmt.Println(field("runner", orDash(report.Runner)))
		fmt.Println(field("macos-jobs", onOff(current.MacOSJobs)))
		if report.Problem != "" {
			return errors.New(report.Problem)
		}
		if !current.MacOSJobs {
			fmt.Println("Ready, but turned off. Turn on with: hived config set macos-jobs on")
			return nil
		}
		fmt.Println("This Mac takes macOS jobs.")
		fmt.Println()
		hiveLines(ctx)
		return nil
	case "remove":
		if err := native.Remove(ctx, options); err != nil {
			return err
		}
		current, err := settings.Load()
		if err != nil {
			return err
		}
		if current.MacOSJobs {
			current.MacOSJobs = false
			return saved(current, "macos-jobs is now off")
		}
		return nil
	case "help", "-h", "--help":
		fmt.Print(macosUsage)
		return nil
	}
	return errors.New("usage: hived macos setup|status|remove")
}

// hiveLines says, for each hive this machine serves, whether it will send
// macOS jobs: that is the other half of the switch, and it is not on this
// Mac. Said as well as it can be when the server cannot be asked.
func hiveLines(ctx context.Context) {
	machine, err := config.Load()
	if err != nil {
		fmt.Println("Jobs come only from hives that allow macOS jobs; an owner turns that on in the hive's settings.")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	assignment, err := api.New(machine.Server).Me(ctx, machine.Token)
	if err != nil || len(assignment.Pools) == 0 {
		fmt.Printf("Jobs come only from hives that allow macOS jobs; an owner turns that on at %s/settings\n", machine.Server)
		return
	}
	fmt.Println("Whether the hives this Mac serves will send macOS jobs:")
	for _, pool := range assignment.Pools {
		switch {
		case pool.MacOSJobs == nil:
			fmt.Printf("  %-24s unknown\n", pool.Name)
		case *pool.MacOSJobs:
			fmt.Printf("  %-24s yes\n", pool.Name)
		default:
			fmt.Printf("  %-24s not yet: an owner of %s turns on \"Allow macOS jobs\" at %s/settings\n", pool.Name, pool.Name, machine.Server)
		}
	}
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
