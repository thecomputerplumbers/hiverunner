package macvm

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ErrImageInUse means the image is open, or being sealed or cloned, by
// another command.
var ErrImageInUse = errors.New("the macOS image is in use; close hived macos image open, or wait for the job cloning it")

// lockImage holds the image while it is read or written. A job holds it
// shared while its clone is made; Open, Seal and Remove hold it alone, so a
// job never clones a disk that is being written. Neither waits.
func lockImage(dir string, exclusive bool) (release func(), err error) {
	file, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoImage
	}
	if err != nil {
		return nil, err
	}
	how := unix.LOCK_SH
	if exclusive {
		how = unix.LOCK_EX
	}
	if err := unix.Flock(int(file.Fd()), how|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrImageInUse
		}
		return nil, err
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		file.Close()
	}, nil
}
