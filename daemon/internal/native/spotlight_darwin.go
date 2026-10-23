//go:build darwin

package native

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// lockPath is held shared by every running job and exclusively by arrange,
// so nothing is moved from under a job. It is in Home, which both layouts
// share.
var lockPath = filepath.Join(Home, ".hived-jobs.lock")

// arrange is the child's layout for the setting the parent sent, with
// everything moved into it if no job is running to be disturbed by that.
// What it could not move it says on stderr; the job runs anyway.
func arrange(noIndex bool) layout {
	where := layout{home: Home, noIndex: noIndex}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return where
	}
	defer file.Close()
	if syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return where
	}
	if err := where.arrange(); err != nil {
		fmt.Fprintln(os.Stderr, "hived macos-job: keeping Spotlight out:", err)
	}
	return where
}

// running holds the lock shared until the returned function is called.
func running() func() {
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_SH)
	return func() { file.Close() }
}

// Spotlight is what Spotlight does with the volume that holds Home, as this
// user can see it.
func Spotlight(noIndex bool) SpotlightReport {
	report := SpotlightReport{NoIndex: noIndex}
	var stat unix.Statfs_t
	if err := unix.Statfs(Home, &stat); err != nil {
		return report
	}
	report.Volume = unix.ByteSliceToString(stat.Mntonname[:])
	if out, err := exec.Command("/usr/bin/mdutil", "-s", report.Volume).CombinedOutput(); err == nil {
		report.Indexing = indexing(string(out))
	}
	// hive.noindex itself is hiveworker's alone, but Home can usually be
	// looked into.
	moved := false
	if _, err := os.Stat(filepath.Join(Home, "runner")); err == nil {
		report.Moved = &moved
	} else if _, err := os.Stat(filepath.Join(Home, NoIndexDir)); err == nil {
		moved = true
		report.Moved = &moved
	}
	return report
}

// SpotlightAdvice is what hived macos setup says about Spotlight once the
// Mac is set up. It changes nothing: macOS has no supported command for
// Spotlight's privacy list, and mdutil turns indexing off only for a whole
// volume.
func SpotlightAdvice(out io.Writer, noIndex bool) {
	report := Spotlight(noIndex)
	if report.Indexing == "off" {
		return
	}
	say := func(line string) { fmt.Fprintln(out, line) }
	say("")
	if !noIndex {
		say("Spotlight indexes everything a macOS job writes, which slows the job. To")
		say("keep it out of the job directories: hived config set spotlight-exclude on")
		return
	}
	say("Jobs keep their runner, checkouts and caches in " + filepath.Join(Home, NoIndexDir) + ",")
	say("which Spotlight skips. To keep the rest of " + Home + " out of the index")
	say("too, add it in System Settings > Spotlight > Search Privacy: press +, then")
	say("Shift-Command-G, and type " + Home + ".")
}
