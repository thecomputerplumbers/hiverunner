//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/host"
)

// What this kind of machine is called in what hived prints.
const thisMachine = "this Mac"

// serviceLabel is the launch agent install.sh creates.
const serviceLabel = "com.thecomputerplumbers.hiveworker"

// servicePaths is where install.sh put the service's definition and where
// the service writes its log.
func servicePaths(home string) (definition, logs string) {
	return filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist"),
		filepath.Join(home, "Library", "Logs", "HiveWorker")
}

func stopService() {
	_ = exec.Command("/bin/launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+serviceLabel).Run()
}

// haltService stops the daemon until it is started again or its owner next
// logs in. launchd would restart a process that was only killed.
func haltService() error {
	return exec.Command("/bin/launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+serviceLabel).Run()
}

func startService(definition string) error {
	domain := "gui/" + strconv.Itoa(os.Getuid())
	if err := exec.Command("/bin/launchctl", "bootstrap", domain, definition).Run(); err != nil {
		// Loaded already, with its process gone: launchd refuses to load it
		// twice and is asked to run it instead.
		return exec.Command("/bin/launchctl", "kickstart", domain+"/"+serviceLabel).Run()
	}
	return nil
}

// launchd forgets a service whose file is gone.
func forgetService() {}

// openBrowser shows the sign-in page. The link is printed too, so a failure
// here costs a click.
func openBrowser(url string) {
	_ = exec.Command("/usr/bin/open", url).Start()
}

// serviceChecks has nothing to add on a Mac.
func serviceChecks(string) []host.Check { return nil }

// processType is the launch agent's ProcessType for a priority.
func processType(background bool) string {
	if background {
		return "Background"
	}
	return "Standard"
}

// setServicePriority makes the installer's launch agent say the priority,
// if it says otherwise. It changes how launchd next starts the daemon, not
// how it runs now.
func setServicePriority(background bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	definition, _ := servicePaths(home)
	if _, err := os.Stat(definition); err != nil {
		return nil
	}
	raw, err := os.ReadFile(definition)
	if err != nil {
		return err
	}
	written, ok := withProcessType(string(raw), processType(background))
	if !ok {
		// Not as the installer writes it: plutil, whatever it then looks like.
		return exec.Command("/usr/bin/plutil", "-replace", "ProcessType", "-string", processType(background), definition).Run()
	}
	if written == string(raw) {
		return nil
	}
	temporary := definition + ".tmp"
	if err := os.WriteFile(temporary, []byte(written), 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, definition)
}

// serviceSpawn is how launchd runs the installer's service: in the
// background band or not, and as which pid. ok is false when it is not
// loaded.
func serviceSpawn() (background bool, pid int, ok bool) {
	out, err := exec.Command("/bin/launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())+"/"+serviceLabel).Output()
	if err != nil {
		return false, 0, false
	}
	background, pid = spawnBackground(string(out))
	return background, pid, true
}

// reloadService stops the service and loads its definition again, which a
// restart under KeepAlive does not read: $1 the domain, $2 the label, $3 the
// definition. Loading is tried for a while, as the installer does, since
// the old daemon takes a few seconds to go.
const reloadService = `PATH=/usr/bin:/bin:/usr/sbin:/sbin
launchctl bootout "$1/$2" 2>/dev/null
waited=0
while launchctl print "$1/$2" >/dev/null 2>&1 && [ "$waited" -lt 90 ]; do
  sleep 1
  waited=$((waited + 1))
done
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  launchctl bootstrap "$1" "$3" 2>/dev/null && exit 0
  sleep 2
done
exit 1
`

// restartService has launchd start the daemon again from its definition. It
// returns at once: the script runs in a session of its own, as the
// installer does, so it outlives this daemon, which launchd stops first.
func restartService() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	definition, _ := servicePaths(home)
	cmd := exec.Command("/bin/sh", "-c", reloadService, "hived", "gui/"+strconv.Itoa(os.Getuid()), serviceLabel, definition)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// priorityApplies is what hived config set priority says after saving.
func priorityApplies(running bool) string {
	if running {
		return "The daemon starts again at that priority once it has no job running."
	}
	return "It applies when the daemon starts."
}
