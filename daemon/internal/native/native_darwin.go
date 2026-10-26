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
// starts a process; Status is cached by its caller for that reason. legacy
// is the daemon itself, which a rule written before there was Child names.
func inspect(legacy string) Report {
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
	out, via, err := askEither(legacy, request{Check: true})
	if err != nil {
		report.Problem = err.Error()
		return report
	}
	report.Child = via
	var reply checkReply
	if json.Unmarshal(out, &reply) != nil || !reply.OK {
		report.Problem = "the " + User + " user cannot run hived; run: hived macos setup"
		return report
	}
	report.Sudo = true
	report.Runner = reply.Runner
	if reply.Runner != RunnerVersion {
		report.Problem = "GitHub's runner " + RunnerVersion + " is not installed in " + Runners + "; run: hived macos setup"
		if report.Kept() {
			report.Problem = "GitHub's runner " + RunnerVersion + " is not in " + Runners + " yet; the daemon puts it there as it starts, or run: hived macos setup"
		}
		return report
	}
	if !report.Kept() {
		report.Advice = adviseSetupAgain
	}
	return report
}

// askEither asks the child through the sudo rule: the hived in ChildDir,
// and when that does not answer, the one a rule written before there was
// Child names. When neither answers, the error says why, in words the owner
// can act on.
func askEither(legacy string, ask request) (out []byte, via string, err error) {
	var kept error
	if _, err := os.Stat(Child); err == nil {
		if out, err := askChild(Child, ask); err == nil {
			return out, Child, nil
		} else {
			kept = err
		}
	}
	if legacy != "" && legacy != Child {
		out, err := askChild(legacy, ask)
		if err == nil {
			return out, legacy, nil
		}
		if unreachable(err) {
			return nil, "", errors.New(User + " cannot open " + legacy + ", which the sudo rule names: it is in a folder closed to " + User + ", such as a home folder at 700; run: hived macos setup, which puts what " + User + " runs outside your home folder")
		}
	}
	if kept != nil && unreachable(kept) {
		return nil, "", errors.New(User + " cannot open " + Child + "; run: hived macos setup")
	}
	return nil, "", errors.New("hived cannot run jobs as " + User + ": the sudo rule in " + SudoersPath + " is missing or names another hived; run: hived macos setup")
}

// unreachable reports whether sudo found the rule but could not start the
// hived it names, as the error askChild returned says.
func unreachable(err error) bool { return strings.Contains(err.Error(), "unable to execute") }

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
	xcode, xcodeErr := xcodeVersion()
	if xcodeErr != nil && xcode == "" {
		return xcodeErr
	}
	before := inspect(exe)
	owned := keeps()
	if before.Problem == "" && before.Kept() && owned {
		if err := keep(ctx, exe, say); err != nil {
			return err
		}
		say("This Mac is already set up for macOS jobs, with %s and runner %s.", before.Xcode, before.Runner)
		warnHomeOpen(say)
		return nil
	}

	owner, err := user.Current()
	if err != nil {
		return err
	}
	say("")
	say("A hive-macos job runs on this Mac itself, not in a VM, as a separate user")
	say("named %s. It can use %s and whatever else is installed here, and the", User, xcode)
	say("network. It cannot read your keychain, or your home folder while that is")
	say("closed to other users. Only hives whose owners allow macOS jobs will send")
	say("any, and you can turn this off at any time with: hived config set macos-jobs off")
	say("")
	say("Setting up will, with your password for sudo:")
	step := 1
	plan := func(line string) { say("  %d. %s", step, line); step++ }
	if !before.UserExists {
		plan("Create the local user " + User + " (hidden from the login window, no usable password)")
	}
	plan("Let " + User + " use the developer tools (the _developer group)")
	if xcodeErr != nil {
		plan("Accept Xcode's license and finish its first launch")
	}
	if !owned {
		plan("Make " + Base + ", root's, with " + filepath.Base(ChildDir) + " and " + filepath.Base(Runners) + " in it yours, so that hived keeps what " + User + " runs up to date with no sudo")
	}
	if !before.Kept() {
		plan("Write " + SudoersPath + ", letting " + owner.Username + " run exactly `" + Child + " macos-job` as " + User)
	}
	if before.Runner != RunnerVersion {
		plan("Download GitHub's runner " + RunnerVersion + ", check it against its checksum, and unpack it in " + Runners)
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

	// sudo asks for a password on the terminal, not stdin, so stdin can
	// carry what a command reads.
	sudoWith := func(stdin io.Reader, args ...string) error {
		cmd := exec.CommandContext(ctx, "/usr/bin/sudo", args...)
		cmd.Stdin = stdin
		cmd.Stdout = io.Discard
		var stderr strings.Builder
		cmd.Stderr = io.MultiWriter(&stderr, os.Stderr)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("sudo %s: %w", args[0], err)
		}
		return nil
	}
	sudo := func(args ...string) error { return sudoWith(os.Stdin, args...) }

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
	if xcodeErr != nil {
		say("Finishing Xcode's first launch...")
		if err := sudo("/usr/bin/xcodebuild", "-license", "accept"); err != nil {
			return err
		}
		if err := sudo("/usr/bin/xcodebuild", "-runFirstLaunch"); err != nil {
			return err
		}
	}
	if !owned {
		say("Making %s...", Base)
		for _, step := range giveFolders(owner.Uid, owner.Gid) {
			if err := sudo(step...); err != nil {
				return err
			}
		}
	}
	// What the rule names is there before the rule is.
	if err := keepChild(exe, Child, say); err != nil {
		return err
	}
	if !before.Kept() {
		say("Writing %s...", SudoersPath)
		rule := fmt.Sprintf("# Written by hived macos setup. Lets %s run GitHub's runner as %s.\n%s ALL=(%s) NOPASSWD: %s macos-job\n", owner.Username, User, owner.Username, User, sudoersPath(Child))
		if err := sudoWith(strings.NewReader(rule), "/bin/sh", "-c", installSudoers, "hived", "/usr/sbin/visudo", SudoersPath); err != nil {
			return err
		}
	}
	if _, err := askChild(Child, request{Check: true}); err != nil {
		return fmt.Errorf("the sudo rule did not take: %w", err)
	}
	if err := keep(ctx, exe, say); err != nil {
		return fmt.Errorf("installing the runner: %w", err)
	}
	after := inspect(exe)
	if after.Problem != "" {
		return errors.New("still not ready: " + after.Problem)
	}
	say("")
	say("This Mac takes macOS jobs: runs-on: hive-macos, with %s and runner %s.", after.Xcode, after.Runner)
	warnHomeOpen(say)
	return nil
}

// giveFolders is what setup runs as root to make Base: root's, and not
// writable by anyone else, as are the folders above it, with ChildDir and
// Runners in it the owner's. A runner setup unpacked as root's is the
// owner's from then on, so the daemon can remove it once it is not needed.
func giveFolders(uid, gid string) [][]string {
	return [][]string{
		{"/usr/bin/install", "-d", "-o", "root", "-g", "wheel", "-m", "0755", Base},
		{"/usr/sbin/chown", "root:wheel", Base},
		{"/bin/chmod", "0755", Base},
		{"/usr/bin/install", "-d", "-m", "0755", ChildDir, Runners},
		{"/usr/sbin/chown", "-R", uid + ":" + gid, ChildDir, Runners},
		{"/bin/chmod", "0755", ChildDir, Runners},
	}
}

// sudoersPath is a path as a sudo rule names a command: a space in it is
// escaped, or it would end the command's name.
func sudoersPath(path string) string { return strings.ReplaceAll(path, " ", `\ `) }

// keeps reports whether setup has given this owner ChildDir and Runners, in
// which the daemon keeps what hiveworker runs.
func keeps() bool {
	for _, dir := range []string{ChildDir, Runners} {
		if ours(dir) != nil {
			return false
		}
	}
	return true
}

// ours says why dir is not a folder of this user's that no one else may
// write, or nil.
func ours(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}
	return ownedBy(dir, info, uint32(os.Getuid()))
}

// keep makes what hiveworker runs this daemon's own: hived in ChildDir, and
// GitHub's runner unpacked in Runners, with any other version removed. It
// needs no sudo. On a Mac set up before setup made those folders the
// owner's, it does nothing: errNotKept.
func keep(ctx context.Context, exe string, say func(string, ...any)) error {
	if !keeps() {
		return errNotKept
	}
	if err := keepChild(exe, Child, say); err != nil {
		return err
	}
	me := uint32(os.Getuid())
	if checkRunner("/", RunnerDir(), 0, me) != nil {
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
		if err := unpackRunner(ctx, tarball, "/", RunnerDir()); err != nil {
			return err
		}
		say("Unpacked GitHub's runner %s in %s", RunnerVersion, Runners)
	}
	// Another version is a runner an older hived pinned. A job clones the
	// runner as it starts, and keep runs while none does.
	entries, err := os.ReadDir(Runners)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != RunnerVersion {
			if err := os.RemoveAll(filepath.Join(Runners, entry.Name())); err != nil {
				say("Could not remove %s: %v", filepath.Join(Runners, entry.Name()), err)
			}
		}
	}
	return checkRunner("/", RunnerDir(), 0, me)
}

// keepChild puts a copy of exe at child, Child but in a test, unless one is
// there, in one step: a job starting at the same moment runs the old one
// or the new.
func keepChild(exe, child string, say func(string, ...any)) error {
	if same, _ := sameFile(exe, child); same {
		return nil
	}
	from, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer from.Close()
	to, err := os.CreateTemp(filepath.Dir(child), ".hived-")
	if err != nil {
		return err
	}
	defer os.Remove(to.Name())
	_, err = io.Copy(to, from)
	if err == nil {
		err = to.Chmod(0o755)
	}
	if err == nil {
		err = to.Sync()
	}
	if closeErr := to.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("copying hived to %s: %w", filepath.Dir(child), err)
	}
	if err := os.Rename(to.Name(), child); err != nil {
		return err
	}
	say("Put this hived in %s for %s to run", filepath.Dir(child), User)
	return nil
}

// unpackRunner unpacks a runner tarball the caller verified into dir,
// RunnerDir but in a test, whole or not at all, read-only to everyone else,
// and with nothing in it setuid or setgid. It checks it as a job would,
// from top.
func unpackRunner(ctx context.Context, tarball, top, dir string) error {
	staging, err := os.MkdirTemp(filepath.Dir(dir), "."+filepath.Base(dir)+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	for _, step := range [][]string{
		{"/usr/bin/tar", "-xzf", tarball, "-C", staging},
		{"/bin/chmod", "-R", "go-w,a+rX,ug-s", staging},
		{"/bin/chmod", "0755", staging},
	} {
		if out, err := exec.CommandContext(ctx, step[0], step[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %w: %s", filepath.Base(step[0]), err, strings.TrimSpace(string(out)))
		}
	}
	if err := checkRunner(top, staging, 0, uint32(os.Getuid())); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Rename(staging, dir)
}

// warnHomeOpen says so if the owner's home folder is open to User, and how
// to close it. It is the owner's to close: setup changes nothing of theirs.
// Setup has put what User runs outside it by then.
func warnHomeOpen(say func(string, ...any)) {
	home, how := HomeOpen()
	if how == "" {
		return
	}
	say("")
	say("Your home folder, %s, is %s,", home, how)
	say("so a macOS job can read whatever in it is not closed to it in turn. To close it:")
	say("  %s", CloseHome)
}

// Remove undoes Setup: the sudo rule, the runner and the user go, with
// everything the user owned. Jobs that were running are gone with it.
func Remove(ctx context.Context, options SetupOptions) error {
	say := func(format string, args ...any) { fmt.Fprintf(options.Stdout, format+"\n", args...) }
	_, lookupErr := user.Lookup(User)
	_, sudoersErr := os.Stat(SudoersPath)
	_, baseErr := os.Lstat(Base)
	if lookupErr != nil && sudoersErr != nil && baseErr != nil {
		say("Nothing to remove: this Mac was not set up for macOS jobs.")
		return nil
	}
	say("This will, with your password for sudo, remove %s, %s,", SudoersPath, Base)
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
	if _, err := os.Lstat(Base); err == nil {
		if err := sudo("/bin/rm", "-rf", Base); err != nil {
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
		if checkRunner("/", RunnerDir(), runnerOwners()...) == nil {
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
	// As root, every process on the Mac is root's to kill.
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

// Signal never takes -1: see sweep. ESRCH is a process gone by then, and
// EPERM one whose pid another user's process has taken since.
func (darwinProcesses) Signal(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return fmt.Errorf("not a process: %d", pid)
	}
	if err := syscall.Kill(pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.EPERM) {
		return err
	}
	return nil
}

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
	if err := checkRunner("/", source, runnerOwners()...); err != nil {
		fmt.Fprintln(os.Stderr, "hived macos-job: the runner may be changed by someone other than root and the owner; run hived macos setup:", err)
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

// Kept reports, without asking sudo, whether setup has put hived in
// ChildDir for this owner: whether hiveworker runs what the daemon keeps,
// outside the owner's home folder.
func Kept() bool {
	if _, err := os.Stat(Child); err != nil {
		return false
	}
	return keeps()
}
