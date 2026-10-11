// hived is the HiveWorker daemon: it enrolls this machine in its
// organization's pool and holds the tunnel jobs arrive through.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/api"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/bench"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/config"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/connection"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/host"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/jobs"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/native"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/pools"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/settings"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/update"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/vm"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.0.0-dev"

const defaultServer = "https://hiveworkr.thecomputerplumbers.com"

const usage = `hived, the HiveWorker daemon

  hived login       Sign in with GitHub and enroll this machine
  hived link        Add this machine to another of your hives
  hived unlink      Stop serving one hive, and keep serving the others
  hived hives       List the hives this machine serves
  hived run         Connect to your hive and wait for jobs
  hived start       Start the daemon the installer set up; stop, restart too
  hived status      Show what this machine lends, and whether it is lending;
                    --json for a script
  hived config      Show or change this machine's settings
  hived doctor      Check this machine keeps taking jobs with nobody at it
  hived cache       Show or empty this machine's Actions cache
  hived macos       Set this Mac up for macOS jobs (Xcode, iOS builds)
  hived update      Update to the current release, if this is not it
  hived pause       Stop taking jobs until you say otherwise
  hived resume      Take jobs again
  hived logout      Forget this machine's token
  hived uninstall   Remove HiveWorker from this machine
  hived selftest    Boot a throwaway VM and report what it finds
  hived benchmark   Measure this machine's speed score
  hived version

Set HIVE_SERVER to use a server other than ` + defaultServer + `.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "login":
		err = login(ctx, os.Args[2:])
	case "link":
		err = link(ctx, os.Args[2:])
	case "unlink":
		err = unlink(ctx, os.Args[2:])
	case "hives":
		err = hives(ctx)
	case "run":
		err = run(ctx)
	case "start":
		err = start()
	case "stop":
		err = halt()
	case "restart":
		if err = halt(); err == nil {
			err = start()
		}
	case "status":
		err = status(os.Args[2:])
	case "config":
		err = configure(os.Args[2:])
	case "doctor":
		err = doctor()
	case "cache":
		err = cacheCommand(os.Args[2:])
	case "macos":
		err = macos(ctx, os.Args[2:])
	case "update":
		err = updateCommand(ctx)
	case "macos-job":
		// The child that runs one macOS job as the hiveworker user. Only
		// ever started by hived itself, through sudo.
		os.Exit(native.Serve(ctx, os.Stdin, os.Stdout))
	case "pause":
		err = pause(true)
	case "resume":
		err = pause(false)
	case "logout":
		err = config.Remove()
	case "uninstall":
		err = uninstall(os.Args[2:])
	case "selftest":
		err = selftest(ctx)
	case "dev-job":
		err = devJob(ctx, os.Args[2:])
	case "benchmark":
		err = benchmark(os.Args[2:])
	case "version", "--version":
		fmt.Println(version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if errors.Is(err, errStartAgain) {
		// Everything run started is stopped by now. The same process, begun
		// again, so it works the same under a service manager and without.
		err = startAgain()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hived:", err)
		os.Exit(1)
	}
}

// errStartAgain is how run asks to be begun again from the top: what it
// read once at the start, where the image and the cache are, has changed.
var errStartAgain = errors.New("starting again")

func startAgain() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}

func server() string {
	if override := os.Getenv("HIVE_SERVER"); override != "" {
		return override
	}
	return defaultServer
}

func login(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("login", flag.ExitOnError)
	hive := flags.String("hive", "", "hive ids to serve, separated by commas, if you belong to several")
	// The name this flag had before a pool was called a hive.
	flags.StringVar(hive, "pool", "", "the same as --hive")
	noBrowser := flags.Bool("no-browser", false, "print the sign-in link instead of opening it")
	_ = flags.Parse(args)

	// Signing in again would enroll this computer a second time, as a new
	// machine with a new token. Someone here for another hive wants link.
	if enrolled, err := config.Load(); err == nil && enrolled.Server == api.New(server()).Server {
		switch serving, err := served(ctx, enrolled); {
		case err == nil:
			fmt.Printf("This machine is already signed in. %s\n", serving)
			fmt.Println("To add it to another hive: hived link")
			fmt.Println("To sign in as someone else: hived logout, then hived login")
			return nil
		case !errors.Is(err, api.ErrUnknownMachine):
			return fmt.Errorf("this machine is already signed in, but the server could not be asked about it: %w", err)
		}
	}

	facts, err := host.ReadFacts()
	if err != nil {
		return err
	}
	fmt.Printf("Measuring this machine (%s, %d cores)...\n", facts.Model, facts.Cores)
	score := bench.Score(bench.Run(3 * time.Second))

	client := api.New(server())
	token, err := signIn(ctx, client, *noBrowser)
	if err != nil {
		return err
	}

	machine := api.Machine{
		Name:          facts.Name,
		Model:         facts.Model,
		OS:            facts.OS,
		Arch:          facts.Arch,
		Cores:         facts.Cores,
		MemoryBytes:   facts.MemoryBytes,
		DaemonVersion: version,
		SpeedScore:    score,
	}
	if *hive != "" {
		machine.OrganizationIDs = strings.Split(*hive, ",")
	}
	enrollment, err := enrollChoosing(machine, func(machine api.Machine) (api.Enrollment, error) {
		return client.Enroll(ctx, token, machine)
	}, func(pools []api.Pool) ([]api.Pool, error) {
		return askPools("You belong to more than one hive. Which should this machine serve?", pools, func(pool api.Pool) string {
			return fmt.Sprintf("hived login --hive %s    (%s)", pool.ID, pool.Name)
		})
	})
	var refusal *api.Error
	if errors.As(err, &refusal) && refusal.Code == "NO_HIVE" {
		// Signed in fine, but in no hive. Retrying cannot help; say what can.
		return fmt.Errorf("your GitHub account is not in a hive yet.\n\n"+
			"A hive is a GitHub organization with HiveWorker installed on it. Three steps:\n\n"+
			"  1. Have a GitHub organization. Creating one is free; a personal account cannot be a hive.\n"+
			"  2. Install HiveWorker on it:  %s/install\n"+
			"     Choose the organization, not your personal account. If you are not an owner,\n"+
			"     GitHub asks the owners to approve it.\n"+
			"  3. Run \"hived login\" again and approve the new code.\n\n"+
			"Joining a teammate's hive instead? Ask them for the invite link on their dashboard.", client.Server)
	}
	if err != nil {
		return err
	}
	if err := config.Save(config.Machine{
		Server:    client.Server,
		MachineID: enrollment.MachineID,
		Token:     enrollment.Token,
	}); err != nil {
		return err
	}
	names := make([]string, len(enrollment.Pools))
	for i, joined := range enrollment.Pools {
		names[i] = joined.Name
	}
	fmt.Printf("\n%s now serves: %s\n", facts.Name, strings.Join(names, ", "))
	for _, refused := range enrollment.Refused {
		fmt.Printf("Not added to %s: %s\n", refused.Name, refused.Message)
	}
	fmt.Println("Start it with: hived run")
	return nil
}

// enrollChoosing enrolls a machine, and when the server wants to be told
// which hives, asks and enrolls again with the answer. What it returns is
// how the last attempt went, not the question that came before it.
func enrollChoosing(machine api.Machine, enroll func(api.Machine) (api.Enrollment, error), ask func([]api.Pool) ([]api.Pool, error)) (api.Enrollment, error) {
	enrollment, err := enroll(machine)
	var choose *api.ChoosePoolError
	if !errors.As(err, &choose) {
		return enrollment, err
	}
	chosen, err := ask(choose.Pools)
	if err != nil {
		return api.Enrollment{}, err
	}
	machine.OrganizationIDs = ids(chosen)
	return enroll(machine)
}

func run(ctx context.Context) error {
	machine, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	log.Info("starting", "version", version, "machine", machine.MachineID)
	live, err := settings.Watch()
	if err != nil {
		return err
	}
	live.OnError = func(err error) {
		log.Warn("keeping the last settings that could be read", "error", err)
	}
	// Whether the owner would mind a job right now: their settings, then
	// what the machine itself is doing.
	holding := func() string { return live.Get().Holding(host.ReadPower(), time.Now()) }
	// Said at the start and whenever it becomes true, while there is someone
	// to read it: closed and asleep, the daemon says nothing at all.
	var said atomic.Value
	said.Store("")
	warnAsleep := func() {
		problem, fix := asleep(live.Get(), host.ReadPower())
		if said.Swap(problem) != problem && problem != "" {
			log.Warn(problem, "fix", fix)
		}
	}
	warnAsleep()
	refresh := time.Minute
	if seconds, err := strconv.Atoi(os.Getenv("HIVE_REFRESH_SECONDS")); err == nil && seconds > 0 {
		refresh = time.Duration(seconds) * time.Second
	}
	// Everything started here is stopped, and waited for, before run
	// returns: a job's VM, the warm VM, and their files on disk.
	ctx, stop := context.WithCancel(ctx)
	tended := make(chan struct{})
	close(tended)
	unlock := func() {}
	defer func() {
		stop()
		<-tended
		// Last: the work directory stays ours until every VM is gone, so a
		// daemon started as this one exits cannot sweep one mid-teardown.
		unlock()
	}()

	// macOS jobs, if the owner has set this Mac up for them. Built now,
	// asked later: whether it is ready is checked when the setting is on.
	var nativeRunner *native.Runner
	if runtime.GOOS == "darwin" {
		if built, err := native.New(log); err != nil {
			log.Warn("macOS jobs are not available", "error", err)
		} else {
			nativeRunner = built
		}
	}
	macOS := func() bool {
		return nativeRunner != nil && live.Get().MacOSJobs && nativeRunner.Ready()
	}
	var native jobs.Runner
	if nativeRunner != nil {
		native = nativeRunner
	}

	// What the owner asked to be kept somewhere else is moved there now,
	// before either place is opened.
	moved, err := settle(live.Get())
	for _, what := range moved {
		log.Info(what)
	}
	if err != nil {
		log.Warn("could not move everything; carrying on from where it is", "error", err)
	}
	settled, gone := loadMoves(), absent(live.Get())

	// Without a guest image the machine still connects and reports presence;
	// it declines jobs and says why.
	var runner jobs.Runner
	if vmRunner, err := newRunner(log, live.Get()); err != nil {
		log.Warn("this machine cannot run jobs yet", "reason", err)
	} else {
		runner = vmRunner
		// One hived per work directory. Holding it is also what makes the
		// sweep below safe: with the lock held, nothing else can be
		// using what is in there.
		release, err := vm.LockWorkDir(vmRunner.WorkDir)
		if err != nil {
			return err
		}
		unlock = release
		// A daemon that was killed leaves its VMs' disks, and possibly a
		// job's runner configuration, on disk. Remove them before anything
		// else runs.
		// The machine's own Actions cache, offered to each job's VM.
		if store := offerCache(vmRunner, log, live.Get); store != nil {
			go keepCacheTrimmed(ctx, store)
		}
		if swept, err := vm.SweepWorkDir(vmRunner.WorkDir); err != nil {
			log.Warn("could not clear what a previous run left behind", "error", err)
		} else if swept > 0 {
			log.Info("cleared what a previous run left behind", "directories", swept)
		}
		// One VM of the default size stays booted while the machine is
		// available, so a job skips the boot. The owner's `warm` setting
		// turns it off and on while running; HIVE_WARM=0 turns it off for
		// good.
		if os.Getenv("HIVE_WARM") != "0" {
			vmRunner.WarmVCPUs = warmVCPUs
			tended = make(chan struct{})
			go func() {
				defer close(tended)
				vmRunner.Tend(ctx, func() bool { return live.Get().Warm && holding() == "" })
			}()
		}
		log.Info("ready to run jobs", "runner", vmRunner.Image.RunnerVersion, "image", vmRunner.Image.ImageVersion, "warm", vmRunner.WarmVCPUs)
	}
	limits := func() jobs.Limits {
		current := live.Get()
		return jobs.Limits{MaxJobs: current.MaxJobs, MaxVCPUs: current.MaxVCPUs, Cores: runtime.NumCPU(), MacOS: macOS()}
	}
	slot := jobs.New(runner, log)
	slot.Native(native)
	slot.Limit(limits)

	// Once a day, whether a release is out. If the owner allows it, the
	// daemon updates itself when it has no job, which ends this process.
	var updating atomic.Bool
	go update.Watch(ctx, update.Options{
		Version:    version,
		Log:        log,
		AutoUpdate: func() bool { return live.Get().AutoUpdate },
		Busy:       slot.Busy,
		Updating:   &updating,
	})

	// A place that changed, or a disk that came back, is picked up by
	// starting again once no job is running.
	var moving, again atomic.Bool
	go func() {
		if waitToMove(ctx, live.Get, settled, gone, slot.Busy, &moving) {
			log.Info("starting again: where this machine keeps its image or cache has changed")
			again.Store(true)
			stop()
		}
	}()

	err = pools.Run(ctx, pools.Options{
		Connections:   connection.NewRecorder(),
		Client:        api.New(machine.Server),
		Token:         machine.Token,
		DaemonVersion: version,
		RefreshEvery:  refresh,
		Jobs:          slot,
		Log:           log,
		// Paused, not online, when the machine cannot take a job: the pool
		// then stops offering it any, instead of being declined each time.
		Available: func() bool {
			// A machine that cannot run a job at all, for want of an image
			// or a hypervisor, is not waiting for one. Unless it takes
			// macOS jobs, which need neither.
			vm := runner != nil
			if unusable, ok := runner.(jobs.Unusable); ok && unusable.Unusable() != "" {
				vm = false
			}
			if !vm && !macOS() {
				return false
			}
			// An update, or a move, is waiting for the jobs in hand to finish.
			if updating.Load() || moving.Load() {
				return false
			}
			warnAsleep()
			return holding() == ""
		},
		Limits: limits,
	})
	if again.Load() {
		return errStartAgain
	}
	return err
}

// waitToMove returns true when the daemon should start again: its owner
// has moved the image or the cache since it started, or a disk that was
// missing then is attached now. It sets moving first, which stops new jobs,
// and waits for the ones in hand. Only a change since the start counts, so a
// move that failed then is not tried again in a loop.
func waitToMove(ctx context.Context, current func() settings.Settings, settled moves, gone []string, busy func() bool, moving *atomic.Bool) bool {
	changed := func() bool {
		now := loadMoves()
		if !sameMove(now.Data, settled.Data) || !sameMove(now.Cache, settled.Cache) {
			return true
		}
		return len(gone) > 0 && len(absent(current())) == 0
	}
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for !changed() {
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
	moving.Store(true)
	for busy() {
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
	return ctx.Err() == nil
}

func sameMove(a, b *move) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// updateCommand is `hived update`: the installer, run again, if there is a
// newer release than this one. The daemon's own service is restarted by it.
func updateCommand(ctx context.Context) error {
	state, _ := update.LoadState()
	latest, _, err := update.Check(ctx, &state)
	_ = update.SaveState(state)
	if err != nil {
		return fmt.Errorf("could not check for a release: %w", err)
	}
	if !update.Newer(version, latest.Version) {
		fmt.Printf("hived %s is the current release.\n", version)
		if latest.Version != version {
			fmt.Printf("(The published release is %s; this build is not from it.)\n", latest.Version)
		}
		return nil
	}
	fmt.Printf("Updating hived %s to %s. A job running on this machine would be cancelled;\nthe daemon is restarted at the end.\n\n", version, latest.Version)
	_, err = update.Apply(ctx, false)
	return err
}

func newRunner(log *slog.Logger, current settings.Settings) (*vm.Runner, error) {
	dir, err := imageDir(current)
	if err != nil {
		return nil, err
	}
	image, err := vm.LoadImage(dir)
	if err != nil {
		return nil, err
	}
	// Said now, in words the owner can act on, rather than by every job
	// failing later.
	if err := vm.Preflight(); err != nil {
		return nil, err
	}
	return &vm.Runner{Image: image, WorkDir: workDir(dir), Log: log}, nil
}

func selftest(ctx context.Context) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	current, err := settings.Load()
	if err != nil {
		return err
	}
	runner, err := newRunner(log, current)
	if err != nil {
		return err
	}
	// Held while the VM exists, so a daemon starting meanwhile cannot sweep
	// it away as a leftover.
	release, err := vm.LockWorkDir(runner.WorkDir)
	if errors.Is(err, vm.ErrWorkDirInUse) {
		// Almost always the daemon. The test boots a VM in the directory
		// the daemon keeps its own in, so they cannot share it.
		return fmt.Errorf("%w\nThe daemon is running, and a self-test needs it stopped. A job it is running stops with it.\n\n  hived stop && hived selftest; hived start", err)
	}
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	offerCache(runner, log, func() settings.Settings { return current })
	started := time.Now()
	report, err := runner.SelfTest(ctx, 2)
	report = tidyReport(report, runner.Image)
	fmt.Print(report)
	if err != nil {
		return err
	}
	if err := reportFailure(report); err != nil {
		return err
	}
	fmt.Printf("A VM booted, ran and was destroyed in %.1fs.\n", time.Since(started).Seconds())
	return nil
}

// reportFailure is what a guest's report says is wrong with it, or nil.
func reportFailure(report string) error {
	// The network first: a VM that cannot reach GitHub runs no job, whatever
	// else it could not reach.
	if !strings.Contains(report, "NETWORK=ok") {
		return errors.New("the VM booted but could not reach GitHub")
	}
	if strings.Contains(report, "CACHE=failed") {
		return errors.New("the VM booted but could not reach this machine's Actions cache")
	}
	// Only a guest that tried and failed. An image built before Docker was
	// in it says nothing of Docker, and still runs the jobs it always ran.
	if strings.Contains(report, "DOCKER=failed") {
		return errors.New("the VM booted but Docker could not run a container in it")
	}
	return nil
}

// tidyReport is the guest's report as its owner should read it: each line
// once, and what the VM is as well as what it found. An image built before
// the guest said its own architecture is described from its manifest.
func tidyReport(report string, image vm.Image) string {
	seen := map[string]bool{}
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(report, "\n"), "\n") {
		if line != "" && seen[line] {
			continue
		}
		seen[line] = true
		lines = append(lines, line)
	}
	if !strings.Contains(report, "ARCH=") {
		lines = append(lines, "ARCH="+image.Arch())
	}
	lines = append(lines, "IMAGE="+strconv.Itoa(image.ImageVersion))
	return strings.Join(lines, "\n") + "\n"
}

// devJob runs one job from a JIT config in a file, with no coordinator
// involved. It is how the VM runner is exercised against GitHub on its own.
// The file is a credential; it is read and never echoed.
func devJob(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("dev-job", flag.ExitOnError)
	file := flags.String("jitconfig-file", "", "path to a file holding an encoded JIT runner config")
	vcpus := flags.Int("vcpus", 2, "vCPUs for the VM")
	minutes := flags.Int("timeout-minutes", 30, "destroy the VM after this long")
	_ = flags.Parse(args)
	if *file == "" {
		return errors.New("--jitconfig-file is required")
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	current, err := settings.Load()
	if err != nil {
		return err
	}
	runner, err := newRunner(log, current)
	if err != nil {
		return err
	}
	release, err := vm.LockWorkDir(runner.WorkDir)
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(*minutes)*time.Minute)
	defer cancel()
	code, err := runner.Run(ctx, jobs.Job{
		AssignmentID: "dev",
		JITConfig:    strings.TrimSpace(string(raw)),
		RunnerName:   "dev",
		VCPUs:        *vcpus,
	})
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("the runner exited %d", code)
	}
	return nil
}

func benchmark(args []string) error {
	flags := flag.NewFlagSet("benchmark", flag.ExitOnError)
	raw := flags.Bool("raw", false, "print units per second, for calibrating the reference")
	seconds := flags.Int("seconds", 5, "how long to measure")
	_ = flags.Parse(args)
	unitsPerSecond := bench.Run(time.Duration(*seconds) * time.Second)
	if *raw {
		fmt.Printf("%.2f\n", unitsPerSecond)
		return nil
	}
	fmt.Printf("%.2f units/s on one core: speed score %.2f\n", unitsPerSecond, bench.Score(unitsPerSecond))
	return nil
}
