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
		report.Problem = "GitHub's runner " + RunnerVersion + " is not installed for " + User + "; run: hived macos setup"
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
		plan("Download GitHub's runner " + RunnerVersion + " for " + User + " and check it against its checksum")
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
		if err := sudo("/usr/sbin/sysadminctl", "-addUser", User, "-fullName", "HiveWorker jobs", "-shell", "/bin/zsh", "-home", Home, "-password", hex.EncodeToString(password)); err != nil {
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
		dir, err := os.MkdirTemp("/tmp", "hiveworker-runner-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		if err := os.Chmod(dir, 0o755); err != nil {
			return err
		}
		tarball, err := downloadRunner(ctx, dir)
		if err != nil {
			return err
		}
		if _, err := askChild(exe, request{Install: tarball, Version: RunnerVersion}); err != nil {
			return fmt.Errorf("installing the runner for %s: %w", User, err)
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

// Remove undoes Setup: the sudo rule and the user go, with everything the
// user owned. Jobs that were running are gone with it.
func Remove(ctx context.Context, options SetupOptions) error {
	say := func(format string, args ...any) { fmt.Fprintf(options.Stdout, format+"\n", args...) }
	_, lookupErr := user.Lookup(User)
	_, sudoersErr := os.Stat(SudoersPath)
	if lookupErr != nil && sudoersErr != nil {
		say("Nothing to remove: this Mac was not set up for macOS jobs.")
		return nil
	}
	say("This will, with your password for sudo, remove %s and the %s user with", SudoersPath, User)
	say("its home folder. Nothing of yours is touched.")
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
	switch {
	case ask.Check:
		version := ""
		if _, err := os.Stat(filepath.Join(Home, "runner", RunnerVersion, "run.sh")); err == nil {
			version = RunnerVersion
		}
		_ = json.NewEncoder(stdout).Encode(checkReply{OK: true, Runner: version})
		return 0
	case ask.Install != "":
		if err := install(ctx, ask.Install, ask.Version); err != nil {
			fmt.Fprintln(os.Stderr, "hived macos-job:", err)
			return 1
		}
		return 0
	case ask.Job != nil:
		return runJob(ctx, *ask.Job)
	}
	fmt.Fprintln(os.Stderr, "hived macos-job: empty request")
	return 2
}

// install unpacks a runner tarball the parent verified into the runner
// directory for its version, whole or not at all.
func install(ctx context.Context, tarball, version string) error {
	if version == "" || strings.ContainsAny(version, "/\\ ") {
		return errors.New("bad runner version")
	}
	base := filepath.Join(Home, "runner")
	final := filepath.Join(base, version)
	staging := final + ".tmp"
	os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/tar", "-xzf", tarball, "-C", staging)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(staging)
		return fmt.Errorf("unpacking the runner: %w", err)
	}
	if _, err := os.Stat(filepath.Join(staging, "run.sh")); err != nil {
		os.RemoveAll(staging)
		return errors.New("the tarball is not GitHub's runner")
	}
	os.RemoveAll(final)
	return os.Rename(staging, final)
}

// runJob runs one job: a clone of the runner directory, GitHub's runner in
// it with a clean environment, and nothing left behind. A SIGTERM, which
// sudo relays from the daemon, stops the runner and still cleans up.
func runJob(ctx context.Context, job jobRequest) int {
	if job.JITConfig == "" || job.ID == "" || strings.ContainsAny(job.ID, "/\\ ") {
		fmt.Fprintln(os.Stderr, "hived macos-job: incomplete job")
		return 2
	}
	source := filepath.Join(Home, "runner", RunnerVersion)
	if _, err := os.Stat(filepath.Join(source, "run.sh")); err != nil {
		fmt.Fprintln(os.Stderr, "hived macos-job: no runner installed; run hived macos setup")
		return 1
	}
	jobs := filepath.Join(Home, "jobs")
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
	// An APFS clone: instant, and the job's writes never touch the original.
	if out, err := exec.CommandContext(ctx, "/bin/cp", "-c", "-R", source, runner).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "hived macos-job: cloning the runner: %v %s\n", err, strings.TrimSpace(string(out)))
		return 1
	}
	tmp := filepath.Join(dir, "tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "hived macos-job:", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	cmd := exec.CommandContext(ctx, filepath.Join(runner, "run.sh"), "--jitconfig", job.JITConfig)
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
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	// Its own process group, so stopping it stops every process the job
	// started, not only the runner.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 10 * time.Second
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
