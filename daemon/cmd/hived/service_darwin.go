//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// launchd forgets a service whose file is gone.
func forgetService() {}

// openBrowser shows the sign-in page. The link is printed too, so a failure
// here costs a click.
func openBrowser(url string) {
	_ = exec.Command("/usr/bin/open", url).Start()
}
