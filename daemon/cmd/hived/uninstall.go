package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/cache"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/config"
)

// uninstall removes everything the installer and the daemon put on this
// machine: the background service, the guest image, the Actions cache, the
// settings, this machine's token and the daemon itself. It asks first.
//
// The machine's row on the server is not removed here. It shows as offline,
// and its owner or an organization owner can remove it from the dashboard.
func uninstall(args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	machineFile, err := config.Path()
	if err != nil {
		return err
	}
	data := filepath.Dir(machineFile)
	cacheDir, _ := cache.DefaultDir()
	agent, logs := servicePaths(home)
	self, _ := os.Executable()

	fmt.Println("This removes HiveWorker from " + thisMachine + ":")
	fmt.Println("  the background service, so it stops taking jobs")
	fmt.Println("  " + data + " (the Linux image, your settings and " + thisMachine + "'s sign-in)")
	if cacheDir != "" {
		fmt.Println("  " + filepath.Dir(cacheDir) + " (the Actions cache)")
	}
	if logs != "" {
		fmt.Println("  " + logs)
	}
	if self != "" {
		fmt.Println("  " + self)
	}
	if len(args) == 0 || args[0] != "--yes" {
		fmt.Print("\nType yes to remove it: ")
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(strings.ToLower(answer)) != "yes" {
			return errors.New("nothing was removed")
		}
	}

	// Stopping the service stops the daemon, which destroys any VM it had.
	// Only if this installation has a service: the service's name belongs to
	// the user, not to a home directory, so a copy of hived run from
	// somewhere else must not stop the one that is really installed.
	if _, err := os.Stat(agent); err == nil {
		stopService()
	}
	var failed []string
	remove := func(path string) {
		if path == "" {
			return
		}
		if err := os.RemoveAll(path); err != nil {
			failed = append(failed, path)
		}
	}
	remove(agent)
	forgetService()
	remove(data)
	if cacheDir != "" {
		remove(filepath.Dir(cacheDir))
	}
	remove(logs)
	// Last, and it works: a running program's file can be deleted under it.
	remove(self)
	if len(failed) > 0 {
		return fmt.Errorf("could not remove: %s", strings.Join(failed, ", "))
	}
	fmt.Println("\nRemoved. " + strings.ToUpper(thisMachine[:1]) + thisMachine[1:] + " still appears in your hive as offline; remove it from the dashboard under Machines.")
	return nil
}
