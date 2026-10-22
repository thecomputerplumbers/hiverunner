package native

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// What the child does that does not need macOS, so it is tested anywhere.

// runnerCommand is GitHub's runner in the job's clone of it, with a clean
// environment. The JIT config goes in the one variable the runner reads it
// from and then removes from its own environment, so it is not on any
// command line, and the job's steps do not inherit it.
func runnerCommand(ctx context.Context, runner, tmp, jitConfig string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, filepath.Join(runner, "run.sh"))
	cmd.Dir = runner
	// Nothing of this process leaks into the job but what is listed. The
	// one setting asks the runner to exit 7 when GitHub refuses its version.
	cmd.Env = []string{
		"HOME=" + Home,
		"USER=" + User,
		"LOGNAME=" + User,
		"SHELL=/bin/zsh",
		"TMPDIR=" + tmp,
		"LANG=en_US.UTF-8",
		"PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"ACTIONS_RUNNER_RETURN_VERSION_DEPRECATED_EXIT_CODE=1",
		"ACTIONS_RUNNER_INPUT_JITCONFIG=" + jitConfig,
	}
	// Its own process group, so stopping it stops every process the job
	// started that stayed in it. The clean after the job stops the rest.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 10 * time.Second
	return cmd
}

// checkRunner says why dir is not a runner every job may clone, or nil. It
// must be owner's alone, root on a Mac, and so must every directory from
// top down to it: whoever can write any of them can change what the next
// job runs. Nothing in it is setuid, setgid, or other than a directory, a
// file or a link. Links are not followed.
func checkRunner(top, dir string, owner uint32) error {
	top, dir = filepath.Clean(top), filepath.Clean(dir)
	if rel, err := filepath.Rel(top, dir); err != nil || !filepath.IsLocal(rel) || rel == "." {
		return fmt.Errorf("%s is not inside %s", dir, top)
	}
	for at := filepath.Dir(dir); ; at = filepath.Dir(at) {
		info, err := os.Lstat(at)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", at)
		}
		if err := ownedBy(at, info, owner); err != nil {
			return err
		}
		if at == top || at == filepath.Dir(at) {
			break
		}
	}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&(fs.ModeSetuid|fs.ModeSetgid) != 0:
			return fmt.Errorf("%s is setuid or setgid", path)
		case !info.Mode().IsDir() && !info.Mode().IsRegular() && info.Mode().Type() != fs.ModeSymlink:
			return fmt.Errorf("%s is not a file, a directory or a link", path)
		}
		return ownedBy(path, info, owner)
	})
	if err != nil {
		return err
	}
	if info, err := os.Lstat(filepath.Join(dir, "run.sh")); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not GitHub's runner", dir)
	}
	return nil
}

// ownedBy says why path is not owner's alone. A link's own mode is not
// asked: nothing writes through it but its target's permissions.
func ownedBy(path string, info fs.FileInfo, owner uint32) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: no owner", path)
	}
	if stat.Uid != owner {
		return fmt.Errorf("%s belongs to uid %d, not %d", path, stat.Uid, owner)
	}
	if info.Mode().Type() != fs.ModeSymlink && info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s may be written by others (%v)", path, info.Mode().Perm())
	}
	return nil
}

// process is one the clean found still running.
type process struct {
	PID  int
	Name string
}

// processes is what a clean needs of the system.
type processes interface {
	// SignalAll sends sig to every process this one may signal, but itself:
	// kill(-1, sig). For a user that is not root, that is every process of
	// that user's.
	SignalAll(sig syscall.Signal) error
	// Left is every process of this user's still alive, but this one.
	Left() ([]process, error)
}

// sweep stops every process of this user's but this one, and says what is
// still running once within has passed, if anything is. Stopping before
// killing keeps a process that forks from outrunning the kill.
func sweep(system processes, within, pause time.Duration) ([]process, error) {
	deadline := time.Now().Add(within)
	for {
		for _, sig := range []syscall.Signal{syscall.SIGSTOP, syscall.SIGKILL} {
			// ESRCH: there was nothing to signal.
			if err := system.SignalAll(sig); err != nil && !errors.Is(err, syscall.ESRCH) {
				return nil, err
			}
		}
		time.Sleep(pause)
		left, err := system.Left()
		if err != nil || len(left) == 0 || time.Now().After(deadline) {
			return left, err
		}
	}
}
