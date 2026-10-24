// hived is the Hive Runner daemon: it enrolls this machine in its
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
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/api"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/bench"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/config"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/connection"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/cooldown"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/fence"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/host"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/jobs"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/macvm"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/native"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/pools"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/update"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/vm"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.0.0-dev"

const defaultServer = "https://hiverunner.dev"

const usage = `hived, the Hive Runner daemon

  hived login       Sign in with GitHub and enroll this machine
  hived link        Add this machine to another of your hives
  hived unlink      Stop serving one hive, and keep serving the others
  hived hives       List the hives this machine serves; --json for a script
  hived run         Connect to your hive and wait for jobs
  hived start       Start the daemon the installer set up; stop, restart too
  hived status      Show what this machine lends, and whether it is lending;
                    --json for a script
  hived config      Show or change this machine's settings
  hived doctor      Check this machine keeps taking jobs with nobody at it;
                    --sudo to read the firewall and sshd as root too
  hived cache       Show or empty this machine's Actions cache
  hived macos       Set this Mac up for macOS jobs (Xcode, iOS builds)
  hived update      Update the daemon and guest image to the current
                    release, once the jobs it is running have finished;
                    --now to not wait
  hived pause       Stop taking jobs until you say otherwise
  hived resume      Take jobs again
  hived logout      Forget this machine's token; --revoke to end it on the
                    server too, so it works nowhere
  hived uninstall   Remove Hive Runner from this machine
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
		err = hives(ctx, os.Args[2:])
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
		err = doctor(os.Args[2:])
	case "cache":
		err = cacheCommand(os.Args[2:])
	case "macos":
		err = macos(ctx, os.Args[2:])
	case "update":
		err = updateCommand(ctx, os.Args[2:])
	case "drain":
		// What the installer runs before it replaces a running daemon.
		err = drainCommand(ctx, os.Args[2:])
	case "macos-job":
		// The child that runs one macOS job as the hiveworker user. Only
		// ever started by hived itself, through sudo.
		os.Exit(native.Serve(ctx, os.Stdin, os.Stdout))
	case "pause":
		err = pause(true)
	case "resume":
		err = pause(false)
	case "logout":
		err = logout(ctx, os.Args[2:])
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
	hive := flags.String("hive", "", "the hives to serve if you are in several, separated by commas: as GitHub names the organization, or by id; \"all\" for every one")
	// The name this flag had before a pool was called a hive.
	flags.StringVar(hive, "pool", "", "the same as --hive")
	noBrowser := flags.Bool("no-browser", false, "print the sign-in link instead of opening it")
	enrollKey := flags.String("enroll-key", "", "an enroll key from a hive owner or admin, in place of signing in at a browser; HIVE_ENROLL_KEY says the same and keeps it out of the process list")
	_ = flags.Parse(args)
	if *enrollKey == "" {
		*enrollKey = os.Getenv("HIVE_ENROLL_KEY")
	}
	if *enrollKey != "" && !strings.HasPrefix(*enrollKey, "hwe_") {
		return errors.New("that is not an enroll key: one starts with hwe_")
	}

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
	// A key stands in for the person: no code to approve, and the machine
	// is whoever minted the key's.
	token := *enrollKey
	if token == "" {
		if token, err = signIn(ctx, client, *noBrowser); err != nil {
			return err
		}
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
	everyHive := strings.EqualFold(*hive, "all")
	if *hive != "" && !everyHive {
		machine.OrganizationIDs = splitHives(*hive)
	}
	enrollment, err := enrollChoosing(machine, func(machine api.Machine) (api.Enrollment, error) {
		return client.Enroll(ctx, token, machine)
	}, func(pools []api.Pool) ([]api.Pool, error) {
		if everyHive {
			return pools, nil
		}
		return askPools("You belong to more than one hive. Which should this machine serve?", pools, func(pool api.Pool) string {
			return "hived login --hive " + pool.Name
		}, "hived login --hive all")
	})
	var refusal *api.Error
	if errors.As(err, &refusal) && refusal.Code == "NO_HIVE" {
		// Signed in fine, but in no hive. Retrying cannot help; say what can.
		return fmt.Errorf("your GitHub account is not in a hive yet.\n\n"+
			"A hive is a GitHub organization with Hive Runner installed on it. Three steps:\n\n"+
			"  1. Have a GitHub organization. Creating one is free; a personal account cannot be a hive.\n"+
			"  2. Install Hive Runner on it:  %s/install\n"+
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
	// A machine enrolled now has no jobs a fence could break, so it starts
	// with the strictest; see settings.Isolation.
	if current, err := settings.Load(); err == nil && current.NetworkIsolation == "" && vm.FencesNetwork {
		current.NetworkIsolation = settings.IsolationBlock
		if settings.Save(current) == nil {
			fmt.Println("Jobs here reach the internet only, not this machine's LAN or tailnet. To change that: hived config set network-isolation lan-allow")
		}
	}
	names := make([]string, len(enrollment.Pools))
	for i, joined := range enrollment.Pools {
		names[i] = joined.Name
	}
	fmt.Printf("\n%s now serves: %s\n", facts.Name, strings.Join(names, ", "))
	for _, refused := range enrollment.Refused {
		fmt.Printf("Not added to %s: %s\n", refused.Name, refused.Message)
	}
	// The installer starts the daemon itself once this returns.
	if os.Getenv("HIVE_INSTALLING") == "1" {
		return nil
	}
	// One the installer set up and could not start, for want of a hive, is
	// started now: this is the one command it said was left. Not for a
	// machine file of another name, which that service does not read.
	if _, err := installedService(); err == nil && os.Getenv("HIVE_CONFIG") == "" {
		return start()
	}
	fmt.Println("Start it with: hived run")
	return nil
}

// splitHives is the hives a --hive flag names, without the spaces a person
// may type after a comma.
func splitHives(flag string) []string {
	var hives []string
	for _, hive := range strings.Split(flag, ",") {
		if hive = strings.TrimSpace(hive); hive != "" {
			hives = append(hives, hive)
		}
	}
	return hives
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
	// An update that waited for this machine's jobs paused it to do so. A
	// daemon starting is what it was waiting to do, so the pause is over.
	if err := endDrain(); err != nil {
		log.Warn("could not lift the pause an update put on", "error", err)
	}
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

	// macOS jobs, if the owner has set this Mac up for them, run either as
	// the hiveworker user or in a throwaway macOS VM, as macos-isolation
	// says. Built now, once anything waiting to move has moved, and asked
	// later: whether it is ready is checked when the setting is on.
	var macosJobs *macosRunner
	if runtime.GOOS == "darwin" {
		macosJobs = newMacOSRunner(log, live.Get)
		if err := macosJobs.claim(); err != nil {
			log.Warn("could not clear what a previous run's macOS VMs left behind", "error", err)
		}
	}
	macOS := func() bool {
		return macosJobs != nil && live.Get().MacOSJobs && macosJobs.Unusable() == ""
	}
	var native jobs.Runner
	if macosJobs != nil {
		native = macosJobs
	}

	// Without a guest image the machine still connects and reports presence;
	// it declines jobs and says why.
	installedImage := ""
	var runner jobs.Runner
	// When the host last killed a job's VM, as the VM runner marked it.
	hostKillAt := func() time.Time { return time.Time{} }
	// A machine that cannot start a VM right now is another matter: the
	// runner is kept, declines jobs while that lasts, and says so.
	connections := connection.NewRecorder()
	if vmRunner, err := loadRunner(log, live.Get()); err != nil {
		log.Warn("this machine cannot run jobs yet", "reason", err)
	} else {
		installedImage = update.ImageID(vmRunner.Image.ImageVersion, vmRunner.Image.RunnerVersion)
		runner = vmRunner
		hostKillAt = func() time.Time { return vm.HostKillTime(vmRunner.WorkDir) }
		vmRunner.OnUnusable = connections.Unusable
		// What a job may reach is the owner's word when it starts.
		vmRunner.Network = func() fence.Policy { return live.Get().Network() }
		vmRunner.PublicDNS = func() bool { return live.Get().DNS == settings.DNSPublic }
		fenceNotice(log, live.Get())
		// Asked now so that it is in the log's first lines, where the
		// installer and the owner both look.
		vmRunner.Unusable()
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
		log.Info("ready to run jobs", "runner", vmRunner.Image.RunnerVersion, "image", vmRunner.Image.ImageVersion, "warm", warmState(vmRunner.WarmVCPUs, live.Get().Warm))
	}
	// After the host kills a job's VM, the machine lends less for a while,
	// and for longer each time it happens again. See package cooldown.
	cooling := cooldown.Watch()
	limits := func() jobs.Limits {
		current := live.Get()
		maxJobs, hold := cooling.Get().Cap(time.Now(), current.MaxJobs)
		if facts, err := host.ReadFacts(); err == nil {
			maxJobs = min(maxJobs, memoryPlan(facts, current).Jobs)
		}
		// A macOS VM's own limits are set by its runner, once it is the
		// one chosen for an offer: see jobs.Limiter. Its memory is set here
		// too, so that a Linux offer counts the macOS VMs accepted.
		limits := jobs.Limits{MaxJobs: maxJobs, MaxVCPUs: current.MaxVCPUs, Cores: runtime.NumCPU(), MacOS: macOS(), Hold: hold,
			Available: host.AvailableMemory(), Reserve: current.ReserveBytes(hostMemory()), GuestMemory: guestMemory, LeanestGuest: leanestGuest}
		if current.MacOSInVM() {
			limits.MacOSMemory = macvm.Memory(hostMemory())
		}
		limits.Planned = plannedMemory(current, limits.Available, hostMemory())
		return limits
	}
	slot := jobs.New(runner, log)
	// Every job that stops with the host's kill reports it, and a restarted
	// daemon finds the marker of one that stopped it; the marker's time is
	// what makes those one strike.
	heardOfKill := func(now, kill time.Time) {
		state, counted, err := cooling.Hit(now, kill)
		if counted {
			log.Warn("the host killed a job; lending less until the machine has had time to recover",
				"until", state.Until.Format(time.RFC3339), "strikes", state.Strikes)
		}
		if err != nil {
			log.Error("could not remember the cooldown across a restart", "error", err)
		}
	}
	slot.OnHostKilled(func() { heardOfKill(time.Now(), hostKillAt()) })
	// A kill that took hived down with it left no job to report it.
	if kill := hostKillAt(); !kill.IsZero() {
		heardOfKill(kill, kill)
	}
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
		Image:      func() string { return installedImage },
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
		Connections:   connections,
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
			if _, hold := cooling.Get().Cap(time.Now(), live.Get().MaxJobs); hold {
				return false
			}
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
// newer release than this one or the guest image is missing or behind. The
// daemon's own service is restarted by it.
func updateCommand(ctx context.Context, args []string) error {
	now := false
	for _, arg := range args {
		switch arg {
		case "--now":
			now = true
		default:
			return errors.New("usage: hived update [--now]")
		}
	}
	state, _ := update.LoadState()
	latest, _, err := update.Check(ctx, &state)
	_ = update.SaveState(state)
	if err != nil {
		return fmt.Errorf("could not check for a release: %w", err)
	}
	have := installedImageVersion()
	if !update.Needed(version, latest, have) {
		fmt.Printf("hived %s is the current release.\n", version)
		if latest.Version != version {
			fmt.Printf("(The published release is %s; this build is not from it.)\n", latest.Version)
		}
		return nil
	}
	switch {
	case update.Newer(version, latest.Version) && now:
		fmt.Printf("Updating hived %s to %s now. A job running on this machine is cancelled.\n\n", version, latest.Version)
	case update.Newer(version, latest.Version):
		fmt.Printf("Updating hived %s to %s. The daemon is restarted once this machine has no job.\n\n", version, latest.Version)
	case now:
		fmt.Printf("Updating the guest image to %s now. A job running on this machine is cancelled.\n\n", latest.Image.Version)
	default:
		fmt.Printf("Updating the guest image to %s. The daemon is restarted once this machine has no job.\n\n", latest.Image.Version)
	}
	_, err = update.Apply(ctx, false, now)
	return err
}

// installedImageVersion is the guest image on disk, as the release
// manifest names it, or "" if none is installed.
func installedImageVersion() string {
	current, err := settings.Load()
	if err != nil {
		return ""
	}
	dir, err := imageDir(current)
	if err != nil {
		return ""
	}
	image, err := vm.LoadImage(dir)
	if err != nil {
		return ""
	}
	return update.ImageID(image.ImageVersion, image.RunnerVersion)
}

// newRunner is a runner for a command that boots a VM now, and so wants to
// hear at once that this machine cannot.
func newRunner(log *slog.Logger, current settings.Settings) (*vm.Runner, error) {
	runner, err := loadRunner(log, current)
	if err != nil {
		return nil, err
	}
	// Said now, in words the owner can act on, rather than by every job
	// failing later.
	if err := vm.Preflight(); err != nil {
		return nil, err
	}
	return runner, nil
}

// loadRunner is a runner over the installed image, whether or not this
// machine can start a VM at the moment.
func loadRunner(log *slog.Logger, current settings.Settings) (*vm.Runner, error) {
	dir, err := imageDir(current)
	if err != nil {
		return nil, err
	}
	image, err := vm.LoadImage(dir)
	if err != nil {
		return nil, err
	}
	return &vm.Runner{Image: image, WorkDir: workDir(dir), Log: log, Network: current.Network,
		PublicDNS: func() bool { return current.DNS == settings.DNSPublic }}, nil
}

// fenceNotice says once, on the first start of a hived that fences jobs'
// networks, what that changed for an owner who has not chosen, and how to
// choose otherwise. An owner who has chosen knows.
func fenceNotice(log *slog.Logger, current settings.Settings) {
	if !vm.FencesNetwork || current.NetworkIsolation != "" {
		return
	}
	path, err := settings.Path()
	if err != nil {
		return
	}
	said := filepath.Join(filepath.Dir(path), "network-isolation-said")
	if _, err := os.Stat(said); err == nil {
		return
	}
	log.Warn("jobs are now kept off this machine itself, its tailnet and a cloud's metadata service, and still reach its LAN: network-isolation is lan-allow until you choose",
		"stricter", "hived config set network-isolation block",
		"as before", "hived config set network-isolation off",
		"one place", "hived config set allow-network 100.101.102.103:5000")
	_ = os.WriteFile(said, nil, 0o600)
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
	jobsWork := runner.WorkDir
	// Beside the daemon's work directory, so the daemon can stay running.
	// A job in hand would share the machine with the test VM.
	runner.WorkDir = selftestDir(runner.Image.Dir)
	if err := refuseSelftestWhileBusy(jobsWork, macosWorkDir(current)); err != nil {
		return err
	}
	release, err := vm.LockWorkDir(runner.WorkDir)
	if errors.Is(err, vm.ErrWorkDirInUse) {
		return fmt.Errorf("%w\nA self-test is already running.", err)
	}
	if err != nil {
		return err
	}
	defer release()
	if _, err := vm.SweepWorkDir(runner.WorkDir); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	// The daemon already holds the cache's vsock when it is running.
	if !vm.WorkDirInUse(jobsWork) {
		offerCache(runner, log, func() settings.Settings { return current })
	}
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

// selftestDir is where a self-test keeps its throwaway VM: beside the
// image, like a job, but not the directory the daemon holds.
func selftestDir(image string) string { return image + "-selftest" }

func refuseSelftestWhileBusy(work, macOS string) error {
	n := jobsInHand(work, macOS)
	switch n {
	case 0:
		return nil
	case 1:
		return errors.New("a job is running on this machine; wait until it finishes, then run hived selftest again")
	default:
		return fmt.Errorf("%d jobs are running on this machine; wait until they finish, then run hived selftest again", n)
	}
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
	result, err := runner.Run(ctx, jobs.Job{
		AssignmentID: "dev",
		JITConfig:    strings.TrimSpace(string(raw)),
		RunnerName:   "dev",
		VCPUs:        *vcpus,
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("the runner exited %d", result.ExitCode)
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

// plannedMemory is the memory budget jobs are admitted against where what
// is free cannot be read: on a Mac, which reclaims its caches as it is
// asked. Every job held counts against it, as if its guest had touched all
// of its memory. It is the host's memory where the owner runs macOS VMs or
// keeps a reserve, and nothing otherwise, so a Mac lending Linux jobs alone
// takes as many as it always did.
func plannedMemory(current settings.Settings, available, hostBytes uint64) uint64 {
	if available > 0 || !current.MacOSInVM() && current.ReserveMemory == "" {
		return 0
	}
	return hostBytes
}
