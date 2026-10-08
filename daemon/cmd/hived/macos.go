package main

import (
	"context"
	"errors"
	"fmt"
	"os"

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
			return saved(current, "macos-jobs is now on")
		}
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
		} else {
			fmt.Println("This Mac takes macOS jobs.")
		}
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

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
