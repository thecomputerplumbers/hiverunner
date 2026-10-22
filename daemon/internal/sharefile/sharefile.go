// Package sharefile is how the host touches a directory a guest can write.
//
// A job's VM is given directories of the host's. Anything the guest can
// write there may be a trap: a link to one of the host's files, a FIFO that
// never answers, a device that never ends, a directory where a file was
// expected, a file far larger than any result. So the host never follows a
// name there and never writes through one. A file it reads is opened without
// following a link, refused unless it is a regular file with no other name,
// and read no further than a result can go. A file it writes is a new one:
// whatever had the name before is removed first, never written through.
package sharefile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// ErrNotResult means what is at a path is not a file the host will read:
// a link, a FIFO, a device, a directory, or a file with another name.
var ErrNotResult = errors.New("not a regular file of its own")

// ErrTooLarge means a file is longer than a result can be.
var ErrTooLarge = errors.New("larger than a result can be")

// open opens a regular file with one name for reading, without following a
// link at the last step and without waiting on a FIFO.
func open(path string) (*os.File, fs.FileInfo, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, nil, fmt.Errorf("%s: %w", filepath.Base(path), ErrNotResult)
		}
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	// A hard link is another name for a file that may be the host's own.
	if !info.Mode().IsRegular() || links(info) != 1 {
		file.Close()
		return nil, nil, fmt.Errorf("%s: %w", filepath.Base(path), ErrNotResult)
	}
	return file, info, nil
}

func links(info fs.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Nlink)
	}
	return 0
}

// Read reads a file the guest wrote, of at most most bytes. A file that is
// not a regular file of its own is ErrNotResult; one that is longer is
// ErrTooLarge, and none of it is returned.
func Read(path string, most int64) ([]byte, error) {
	file, info, err := open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info.Size() > most {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), ErrTooLarge)
	}
	// The guest can still be writing: what is read is bounded, not what
	// the size said.
	raw, err := io.ReadAll(io.LimitReader(file, most+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > most {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), ErrTooLarge)
	}
	return raw, nil
}

// Tail reads the last most bytes of a file, or all of it if it is shorter:
// a log the guest can make as long as it likes. Like Read, it reads only a
// regular file of its own.
func Tail(path string, most int64) ([]byte, error) {
	file, info, err := open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if from := info.Size() - most; from > 0 {
		if _, err := file.Seek(from, io.SeekStart); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(io.LimitReader(file, most))
}

// CopyTail writes the last most bytes of a file to a new file at to, which
// must not exist.
func CopyTail(from, to string, most int64) error {
	source, info, err := open(from)
	if err != nil {
		return err
	}
	defer source.Close()
	if start := info.Size() - most; start > 0 {
		if _, err := source.Seek(start, io.SeekStart); err != nil {
			return err
		}
	}
	target, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(target, io.LimitReader(source, most))
	if closeErr := target.Close(); err == nil {
		err = closeErr
	}
	return err
}

// Present reports whether the guest has put a regular file of its own at
// path. A link, even to a regular file, is not one.
func Present(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && links(info) == 1
}

// WriteFresh puts a new file at path, readable by its owner alone. Whatever
// had the name before is removed, never written through; if the guest puts
// something back in between, the write fails rather than follow it.
func WriteFresh(path string, content []byte) error {
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

// Overfull reports whether dir holds more than a few small results: more
// than entries names, more than bytes in regular files, or anything a result
// never is, such as a directory. Results are files side by side, so nothing
// below dir is looked at, and nothing is followed. What cannot be looked at
// is overfull: a guest that hides what it has written is not believed. It
// bounds a mistake rather than a guest set on filling the disk between two
// looks.
func Overfull(dir string, entries int, bytes int64) bool {
	opened, err := os.OpenFile(dir, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return !errors.Is(err, os.ErrNotExist)
	}
	defer opened.Close()
	// One more than allowed is enough to know.
	names, err := opened.ReadDir(entries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return true
	}
	if len(names) > entries {
		return true
	}
	size := int64(0)
	for _, entry := range names {
		if entry.IsDir() {
			return true
		}
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			// Renamed or removed since it was listed: the agent's
			// name.tmp becoming name.
			continue
		}
		if err != nil {
			return true
		}
		if info.Mode().IsRegular() {
			size += info.Size()
		}
	}
	return size > bytes
}

// RemoveAll removes path and everything in it, as os.RemoveAll does,
// following no link. A guest can leave a directory it has taken its own
// permissions from, which os.RemoveAll cannot empty; this gives each
// directory back to its owner first. Call it only once no guest can write
// there: a directory swapped for a link between the look and the chmod
// would be followed.
func RemoveAll(path string) error {
	if err := os.RemoveAll(path); err == nil {
		return nil
	}
	_ = filepath.WalkDir(path, func(at string, entry fs.DirEntry, err error) error {
		// Called for a directory before it is read, so it can be.
		if entry != nil && entry.IsDir() {
			_ = os.Chmod(at, 0o700)
		}
		return nil
	})
	return os.RemoveAll(path)
}
