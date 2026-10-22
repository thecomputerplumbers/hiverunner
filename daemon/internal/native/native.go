// Package native runs hive-macos jobs on this Mac itself, for the things
// only macOS can do: building for iOS, signing with Xcode. There is no VM.
// Instead the job runs as a separate local user, hiveworker, that owns
// nothing of the Mac's owner's. That keeps the owner's files and keychain
// out of reach, and nothing more: the job sees this Mac's installed
// software and its network, like any self-hosted macOS runner.
//
// The daemon runs as the owner. It reaches the hiveworker user through one
// sudo rule, written by `hived macos setup`, that lets the owner run exactly
// `hived macos-job` as hiveworker and nothing else. That child reads what to
// do from stdin, so nothing it is given appears on a command line.
//
// Every job runs as the one user, so one runs at a time, and before and
// after each the child stops every process hiveworker has: a job's process
// that left its process group is not left running into the next job.
package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/jobs"
)

const (
	// User runs the jobs. A plain local account, hidden from the login
	// window, with a password nobody knows.
	User = "hiveworker"
	Home = "/Users/" + User
	// RunnerVersion is GitHub's runner, pinned with its checksum the way the
	// guest image pins its own. Bump both together.
	RunnerVersion = "2.337.0"
	RunnerSHA256  = "5a2cd92908a93d7276a194e1de6008099f3e7946f3f8e14aa7a1a7b4a31fdec2"
	// RunnerFile is the runner's tarball, as GitHub names it.
	RunnerFile = "actions-runner-osx-arm64-" + RunnerVersion + ".tar.gz"
	runnerURL  = "https://github.com/actions/runner/releases/download/v" + RunnerVersion + "/" + RunnerFile
	// Runners is where setup unpacks GitHub's runner, a directory per
	// version. It is root's, and outside hiveworker's home, so no job can
	// change what the next job runs: each job runs a clone of it.
	Runners = "/Library/Application Support/Hive Runner/runner"
	// SudoersPath is the one rule setup writes.
	SudoersPath = "/etc/sudoers.d/hiveworker"
	// ExitRunnerDeprecated is the runner's exit code when GitHub refuses its
	// version as too old, the same as in the guest.
	ExitRunnerDeprecated = 7
	// ExitNotClean is the child's exit code when processes of hiveworker's
	// are still running after it stopped them all.
	ExitNotClean = 75
)

// RunnerDir is the runner every job clones.
func RunnerDir() string { return filepath.Join(Runners, RunnerVersion) }

// ReasonStaleRunner is why a Mac declines macOS jobs once GitHub has refused
// its runner as too old.
const ReasonStaleRunner = "this Mac's macOS runner is too old for GitHub; update hived and run hived macos setup again"

// ReasonNotClean is why a Mac declines macOS jobs for a while after
// processes a job left could not be stopped.
const ReasonNotClean = "processes an earlier macOS job left running as " + User + " could not be stopped; macOS jobs are declined for ten minutes"

// holdNotClean is how long that lasts. The next job's clean tries again.
const holdNotClean = 10 * time.Minute

// request is what the parent writes to the child's stdin: one of three.
type request struct {
	// Check asks whether the child works and which runner it has.
	Check bool `json:"check,omitempty"`
	// Clean stops every process of hiveworker's but the child's own, and
	// removes what jobs left in its jobs directory.
	Clean bool `json:"clean,omitempty"`
	// Job runs one.
	Job *jobRequest `json:"job,omitempty"`
	// NoIndex is the owner's spotlight-exclude, which the child cannot read
	// for itself: whether the runner and the jobs live in hive.noindex.
	NoIndex bool `json:"noIndex,omitempty"`
}

type jobRequest struct {
	ID string `json:"id"`
	// JITConfig is a credential. It is read from stdin and handed to the
	// runner; it is never written to disk by this package.
	JITConfig string `json:"jitConfig"`
}

// checkReply is the child's answer to a Check.
type checkReply struct {
	OK     bool   `json:"ok"`
	Runner string `json:"runner"`
}

// Runner implements jobs.Runner for hive-macos jobs. It is built once and
// asked often; readiness is checked at most once a minute.
type Runner struct {
	Log *slog.Logger
	// Executable is hived itself, as the sudo rule names it.
	Executable string
	// command starts the child. Nil is `hived macos-job` through sudo.
	command func(ctx context.Context) *exec.Cmd

	// busy is held through a job and the cleans around it. A clean stops
	// every process hiveworker has, a second job's or a check's too.
	busy sync.Mutex

	mu      sync.Mutex
	checked time.Time
	report  Report
	// held declines jobs until then, after a clean failed.
	held  time.Time
	stale atomic.Bool
}

var _ jobs.Runner = (*Runner)(nil)
var _ jobs.Unusable = (*Runner)(nil)
var _ jobs.Limiter = (*Runner)(nil)

// New returns a runner for this Mac. It does nothing until asked.
func New(log *slog.Logger) (*Runner, error) {
	exe, err := executable()
	if err != nil {
		return nil, err
	}
	return &Runner{Log: log, Executable: exe}, nil
}

func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// SetupOptions is how setup talks to the person running it.
type SetupOptions struct {
	// Yes skips the confirmation.
	Yes    bool
	Stdin  io.Reader
	Stdout io.Writer
}

// Report is what Status finds, in the order setup fixes it.
type Report struct {
	Platform string
	Xcode    string
	// UserExists is whether the hiveworker account is there.
	UserExists bool
	// Sudo is whether the rule works: the child answered through it.
	Sudo bool
	// Runner is the runner version the child has, or "".
	Runner string
	// Problem is the first thing wrong, in words the owner can act on, or
	// "" when everything is ready.
	Problem string
}

// Ready reports whether macOS jobs can run now. It is cheap to call often.
func (r *Runner) Ready() bool {
	return r.Unusable() == ""
}

// Unusable implements jobs.Unusable.
func (r *Runner) Unusable() string {
	if r.stale.Load() {
		return ReasonStaleRunner
	}
	r.mu.Lock()
	held := time.Now().Before(r.held)
	r.mu.Unlock()
	if held {
		return ReasonNotClean
	}
	return r.status(false).Problem
}

// Limit implements jobs.Limiter: one macOS job at a time, since every one
// runs as the one user and a clean stops all that user's processes.
func (r *Runner) Limit(limits *jobs.Limits) {
	limits.MostMacOS = 1
}

// Status checks everything now and says what it found.
func (r *Runner) Status() Report {
	return r.status(true)
}

func (r *Runner) status(fresh bool) Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !fresh && !r.checked.IsZero() && time.Since(r.checked) < time.Minute {
		return r.report
	}
	// A job's clean would kill the check's child: what was found before
	// stands until the job is over.
	if !r.busy.TryLock() {
		return r.report
	}
	defer r.busy.Unlock()
	r.report = inspect(r.Executable)
	r.checked = time.Now()
	return r.report
}

// Run implements jobs.Runner: the job runs as the hiveworker user, through
// the sudo rule, and returns the runner's exit code. hiveworker is cleaned
// before the job and after it, however it ended. A failed clean after the
// job does not change how the job ended; it holds off the next.
func (r *Runner) Run(ctx context.Context, job jobs.Job) (jobs.Result, error) {
	r.busy.Lock()
	defer r.busy.Unlock()
	if err := r.clean("before"); err != nil {
		return jobs.Result{ExitCode: -1}, err
	}
	result, err := r.run(ctx, job)
	_ = r.clean("after")
	return result, err
}

func (r *Runner) run(ctx context.Context, job jobs.Job) (jobs.Result, error) {
	started := time.Now()
	cmd := r.child(ctx)
	cmd.Stderr = os.Stderr
	// Cancelled or out of time: sudo relays the signal, and the child kills
	// the runner and cleans up before it goes.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return jobs.Result{ExitCode: -1}, err
	}
	if err := cmd.Start(); err != nil {
		return jobs.Result{ExitCode: -1}, err
	}
	err = json.NewEncoder(stdin).Encode(request{Job: &jobRequest{ID: job.AssignmentID, JITConfig: job.JITConfig}, NoIndex: spotlightExcluded()})
	stdin.Close()
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return jobs.Result{ExitCode: -1}, err
	}
	err = cmd.Wait()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
		err = nil
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if code == ExitRunnerDeprecated && err == nil {
		r.stale.Store(true)
		r.Log.Error("GitHub refused this Mac's runner as too old; macOS jobs are declined until hived is updated", "runner", RunnerVersion)
	}
	r.Log.Info("macOS job ended", "job", job.AssignmentID, "exit", code, "seconds", int(time.Since(started).Seconds()))
	return jobs.Result{ExitCode: code}, err
}

// clean asks the child to stop every process hiveworker has. A process a
// job started in a session of its own escapes the kill of the runner's
// process group, and would otherwise run on, as the same user, into the
// next job. It has its own time limit, so it runs after a cancelled job
// too. When it fails, macOS jobs are declined for a while.
func (r *Runner) clean(when string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := r.child(ctx)
	cmd.Stdin = strings.NewReader(`{"clean":true}`)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return nil
	}
	r.mu.Lock()
	r.held = time.Now().Add(holdNotClean)
	r.mu.Unlock()
	said := strings.TrimSpace(stderr.String())
	r.Log.Error("could not stop what was running as "+User+"; macOS jobs are declined for ten minutes", "when", when, "error", err, "said", said)
	return fmt.Errorf("cleaning %s %s the job: %w: %s", User, when, err, said)
}

// child is `hived macos-job` as the hiveworker user. -n: a rule that would
// prompt is no rule.
func (r *Runner) child(ctx context.Context) *exec.Cmd {
	if r.command != nil {
		return r.command(ctx)
	}
	return exec.CommandContext(ctx, "/usr/bin/sudo", "-n", "-u", User, r.Executable, "macos-job")
}

// askChild runs `hived macos-job` as the hiveworker user with one request
// and returns what it printed. -n: a rule that would prompt is no rule.
func askChild(exe string, ask request) ([]byte, error) {
	ask.NoIndex = spotlightExcluded()
	raw, err := json.Marshal(ask)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("/usr/bin/sudo", "-n", "-u", User, exe, "macos-job")
	cmd.Stdin = strings.NewReader(string(raw))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// DownloadRunner fetches GitHub's runner for this Mac into dir, checks it
// against the pinned checksum, and returns its path. Setup unpacks it as
// root; a macOS VM is handed the same tarball for each job.
func DownloadRunner(ctx context.Context, dir string) (string, error) {
	path := filepath.Join(dir, RunnerFile)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, runnerURL, nil)
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading the runner: GitHub answered %d", response.StatusCode)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	_, err = io.Copy(io.MultiWriter(file, sum), response.Body)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != RunnerSHA256 {
		os.Remove(path)
		return "", fmt.Errorf("the downloaded runner does not match its checksum (%s)", got)
	}
	return path, nil
}
