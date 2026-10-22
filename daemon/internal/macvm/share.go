package macvm

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/sharefile"
)

// A job's VM is given two directories. What the host hands it (the runner,
// the job's configuration, the image's digest, a stop's reason) is in one
// the guest can only read. What it hands back (that it is ready, its exit
// code) is in another it can write, and anything there may be a trap: see
// package sharefile. The host never writes there.

// mostResult is the longest a result the guest writes can be.
const mostResult = 32

// readExit is the exit code the guest reported, if it reported one.
func readExit(out string) (int, bool) {
	raw, err := sharefile.Read(filepath.Join(out, "exit"), mostResult)
	if err != nil {
		return -1, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return -1, false
	}
	return code, true
}

// writeIn puts a file in what the host hands the guest as a new file:
// whatever had its name before is removed, never written through.
func writeIn(path string, content []byte) error { return sharefile.WriteFresh(path, content) }

// present reports whether the guest has written a regular file here.
func present(path string) bool { return sharefile.Present(path) }
