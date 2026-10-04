package vm

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ErrWorkDirInUse means another hived process holds this work directory. The
// lock is per work directory, which is per guest image directory: two daemons
// on different images do not exclude each other.
var ErrWorkDirInUse = errors.New("another hived process is using this work directory; stop it first")

// LockWorkDir takes exclusive ownership of a work directory for as long as
// the caller runs VMs in it, and returns the call that gives it up. Every
// command that boots a VM takes it: the daemon, the self-test and dev-job.
//
// The lock is what makes sweeping safe. A job directory left on disk is only
// known to be abandoned if nothing else can be using it, and a name is no
// proof of that. While this lock is held no other daemon is running here, so
// whatever is in the directory before this one starts a VM is a leftover.
// The kernel drops the lock when the process dies, however it dies.
func LockWorkDir(dir string) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrWorkDirInUse
		}
		return nil, err
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		file.Close()
	}, nil
}
