// hived is the HiveWorker daemon: it enrolls this machine in its
// organization's pool and holds the tunnel jobs arrive through.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/api"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/bench"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/config"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/host"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/jobs"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/pools"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/settings"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/vm"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.0.0-dev"

const defaultServer = "https://hiveworkr.thecomputerplumbers.com"

const usage = `hived, the HiveWorker daemon

  hived login       Sign in with GitHub and add this machine to your pool
  hived run         Connect to the pool and wait for jobs
  hived status      Show what this machine lends, and whether it is lending
  hived config      Show or change this machine's settings
  hived cache       Show or empty this machine's Actions cache
  hived pause       Stop taking jobs until you say otherwise
  hived resume      Take jobs again
  hived logout      Forget this machine's token
  hived uninstall   Remove HiveWorker from this Mac
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
	case "run":
		err = run(ctx)
	case "status":
		err = status()
	case "config":
		err = configure(os.Args[2:])
	case "cache":
		err = cacheCommand(os.Args[2:])
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
	if err != nil {
		fmt.Fprintln(os.Stderr, "hived:", err)
		os.Exit(1)
	}
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

	facts, err := host.ReadFacts()
	if err != nil {
		return err
	}
	fmt.Printf("Measuring this machine (%s, %d cores)...\n", facts.Model, facts.Cores)
	score := bench.Score(bench.Run(3 * time.Second))

	client := api.New(server())
	code, err := client.StartDevice(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("\nTo add this machine, approve this code in your browser:\n\n    %s\n\n%s\n\n", code.UserCode, code.VerificationURIComplete)
	if !*noBrowser && runtime.GOOS == "darwin" {
		_ = exec.Command("/usr/bin/open", code.VerificationURIComplete).Start()
	}
	fmt.Println("Waiting for approval...")
	token, err := client.AwaitToken(ctx, code)
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
	enrollment, err := client.Enroll(ctx, token, machine)
	var choose *api.ChoosePoolError
	if errors.As(err, &choose) {
		machine.OrganizationIDs, err = askPools(choose.Pools)
		if err != nil {
			return err
		}
		enrollment, err = client.Enroll(ctx, token, machine)
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

// askPools lets someone in several organizations choose which of them this
// machine should serve. It can serve all of them at once.
func askPools(pools []api.Pool) ([]string, error) {
	fmt.Println("\nYou belong to more than one hive. Which should this machine serve?")
	for i, pool := range pools {
		fmt.Printf("  %d. %s\n", i+1, pool.Name)
	}
	fmt.Print("Numbers separated by commas, or \"all\": ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return nil, errors.New("no hive chosen; run again with --hive")
	}
	answer := strings.TrimSpace(line)
	var chosen []string
	if strings.EqualFold(answer, "all") {
		for _, pool := range pools {
			chosen = append(chosen, pool.ID)
		}
		return chosen, nil
	}
	for _, part := range strings.Split(answer, ",") {
		choice, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || choice < 1 || choice > len(pools) {
			return nil, errors.New("that is not one of the hives listed")
		}
		chosen = append(chosen, pools[choice-1].ID)
	}
	return chosen, nil
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

	// Without a guest image the machine still connects and reports presence;
	// it declines jobs and says why.
	var runner jobs.Runner
	if vmRunner, err := newRunner(log); err != nil {
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
			vmRunner.WarmVCPUs = 2
			tended = make(chan struct{})
			go func() {
				defer close(tended)
				vmRunner.Tend(ctx, func() bool { return live.Get().Warm && holding() == "" })
			}()
		}
		log.Info("ready to run jobs", "runner", vmRunner.Image.RunnerVersion, "image", vmRunner.Image.ImageVersion, "warm", vmRunner.WarmVCPUs)
	}
	return pools.Run(ctx, pools.Options{
		Client:        api.New(machine.Server),
		Token:         machine.Token,
		DaemonVersion: version,
		RefreshEvery:  refresh,
		Runner:        runner,
		Log:           log,
		// Paused, not online, when the machine cannot take a job: the pool
		// then stops offering it any, instead of being declined each time.
		Available: func() bool {
			if unusable, ok := runner.(jobs.Unusable); ok && unusable.Unusable() != "" {
				return false
			}
			return holding() == ""
		},
		Limits: func() jobs.Limits {
			current := live.Get()
			return jobs.Limits{MaxJobs: current.MaxJobs, MaxVCPUs: current.MaxVCPUs, Cores: runtime.NumCPU()}
		},
	})
}

func newRunner(log *slog.Logger) (*vm.Runner, error) {
	dir, err := vm.DefaultImageDir()
	if err != nil {
		return nil, err
	}
	image, err := vm.LoadImage(dir)
	if err != nil {
		return nil, err
	}
	// Beside the image, so a job's disk is a clone and not a copy, and named
	// after it, so two daemons on different images (as in development) keep
	// separate job directories and separate network identities.
	work := filepath.Join(filepath.Dir(dir), filepath.Base(dir)+"-jobs")
	return &vm.Runner{Image: image, WorkDir: work, Log: log}, nil
}

func selftest(ctx context.Context) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	runner, err := newRunner(log)
	if err != nil {
		return err
	}
	// Held while the VM exists, so a daemon starting meanwhile cannot sweep
	// it away as a leftover.
	release, err := vm.LockWorkDir(runner.WorkDir)
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	offerCache(runner, log, func() settings.Settings {
		current, _ := settings.Load()
		return current
	})
	started := time.Now()
	report, err := runner.SelfTest(ctx, 2)
	fmt.Print(report)
	if err != nil {
		return err
	}
	if strings.Contains(report, "CACHE=failed") {
		return errors.New("the VM booted but could not reach this machine's Actions cache")
	}
	if !strings.Contains(report, "NETWORK=ok") {
		return errors.New("the VM booted but could not reach GitHub")
	}
	fmt.Printf("A VM booted, ran and was destroyed in %.1fs.\n", time.Since(started).Seconds())
	return nil
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
	runner, err := newRunner(log)
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
