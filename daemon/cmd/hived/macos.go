package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/api"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/config"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/macvm"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/native"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
)

const macosUsage = `hived macos setup    Make this Mac take macOS jobs (runs-on: hive-macos)
hived macos status   Say whether it can, and what is missing
hived macos remove   Undo setup: the sudo rule and the ` + native.User + ` user go
hived macos image    Make and keep the image each job's macOS VM is cloned
                     from, for macos-isolation vm; hived macos image help

A macOS job runs on this Mac itself, as the ` + native.User + ` user, not in a VM.
It can use Xcode and whatever else is installed here. Only hives whose owners
allow macOS jobs send any. Turn it off with: hived config set macos-jobs off

With hived config set macos-isolation vm, each job runs in a throwaway macOS
VM instead, cloned from an image you make once and deleted after the job.
`

// imageSecrets is said wherever the owner sets the image up: whatever is
// left in it, every job has, as root in its clone.
const imageSecrets = `Every job is root in a clone of this image, so anything left in it is every
job's to read. Never sign in to an Apple Account or iCloud in it; get Xcode
through --share, not the App Store. Give the runner user a random password
you use nowhere else. Leave no keys, tokens or keychain items in it.
`

const macosImageUsage = `Experimental: a VM per job has not yet run on real hardware.

hived macos image                          Say what the image is, and whether jobs can use it
hived macos image create --ipsw <file>     Install macOS from a restore image on this Mac
                         [--disk-gb 150]   into a new image; nothing is downloaded
hived macos image open [--share <folder>]  Start the image in a window, to set it up by hand
hived macos image seal                     Take its digest and fetch GitHub's runner beside it;
                                           jobs use only a sealed image
hived macos image remove [--yes]           Delete it

Making one, once:

  1. hived macos image create --ipsw ~/Downloads/UniversalMac_26.0_Restore.ipsw
  2. hived macos image open --share ~/Downloads
     In the window:
     - Go through Setup Assistant. Make an admin user named runner, with a
       random password you use nowhere else. Skip the Apple Account, and
       leave FileVault off: automatic login needs it off.
       Turn on no sharing service: two jobs' VMs share one network.
     - System Settings > Users & Groups: log in automatically as runner.
     - Xcode: expand the .xip from /Volumes/My Shared Files/files into
       /Applications (or share this Mac's /Applications and copy Xcode.app),
       then: sudo xcode-select -s /Applications/Xcode.app
             sudo xcodebuild -license accept && sudo xcodebuild -runFirstLaunch
             xcodebuild -downloadPlatform iOS
     - What every job needs, such as Homebrew, then:
             brew install cocoapods node@24 openjdk@17 && corepack enable
     - The guest agent: sudo "/Volumes/My Shared Files/hive/install.sh"
       It also turns sharing services off and has the firewall block all
       incoming connections.
     - Shut it down from the Apple menu.
  3. hived macos image seal
  4. hived config set macos-isolation vm
     hived config set macos-jobs on

Each job gets a clone of the image with a machine identifier of its own, and
reads the image's digest as HIVE_IMAGE_DIGEST. One runs at a time unless
max-macos-vms is 2, the most Apple's licence allows. Opening the image again changes it; seal it after,
and after moving data-dir to another disk. The digest says which image a job
ran on; this Mac computes it, so it vouches for nothing beyond that.

` + imageSecrets

// macos is `hived macos ...`: setting a Mac up for jobs that need macOS.
func macos(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Print(macosUsage)
		return nil
	}
	if args[0] == "image" {
		return macosImage(ctx, args[1:])
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
		native.SpotlightAdvice(os.Stdout, current.SpotlightExclude)
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
		current, _ := settings.Load()
		if current.MacOSInVM() {
			fmt.Println(field("isolation", "a macOS VM per job"))
			fmt.Println(field("macos-jobs", onOff(current.MacOSJobs)))
			if err := macosImageStatus(current); err != nil {
				return err
			}
			if !current.MacOSJobs {
				fmt.Println("Ready, but turned off. Turn on with: hived config set macos-jobs on")
				return nil
			}
			fmt.Println("This Mac takes macOS jobs, each in a VM of its own.")
			fmt.Println()
			hiveLines(ctx)
			return nil
		}
		runner, err := native.New(nil)
		if err != nil {
			return err
		}
		report := runner.Status()
		fmt.Println(field("isolation", "the "+native.User+" user"))
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
	return errors.New("usage: hived macos setup|status|remove|image")
}

// macosImage is `hived macos image ...`: the image each job's macOS VM is
// cloned from.
func macosImage(ctx context.Context, args []string) error {
	current, err := settings.Load()
	if err != nil {
		return err
	}
	dir, err := macosImageDir(current)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return macosImageStatus(current)
	}
	flags := flag.NewFlagSet("macos image "+args[0], flag.ExitOnError)
	ipsw := flags.String("ipsw", "", "a macOS restore image (.ipsw) on this Mac")
	diskGB := flags.Int("disk-gb", macvm.DefaultDiskGB, "the most the image's disk can hold, in gigabytes")
	share := flags.String("share", "", "a folder of yours the VM can read, such as one holding Xcode")
	yes := flags.Bool("yes", false, "do not ask")
	_ = flags.Parse(args[1:])
	switch args[0] {
	case "create":
		if *ipsw == "" {
			return errors.New("usage: hived macos image create --ipsw <file>; Apple publishes restore images for Apple Silicon Macs")
		}
		if *diskGB < 64 || *diskGB > 4096 {
			return errors.New("--disk-gb takes a number of gigabytes from 64 to 4096")
		}
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return err
		}
		if err := macvm.Create(ctx, macvm.CreateOptions{IPSW: *ipsw, Dir: dir, DiskGB: *diskGB, Say: os.Stdout}); err != nil {
			return err
		}
		fmt.Println("macOS is installed in " + dir + ". Next, set it up by hand:")
		fmt.Println("  hived macos image open --share <a folder holding Xcode>")
		fmt.Println("and see hived macos image help for what to do in it.")
		return nil
	case "open":
		if *share != "" {
			if info, err := os.Stat(*share); err != nil || !info.IsDir() {
				return fmt.Errorf("%s is not a folder", *share)
			}
		}
		fmt.Print(imageSecrets + "\n")
		return macvm.Open(ctx, macvm.OpenOptions{Dir: dir, Share: *share, Say: os.Stdout})
	case "seal":
		image, err := macvm.Seal(dir, func(into string) (string, error) { return native.DownloadRunner(ctx, into) }, os.Stdout)
		if err != nil {
			return err
		}
		fmt.Println("Sealed: " + image.Digest)
		fmt.Print("\n" + imageSecrets)
		if !current.MacOSInVM() {
			fmt.Println("Run each macOS job in a clone of it with: hived config set macos-isolation vm")
		}
		return nil
	case "remove":
		if _, err := os.Stat(dir); err != nil {
			fmt.Println("There is no macOS image.")
			return nil
		}
		if !*yes {
			fmt.Printf("Delete the macOS image in %s? [y/N] ", dir)
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
				return errors.New("nothing was changed")
			}
		}
		if err := macvm.Remove(dir); err != nil {
			return err
		}
		fmt.Println("Removed.")
		return nil
	case "help", "-h", "--help":
		fmt.Print(macosImageUsage)
		return nil
	}
	return errors.New("usage: hived macos image create|open|seal|remove; see hived macos image help")
}

// macosImageStatus says what the image is and whether a job can run from
// it, and returns why not.
func macosImageStatus(current settings.Settings) error {
	dir, err := macosImageDir(current)
	if err != nil {
		return err
	}
	image, problem := (&macvm.Runner{Dir: dir, WorkDir: workDir(dir)}).Status()
	fmt.Println(field("image", dir))
	fmt.Println(field("macOS", orDash(image.MacOS)))
	fmt.Println(field("digest", orDash(image.Digest)))
	fmt.Println(field("runner", native.RunnerVersion))
	if problem != "" {
		return errors.New(problem)
	}
	return nil
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
