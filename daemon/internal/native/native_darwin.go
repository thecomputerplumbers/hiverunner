//go:build darwin

package native

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// inspect is Status on a Mac. Each check is cheap except the sudo one, which
// starts a process; Status is cached by its caller for that reason.
func inspect(exe string) Report {
	report := Report{Platform: runtime.GOOS + "/" + runtime.GOARCH}
	if runtime.GOARCH != "arm64" {
		report.Problem = "macOS jobs run only on an Apple Silicon Mac"
		return report
	}
	version, err := xcodeVersion()
	report.Xcode = version
	if err != nil {
		report.Problem = err.Error()
		return report
	}
	if _, err := user.Lookup(User); err == nil {
		report.UserExists = true
	} else {
		report.Problem = "the " + User + " user does not exist yet; run: hived macos setup"
		return report
	}
	out, err := askChild(exe, request{Check: true})
	if err != nil {
		report.Problem = "hived cannot run jobs as " + User + " (the sudo rule in " + SudoersPath + " is missing or names another hived); run: hived macos setup"
		return report
	}
	var reply checkReply
	if json.Unmarshal(out, &reply) != nil || !reply.OK {
		report.Problem = "the " + User + " user cannot run hived; run: hived macos setup"
		return report
	}
	report.Sudo = true
	report.Runner = reply.Runner
	if reply.Runner != RunnerVersion {
		report.Problem = "GitHub's runner " + RunnerVersion + " is not installed, as root's, in " + Runners + "; run: hived macos setup"
		return report
	}
	return report
}

// xcodeVersion is the installed Xcode, or why it will not do.
func xcodeVersion() (string, error) {
	path, err := exec.Command("/usr/bin/xcode-select", "-p").Output()
	if err != nil || !strings.Contains(string(path), ".app/") {
		return "", errors.New("Xcode is not installed, or xcode-select points at the command line tools; install Xcode and run: sudo xcode-select -s /Applications/Xcode.app")
	}
	out, err := exec.Command("/usr/bin/xcodebuild", "-version").Output()
	if err != nil {
		return "", errors.New("xcodebuild does not run; open Xcode once and accept its license")
	}
	version := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if err := exec.Command("/usr/bin/xcodebuild", "-checkFirstLaunchStatus").Run(); err != nil {
		return version, errors.New(version + " has not finished its first launch; run: sudo xcodebuild -license accept && sudo xcodebuild -runFirstLaunch")
	}
	return version, nil
}

// Setup makes this Mac ready for macOS jobs, asking sudo for what needs it.
// It is idempotent: whatever is already in place is left alone.
func Setup(ctx context.Context, options SetupOptions) error {
	exe, err := executable()
	if err != nil {
		return err
	}
	say := func(format string, args ...any) { fmt.Fprintf(options.Stdout, format+"\n", args...) }
	if runtime.GOARCH != "arm64" {
		return errors.New("macOS jobs run only on an Apple Silicon Mac")
	}
	xcode, err := xcodeVersion()
	if err != nil && xcode == "" {
		return err
	}
	before := inspect(exe)
	if before.Problem == "" {
		say("This Mac is already set up for macOS jobs, with %s and runner %s.", before.Xcode, before.Runner)
		return nil
	}

	owner, err := user.Current()
	if err != nil {
		return err
	}
	say("")
	say("A hive-macos job runs on this Mac itself, not in a VM, as a separate user")
	say("named %s. It can use %s and whatever else is installed here, and the", User, xcode)
	say("network. It cannot read your home folder or your keychain. Only hives whose")
	say("owners allow macOS jobs will send any, and you can turn this off at any")
	say("time with: hived config set macos-jobs off")
	say("")
	say("Setting up will, with your password for sudo:")
	step := 1
	plan := func(line string) { say("  %d. %s", step, line); step++ }
	if !before.UserExists {
		plan("Create the local user " + User + " (hidden from the login window, no usable password)")
	}
	plan("Let " + User + " use the developer tools (the _developer group)")
	if err != nil {
		plan("Accept Xcode's license and finish its first launch")
	}
	if !before.Sudo {
		plan("Write " + SudoersPath + ", letting " + owner.Username + " run exactly `" + exe + " macos-job` as " + User)
	}
	if before.Runner != RunnerVersion {
		plan("Download GitHub's runner " + RunnerVersion + ", check it against its checksum, and unpack it as root's in " + Runners)
	}
	plan("Turn on the macos-jobs setting")
	say("")
	if !options.Yes {
		fmt.Fprint(options.Stdout, "Proceed? [y/N] ")
		line, _ := bufio.NewReader(options.Stdin).ReadString('\n')
		if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
			return errors.New("nothing was changed")
		}
	}

	sudo := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "/usr/bin/sudo", args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = io.Discard
		var stderr strings.Builder
		cmd.Stderr = io.MultiWriter(&stderr, os.Stderr)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("sudo %s: %w", args[0], err)
		}
		return nil
	}

	if !before.UserExists {
		say("Creating the %s user...", User)
		password := make([]byte, 24)
		if _, err := rand.Read(password); err != nil {
			return err
		}
		if err := sudo("/usr/sbin/sysadminctl", "-addUser", User, "-fullName", "Hive Runner jobs", "-shell", "/bin/zsh", "-home", Home, "-password", hex.EncodeToString(password)); err != nil {
			return err
		}
		if err := sudo("/usr/bin/dscl", ".", "-create", "/Users/"+User, "IsHidden", "1"); err != nil {
			return err
		}
		if err := sudo("/usr/sbin/createhomedir", "-c", "-u", User); err != nil {
			return err
		}
	}
	if err := sudo("/usr/sbin/dseditgroup", "-o", "edit", "-a", User, "-t", "user", "_developer"); err != nil {
		return err
	}
	if err != nil {
		say("Finishing Xcode's first launch...")
		if err := sudo("/usr/bin/xcodebuild", "-license", "accept"); err != nil {
			return err
		}
		if err := sudo("/usr/bin/xcodebuild", "-runFirstLaunch"); err != nil {
			return err
		}
	}
	if !before.Sudo {
		say("Writing %s...", SudoersPath)
		rule := fmt.Sprintf("# Written by hived macos setup. Lets %s run GitHub's runner as %s.\n%s ALL=(%s) NOPASSWD: %s macos-job\n", owner.Username, User, owner.Username, User, exe)
		temporary, err := os.CreateTemp("", "hiveworker-sudoers-")
		if err != nil {
			return err
		}
		defer os.Remove(temporary.Name())
		if _, err := temporary.WriteString(rule); err != nil {
			return err
		}
		if err := temporary.Chmod(0o644); err != nil {
			return err
		}
		temporary.Close()
		if err := sudo("/usr/sbin/visudo", "-cf", temporary.Name()); err != nil {
			return err
		}
		if err := sudo("/usr/bin/install", "-o", "root", "-g", "wheel", "-m", "0440", temporary.Name(), SudoersPath); err != nil {
			return err
		}
	}
	// What the child has, now that it can be asked.
	out, err := askChild(exe, request{Check: true})
	if err != nil {
		return fmt.Errorf("the sudo rule did not take: %w", err)
	}
	var reply checkReply
	if json.Unmarshal(out, &reply) != nil || !reply.OK {
		return errors.New("the " + User + " user could not run hived")
	}
	if reply.Runner != RunnerVersion {
		say("Downloading GitHub's runner %s...", RunnerVersion)
		dir, err := os.MkdirTemp("", "hiveworker-runner-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		tarball, err := DownloadRunner(ctx, dir)
		if err != nil {
			return err
		}
		if err := installRunner(sudo, tarball); err != nil {
			return fmt.Errorf("installing the runner: %w", err)
		}
	}
	after := inspect(exe)
	if after.Problem != "" {
		return errors.New("still not ready: " + after.Problem)
	}
	say("")
	say("This Mac takes macOS jobs: runs-on: hive-macos, with %s and runner %s.", after.Xcode, after.Runner)
	return nil
}

// installRunner unpacks a runner tarball the caller verified into RunnerDir,
// whole or not at all, as root's and read-only to everyone else. tar -o
// makes everything root's whatever the archive says, and nothing in it may
// be setuid or setgid.
func installRunner(sudo func(...string) error, tarball string) error {
	final := RunnerDir()
	staging := final + ".tmp"
	steps := [][]string{
		{"/bin/rm", "-rf", staging},
		{"/usr/bin/install", "-d", "-o", "root", "-g", "wheel", "-m", "0755", filepath.Dir(Runners)},
		{"/usr/bin/install", "-d", "-o", "root", "-g", "wheel", "-m", "0755", Runners},
		{"/usr/bin/install", "-d", "-o", "root", "-g", "wheel", "-m", "0755", staging},
		{"/usr/bin/tar", "-xzof", tarball, "-C", staging},
		{"/usr/sbin/chown", "-R", "root:wheel", staging},
		{"/bin/chmod", "-R", "go-w,a+rX,ug-s", staging},
		{"/bin/rm", "-rf", final},
		{"/bin/mv", staging, final},
	}
	for _, step := range steps {
		if err := sudo(step...); err != nil {
			_ = sudo("/bin/rm", "-rf", staging)
			return err
		}
	}
	return checkRunner("/", final, 0)
}

// Remove undoes Setup: the sudo rule, the runner and the user go, with
// everything the user owned. Jobs that were running are gone with it.
func Remove(ctx context.Context, options SetupOptions) error {
	say := func(format string, args ...any) { fmt.Fprintf(options.Stdout, format+"\n", args...) }
	_, lookupErr := user.Lookup(User)
	_, sudoersErr := os.Stat(SudoersPath)
	_, runnersErr := os.Lstat(filepath.Dir(Runners))
	if lookupErr != nil && sudoersErr != nil && runnersErr != nil {
		say("Nothing to remove: this Mac was not set up for macOS jobs.")
		return nil
	}
	say("This will, with your password for sudo, remove %s, %s,", SudoersPath, filepath.Dir(Runners))
	say("and the %s user with its home folder. Nothing of yours is touched.", User)
	if !options.Yes {
		fmt.Fprint(options.Stdout, "Proceed? [y/N] ")
		line, _ := bufio.NewReader(options.Stdin).ReadString('\n')
		if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
			return errors.New("nothing was changed")
		}
	}
	sudo := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "/usr/bin/sudo", args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, io.Discard, os.Stderr
		return cmd.Run()
	}
	if sudoersErr == nil {
		if err := sudo("/bin/rm", "-f", SudoersPath); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(filepath.Dir(Runners)); err == nil {
		if err := sudo("/bin/rm", "-rf", filepath.Dir(Runners)); err != nil {
			return err
		}
	}
	if lookupErr == nil {
		if err := sudo("/usr/sbin/sysadminctl", "-deleteUser", User, "-secure"); err != nil {
			return err
		}
	}
	say("Removed.")
	return nil
}

// Serve is `hived macos-job`: the child, running as the hiveworker user. It
// reads one request from stdin and returns the process exit code. For a job
// that is the runner's own exit code.
func Serve(ctx context.Context, stdin io.Reader, stdout io.Writer) int {
	me, err := user.Current()
	if err != nil || me.Username != User {
		fmt.Fprintln(os.Stderr, "hived macos-job runs as the", User, "user; hived runs it through sudo")
		return 2
	}
	var ask request
	if err := json.NewDecoder(io.LimitReader(stdin, 1<<20)).Decode(&ask); err != nil {
		fmt.Fprintln(os.Stderr, "hived macos-job: bad request:", err)
		return 2
	}
	where := arrange(ask.NoIndex)
	switch {
	case ask.Check:
		version := ""
		if checkRunner("/", RunnerDir(), 0) == nil {
			version = RunnerVersion
		}
		_ = json.NewEncoder(stdout).Encode(checkReply{OK: true, Runner: version})
		return 0
	case ask.Clean:
		return clean()
	case ask.Job != nil:
		return runJob(ctx, where, *ask.Job)
	}
	fmt.Fprintln(os.Stderr, "hived macos-job: empty request")
	return 2
}

// clean stops every process hiveworker has but this one, the way a job's
// process that left the runner's process group is stopped, and checks none
// is left. Then it removes what jobs left in its jobs directory, and the
// runner hiveworker once unpacked for itself. The crontab goes first and
// last, so cron does not start one again.
func clean() int {
	// As root, kill(-1) is every process on the Mac.
	if os.Getuid() == 0 {
		fmt.Fprintln(os.Stderr, "hived macos-job: a clean does not run as root")
		return 2
	}
	removeCrontab()
	left, err := sweep(darwinProcesses{uid: uint32(os.Getuid()), self: os.Getpid()}, 10*time.Second, 100*time.Millisecond)
	removeCrontab()
	if err != nil {
		fmt.Fprintln(os.Stderr, "hived macos-job: stopping "+User+"'s processes:", err)
		return ExitNotClean
	}
	if len(left) > 0 {
		names := make([]string, 0, len(left))
		for _, p := range left {
			names = append(names, fmt.Sprintf("%d %s", p.PID, p.Name))
		}
		fmt.Fprintln(os.Stderr, "hived macos-job: still running as "+User+" after a kill:", strings.Join(names, ", "))
		return ExitNotClean
	}
	// Only disk: a job made a file of its own it cannot remove, say, which
	// must not hold off every job after it. Each job has a directory of its
	// own, and the runner is root's. Both layouts' are removed, whichever
	// spotlight-exclude says: what is in the other is left from before it
	// changed.
	home := layout{home: Home}
	for _, dir := range []string{home.jobs(), home.other().jobs(), home.legacyRunner(), home.other().legacyRunner()} {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintln(os.Stderr, "hived macos-job: removing what jobs left:", err)
		}
	}
	return 0
}

// removeCrontab removes hiveworker's crontab, if a job left one.
func removeCrontab() {
	_ = exec.Command("/usr/bin/crontab", "-r").Run()
}

// darwinProcesses is the system's processes, for a clean.
type darwinProcesses struct {
	uid  uint32
	self int
}

func (darwinProcesses) SignalAll(sig syscall.Signal) error { return syscall.Kill(-1, sig) }

// Left is every process whose real, effective or saved user is this one's,
// but this one and zombies, which are already dead.
func (p darwinProcesses) Left() ([]process, error) {
	const zombie = 5 // SZOMB
	all, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	var left []process
	for _, info := range all {
		pid := int(info.Proc.P_pid)
		if pid == p.self || info.Proc.P_stat == zombie {
			continue
		}
		cred := info.Eproc.Pcred
		if info.Eproc.Ucred.Uid != p.uid && cred.P_ruid != p.uid && cred.P_svuid != p.uid {
			continue
		}
		name := unix.ByteSliceToString(info.Proc.P_comm[:])
		left = append(left, process{PID: pid, Name: name})
	}
	return left, nil
}

// runJob runs one job: a clone of root's runner directory, GitHub's runner
// in it with a clean environment, and nothing left behind. A SIGTERM, which
// sudo relays from the daemon, stops the runner and still cleans up.
func runJob(ctx context.Context, where layout, job jobRequest) int {
	if job.JITConfig == "" || job.ID == "" || strings.ContainsAny(job.ID, "/\\ ") {
		fmt.Fprintln(os.Stderr, "hived macos-job: incomplete job")
		return 2
	}
	defer running()()
	source := RunnerDir()
	if err := checkRunner("/", source, 0); err != nil {
		fmt.Fprintln(os.Stderr, "hived macos-job: the runner is not root's alone; run hived macos setup:", err)
		return 1
	}
	jobs := where.jobs()
	if err := os.MkdirAll(jobs, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "hived macos-job:", err)
		return 1
	}
	dir, err := os.MkdirTemp(jobs, job.ID+"-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "hived macos-job:", err)
		return 1
	}
	defer os.RemoveAll(dir)
	runner := filepath.Join(dir, "runner")
	// An APFS clone: instant, and the job's writes never touch the original,
	// which is root's in any case. The clone is hiveworker's, as the runner
	// writes in its own directory.
	if out, err := exec.CommandContext(ctx, "/bin/cp", "-c", "-R", source, runner).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "hived macos-job: cloning the runner: %v %s\n", err, strings.TrimSpace(string(out)))
		return 1
	}
	if info, err := os.Lstat(filepath.Join(runner, "run.sh")); err != nil || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) {
		fmt.Fprintln(os.Stderr, "hived macos-job: the clone of the runner is not "+User+"'s")
		return 1
	}
	tmp := filepath.Join(dir, "tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "hived macos-job:", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	cmd := runnerCommand(ctx, runner, tmp, job.JITConfig)
	cmd.Env = append(cmd.Env, where.env()...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit) && exit.ExitCode() >= 0:
		return exit.ExitCode()
	}
	fmt.Fprintln(os.Stderr, "hived macos-job: the runner did not finish:", err)
	return 1
}
