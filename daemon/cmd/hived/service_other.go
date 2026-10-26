//go:build !darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/host"
)

// What this kind of machine is called in what hived prints.
const thisMachine = "this machine"

// serviceUnit is the systemd user service install.sh creates.
const serviceUnit = "hiveworker.service"

// servicePaths is where install.sh put the service's definition. The log is
// in the journal, which keeps itself.
func servicePaths(home string) (definition, logs string) {
	return filepath.Join(home, ".config", "systemd", "user", serviceUnit), ""
}

func stopService() {
	_ = exec.Command("systemctl", "--user", "disable", "--now", serviceUnit).Run()
}

// haltService stops the daemon and leaves it enabled, so it is back at the
// next login as it would be on a Mac.
func haltService() error {
	return exec.Command("systemctl", "--user", "stop", serviceUnit).Run()
}

func startService(string) error {
	return exec.Command("systemctl", "--user", "start", serviceUnit).Run()
}

// systemd keeps a unit loaded until it is told the file has gone.
func forgetService() {
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
}

// openBrowser shows the sign-in page where there is a browser to show it in.
// The link is printed too, so a machine with no screen loses nothing.
func openBrowser(url string) {
	// Under Windows' Linux subsystem the browser is Windows' own.
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		if explorer := "/mnt/c/Windows/explorer.exe"; exists(explorer) {
			_ = exec.Command(explorer, url).Start()
		}
		return
	}
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return
	}
	if opener, err := exec.LookPath("xdg-open"); err == nil {
		_ = exec.Command(opener, url).Start()
	}
}

func exists(file string) bool {
	_, err := os.Stat(file)
	return err == nil
}

// serviceChecks reads the installed unit for what an older installer left
// out. Without OOMPolicy=continue the kernel killing one job's VM for memory
// fails the whole service.
func serviceChecks(definition string) []host.Check {
	text, err := os.ReadFile(definition)
	if err != nil {
		return nil
	}
	if hasOOMPolicyContinue(string(text)) {
		return []host.Check{{Name: "memory", Found: "one job's VM running out of memory leaves the daemon running", OK: true}}
	}
	return []host.Check{{Name: "memory", Found: "one job's VM running out of memory stops the daemon and every job", Fix: "run the installer again"}}
}

func hasOOMPolicyContinue(unit string) bool {
	for _, line := range strings.Split(unit, "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok && strings.TrimSpace(key) == "OOMPolicy" {
			return strings.TrimSpace(value) == "continue"
		}
	}
	return false
}

// A Linux machine's daemon runs at the priority systemd gives any user
// service: there is no background band to leave.
func setServicePriority(bool) error { return nil }

func serviceSpawn() (background bool, pid int, ok bool) { return false, 0, false }

func restartService() error { return nil }

// priorityApplies is what hived config set priority says after saving.
func priorityApplies(bool) string {
	return "A Linux machine runs the daemon at standard priority, whatever this says."
}
