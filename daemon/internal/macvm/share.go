package macvm

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// A job's VM is given two directories. What the host hands it (the runner,
// the job's configuration, the image's digest, a stop's reason) is in one
// the guest can only read. What it hands back (that it is ready, its exit
// code) is in another it can write, and anything there may be a trap: a
// link to one of the host's files, a FIFO that never answers, a file that
// never ends. So a file there is opened without following a link, refused
// unless it is a regular file, and read no further than a result can go.
// The host never writes there.

// mostResult is the longest a result the guest writes can be.
const mostResult = 32

// readResult reads a small file the guest wrote, or reports that there is
// no such file it will read.
func readResult(path string) (string, bool) {
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return "", false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	raw, err := io.ReadAll(io.LimitReader(file, mostResult))
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// present reports whether the guest has written a regular file here.
func present(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

// readExit is the exit code the guest reported, if it reported one.
func readExit(out string) (int, bool) {
	raw, ok := readResult(filepath.Join(out, "exit"))
	if !ok {
		return -1, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return -1, false
	}
	return code, true
}

// writeIn puts a file in what the host hands the guest as a new file:
// whatever had its name before is removed, never written through.
func writeIn(path string, content []byte) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(content)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}
