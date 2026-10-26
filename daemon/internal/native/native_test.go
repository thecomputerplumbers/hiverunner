package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/jobs"
)

// jitConfig stands in for the JIT config GitHub hands a runner.
const jitConfig = "example-jit-config"

// TestMain lets the test binary stand in for `hived macos-job`.
func TestMain(m *testing.M) {
	if os.Getenv("NATIVE_TEST_CHILD") == "1" {
		os.Exit(fakeChild())
	}
	os.Exit(m.Run())
}

// fakeChild notes each request in NATIVE_TEST_LOG and answers it as the
// environment says: a clean exits NATIVE_TEST_CLEAN, or after a job
// NATIVE_TEST_CLEAN_AFTER; a job NATIVE_TEST_JOB, or waits for its SIGTERM
// when that is "wait".
func fakeChild() int {
	var ask request
	if err := json.NewDecoder(os.Stdin).Decode(&ask); err != nil {
		return 2
	}
	note := func(line string) {
		file, err := os.OpenFile(os.Getenv("NATIVE_TEST_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			file.WriteString(line + "\n")
			file.Close()
		}
	}
	code := func(name string) int {
		n, _ := strconv.Atoi(os.Getenv(name))
		return n
	}
	if spotlight := os.Getenv("NATIVE_TEST_SPOTLIGHT"); spotlight != "" {
		if file, err := os.OpenFile(spotlight, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			file.WriteString(strconv.FormatBool(ask.NoIndex) + "\n")
			file.Close()
		}
	}
	switch {
	case ask.Clean:
		note("clean")
		if raw, _ := os.ReadFile(os.Getenv("NATIVE_TEST_LOG")); strings.Contains(string(raw), "job") {
			return code("NATIVE_TEST_CLEAN_AFTER")
		}
		return code("NATIVE_TEST_CLEAN")
	case ask.Job != nil:
		note("job " + ask.Job.ID + " " + strconv.FormatBool(ask.Job.JITConfig == jitConfig))
		if os.Getenv("NATIVE_TEST_JOB") == "wait" {
			stop := make(chan os.Signal, 1)
			signal.Notify(stop, syscall.SIGTERM)
			<-stop
			return 143
		}
		return code("NATIVE_TEST_JOB")
	}
	return 2
}

// fakeRunner is a Runner whose child is fakeChild, and the file it notes in.
func fakeRunner(t *testing.T, env ...string) (*Runner, string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "log")
	runner := &Runner{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	runner.command = func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0])
		cmd.Env = append(os.Environ(), append([]string{"NATIVE_TEST_CHILD=1", "NATIVE_TEST_LOG=" + log}, env...)...)
		return cmd
	}
	return runner, log
}

func noted(t *testing.T, log string) []string {
	t.Helper()
	raw, err := os.ReadFile(log)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(raw)), "\n", " | "))
}

// Every job runs as one user, so a process one job left running, in a
// session of its own, would run on into the next job. The child is asked to
// stop all of them before the job and after it.
func TestRunCleansBeforeAndAfterTheJob(t *testing.T) {
	runner, log := fakeRunner(t, "NATIVE_TEST_JOB=3")
	result, err := runner.Run(context.Background(), jobs.Job{AssignmentID: "a1", JITConfig: jitConfig})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 3 {
		t.Fatalf("exit %d, want the job's 3", result.ExitCode)
	}
	want := "clean | job a1 true | clean"
	if got := strings.Join(noted(t, log), " "); got != want {
		t.Fatalf("the child was asked: %s; want %s", got, want)
	}
}

// Every request arranges the child's layout for the spotlight-exclude it
// carries, so a clean that left it out would move the caches out of
// hive.noindex before each job and back for it.
func TestEveryRequestCarriesSpotlightExclude(t *testing.T) {
	for _, on := range []bool{true, false} {
		dir := t.TempDir()
		t.Setenv("HIVE_CONFIG", filepath.Join(dir, "machine.json"))
		if !on {
			if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"spotlightExclude":false}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if spotlightExcluded() != on {
			t.Fatalf("spotlight-exclude reads as %v, want %v", !on, on)
		}
		spotlight := filepath.Join(dir, "spotlight")
		runner, _ := fakeRunner(t, "NATIVE_TEST_SPOTLIGHT="+spotlight)
		if _, err := runner.Run(context.Background(), jobs.Job{AssignmentID: "a1", JITConfig: jitConfig}); err != nil {
			t.Fatal(err)
		}
		want := strings.Repeat(strconv.FormatBool(on)+" ", 3)
		if got := strings.Join(strings.Fields(read(t, spotlight)), " ") + " "; got != want {
			t.Fatalf("spotlight-exclude %v: the clean, job and clean carried %s", on, got)
		}
	}
}

func TestRunCleansAfterACancelledJob(t *testing.T) {
	runner, log := fakeRunner(t, "NATIVE_TEST_JOB=wait")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for !strings.Contains(strings.Join(noted(t, log), " "), "job") {
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	_, err := runner.Run(ctx, jobs.Job{AssignmentID: "a2", JITConfig: jitConfig})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want cancelled", err)
	}
	want := "clean | job a2 true | clean"
	if got := strings.Join(noted(t, log), " "); got != want {
		t.Fatalf("the child was asked: %s; want %s", got, want)
	}
}

// A clean that leaves processes running starts no job, and the Mac declines
// macOS jobs for a while.
func TestRunStartsNoJobWhenACleanFails(t *testing.T) {
	runner, log := fakeRunner(t, "NATIVE_TEST_CLEAN="+strconv.Itoa(ExitNotClean))
	result, err := runner.Run(context.Background(), jobs.Job{AssignmentID: "a3", JITConfig: jitConfig})
	if err == nil || result.ExitCode != -1 {
		t.Fatalf("exit %d, err %v; want a failure", result.ExitCode, err)
	}
	if got := strings.Join(noted(t, log), " "); got != "clean" {
		t.Fatalf("the child was asked: %s; want only a clean", got)
	}
	if reason := runner.Unusable(); reason != ReasonNotClean {
		t.Fatalf("unusable %q, want %q", reason, ReasonNotClean)
	}
}

// A clean after the job that fails does not change how the job ended, and
// holds off the next.
func TestRunHoldsJobsWhenTheCleanAfterFails(t *testing.T) {
	runner, log := fakeRunner(t, "NATIVE_TEST_CLEAN_AFTER="+strconv.Itoa(ExitNotClean))
	result, err := runner.Run(context.Background(), jobs.Job{AssignmentID: "a4", JITConfig: jitConfig})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("exit %d, err %v; want the job's own 0", result.ExitCode, err)
	}
	if got := strings.Join(noted(t, log), " "); got != "clean | job a4 true | clean" {
		t.Fatalf("the child was asked: %s", got)
	}
	if reason := runner.Unusable(); reason != ReasonNotClean {
		t.Fatalf("unusable %q, want %q", reason, ReasonNotClean)
	}
}

// Since a clean stops every process the user has, two jobs as that user
// cannot run at once.
func TestOneMacOSJobAtATime(t *testing.T) {
	var runner jobs.Runner = &Runner{}
	limiter, ok := runner.(jobs.Limiter)
	if !ok {
		t.Fatal("the native runner sets no limits of its own")
	}
	limits := jobs.Limits{MaxJobs: 4}
	limiter.Limit(&limits)
	if limits.MostMacOS != 1 {
		t.Fatalf("MostMacOS %d, want 1", limits.MostMacOS)
	}
}

// The JIT config is a credential: it reaches the runner in its environment,
// which it removes from its own, never on a command line.
func TestRunnerCommandKeepsTheJITConfigOffTheCommandLine(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf 'args=%s\\n' \"$*\"\nprintf 'jit=%s\\n' \"$ACTIONS_RUNNER_INPUT_JITCONFIG\"\n"
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := runnerCommand(context.Background(), dir, t.TempDir(), jitConfig)
	for _, arg := range cmd.Args {
		if strings.Contains(arg, jitConfig) {
			t.Fatalf("the JIT config is on the command line: %q", cmd.Args)
		}
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if want := "args=\njit=" + jitConfig + "\n"; out.String() != want {
		t.Fatalf("run.sh saw %q, want %q", out.String(), want)
	}
}

// A runner a job could write would be what the next job runs.
func TestRunnerIsOutsideTheJobsHome(t *testing.T) {
	if strings.HasPrefix(RunnerDir(), Home+"/") {
		t.Fatalf("the runner %s is in %s", RunnerDir(), Home)
	}
}

func TestCheckRunner(t *testing.T) {
	me := uint32(os.Getuid())
	build := func(t *testing.T) (top, dir string) {
		top = t.TempDir()
		dir = filepath.Join(top, "runner", "2.0.0")
		if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		for path, mode := range map[string]os.FileMode{"run.sh": 0o755, "bin/Runner.Listener": 0o755, "config.sh": 0o644} {
			if err := os.WriteFile(filepath.Join(dir, path), []byte("x"), mode); err != nil {
				t.Fatal(err)
			}
			// Whatever the umask was.
			if err := os.Chmod(filepath.Join(dir, path), mode); err != nil {
				t.Fatal(err)
			}
		}
		for _, d := range []string{top, filepath.Join(top, "runner"), dir, filepath.Join(dir, "bin")} {
			if err := os.Chmod(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink("run.sh", filepath.Join(dir, "link")); err != nil {
			t.Fatal(err)
		}
		return top, dir
	}
	t.Run("one owner, read-only to others", func(t *testing.T) {
		top, dir := build(t)
		if err := checkRunner(top, dir, me); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("another owner", func(t *testing.T) {
		top, dir := build(t)
		if err := checkRunner(top, dir, me+1); err == nil {
			t.Fatal("a runner of another user's passed")
		}
	})
	broken := map[string]func(t *testing.T, top, dir string) error{
		"a file others may write": func(_ *testing.T, _, dir string) error {
			return os.Chmod(filepath.Join(dir, "bin", "Runner.Listener"), 0o757)
		},
		"a directory the group may write": func(_ *testing.T, _, dir string) error {
			return os.Chmod(filepath.Join(dir, "bin"), 0o775)
		},
		"a directory above it others may write": func(_ *testing.T, top, _ string) error {
			return os.Chmod(filepath.Join(top, "runner"), 0o777)
		},
		"a setuid file": func(_ *testing.T, _, dir string) error {
			return os.Chmod(filepath.Join(dir, "config.sh"), os.ModeSetuid|0o755)
		},
		"a fifo": func(_ *testing.T, _, dir string) error {
			return syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o644)
		},
		"no run.sh": func(_ *testing.T, _, dir string) error {
			return os.Remove(filepath.Join(dir, "run.sh"))
		},
	}
	for name, breakIt := range broken {
		t.Run(name, func(t *testing.T) {
			top, dir := build(t)
			if err := breakIt(t, top, dir); err != nil {
				t.Fatal(err)
			}
			if err := checkRunner(top, dir, me); err == nil {
				t.Fatal("passed")
			}
		})
	}
	t.Run("a directory above it that is a link", func(t *testing.T) {
		top, dir := build(t)
		if err := os.Rename(filepath.Join(top, "runner"), filepath.Join(top, "elsewhere")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("elsewhere", filepath.Join(top, "runner")); err != nil {
			t.Fatal(err)
		}
		if err := checkRunner(top, dir, me); err == nil {
			t.Fatal("passed")
		}
	})
	t.Run("outside top", func(t *testing.T) {
		top, _ := build(t)
		if err := checkRunner(top, t.TempDir(), me); err == nil {
			t.Fatal("passed")
		}
	})
}

// fakeProcesses is a process table: a kill takes the process killed, unless
// it comes back, which it does with a new pid.
type fakeProcesses struct {
	running  []process
	respawns map[string]bool
	signals  []sent
	err      error
}

type sent struct {
	pid int
	sig syscall.Signal
}

func (f *fakeProcesses) Signal(pid int, sig syscall.Signal) error {
	f.signals = append(f.signals, sent{pid, sig})
	if sig != syscall.SIGKILL {
		return nil
	}
	var still []process
	for _, p := range f.running {
		switch {
		case p.PID != pid:
			still = append(still, p)
		case f.respawns[p.Name]:
			still = append(still, process{PID: p.PID + 1000, Name: p.Name})
		}
	}
	f.running = still
	return nil
}

func (f *fakeProcesses) Left() ([]process, error) { return f.running, f.err }

func TestSweep(t *testing.T) {
	t.Run("what escaped the job's process group", func(t *testing.T) {
		system := &fakeProcesses{running: []process{{PID: 10, Name: "sleep"}, {PID: 11, Name: "nohup"}}}
		left, err := sweep(system, time.Second, time.Millisecond)
		if err != nil || len(left) != 0 {
			t.Fatalf("left %v, err %v", left, err)
		}
		want := []sent{{10, syscall.SIGSTOP}, {11, syscall.SIGSTOP}, {10, syscall.SIGKILL}, {11, syscall.SIGKILL}}
		if !slices.Equal(system.signals, want) {
			t.Fatalf("signals %v, want both stopped, then both killed", system.signals)
		}
	})
	t.Run("nothing running", func(t *testing.T) {
		system := &fakeProcesses{}
		left, err := sweep(system, time.Second, time.Millisecond)
		if err != nil || len(left) != 0 || len(system.signals) != 0 {
			t.Fatalf("left %v, err %v, signals %v", left, err, system.signals)
		}
	})
	t.Run("something that comes back", func(t *testing.T) {
		system := &fakeProcesses{running: []process{{PID: 10, Name: "agent"}}, respawns: map[string]bool{"agent": true}}
		left, err := sweep(system, 20*time.Millisecond, time.Millisecond)
		if err != nil || len(left) != 1 || left[0].Name != "agent" {
			t.Fatalf("left %v, err %v; want the agent", left, err)
		}
	})
	t.Run("processes that cannot be listed", func(t *testing.T) {
		if _, err := sweep(&fakeProcesses{err: errors.New("no")}, time.Second, time.Millisecond); err == nil {
			t.Fatal("no error")
		}
	})
}

// The real thing, scoped to processes this test started: a job's process in
// a session of its own outlives the kill of the runner's process group, as
// it would on a Mac, and a sweep stops it.
func TestSweepStopsWhatEscapedTheProcessGroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	setsid, err := exec.LookPath("setsid")
	if err != nil {
		t.Skip("no setsid")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	script := "#!/bin/sh\n" + setsid + " sleep 300 </dev/null >/dev/null 2>&1 &\necho $! >" + pidFile + "\nexec sleep 300\n"
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := runnerCommand(ctx, dir, t.TempDir(), jitConfig)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var escaped int
	for deadline := time.Now().Add(5 * time.Second); escaped == 0 && time.Now().Before(deadline); {
		raw, _ := os.ReadFile(pidFile)
		escaped, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		time.Sleep(10 * time.Millisecond)
	}
	if escaped == 0 {
		t.Fatal("the job did not start its process")
	}
	t.Cleanup(func() { syscall.Kill(escaped, syscall.SIGKILL) })
	// The kill of the process group, which is all there was before.
	cancel()
	_ = cmd.Wait()
	time.Sleep(50 * time.Millisecond)
	if !alive(escaped) {
		t.Skip("setsid here did not leave the process group")
	}
	left, err := sweep(scoped{escaped}, 5*time.Second, 10*time.Millisecond)
	if err != nil || len(left) != 0 {
		t.Fatalf("left %v, err %v", left, err)
	}
	if alive(escaped) {
		t.Fatal("the escaped process still runs")
	}
}

// scoped is a process table of the given pids alone, so a test signals
// nothing it did not start.
type scoped []int

func (s scoped) Signal(pid int, sig syscall.Signal) error {
	if slices.Contains(s, pid) {
		_ = syscall.Kill(pid, sig)
	}
	return nil
}

func (s scoped) Left() ([]process, error) {
	var left []process
	for _, pid := range s {
		if alive(pid) {
			left = append(left, process{PID: pid})
		}
	}
	return left, nil
}

// alive is whether pid runs and is not a zombie.
func alive(pid int) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	fields := strings.Fields(string(raw[bytes.LastIndexByte(raw, ')')+1:]))
	return len(fields) > 0 && fields[0] != "Z" && fields[0] != "X"
}

// The runner a job clones may be root's, as setup once unpacked it, or the
// owner's whose daemon started the child, as sudo says; never the child's
// own user's, or a job could change what the next job runs.
func TestRunnerOwners(t *testing.T) {
	me := os.Getuid()
	for env, want := range map[string][]uint32{
		"":                       {0},
		"not a number":           {0},
		"0":                      {0},
		strconv.Itoa(me):         {0},
		strconv.Itoa(me + 12345): {0, uint32(me + 12345)},
	} {
		t.Setenv("SUDO_UID", env)
		if got := runnerOwners(); !slices.Equal(got, want) {
			t.Errorf("SUDO_UID=%q: %v, want %v", env, got, want)
		}
	}
}

// Auto-update waits for the owner on a release whose setup is newer than
// this one's; a manifest that says nothing of setup waits for no one.
func TestNeedsSetup(t *testing.T) {
	if NeedsSetup(0) || NeedsSetup(SetupVersion) || !NeedsSetup(SetupVersion+1) {
		t.Fatal("wrong")
	}
}
