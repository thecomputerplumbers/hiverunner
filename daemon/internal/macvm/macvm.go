package macvm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/jobs"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/native"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/vm"
)

const (
	// MostVMs is how many macOS VMs a Mac runs at once: Apple's licence
	// allows two besides the Mac's own macOS.
	MostVMs = 2
	// VCPUs is a job VM's size, which is what a hive-macos job is billed as.
	VCPUs = 4
	// InTag is the virtio-fs tag of what the host hands the guest, which it
	// can only read; OutTag is the one it writes its results to.
	InTag  = "hive"
	OutTag = "hive-out"
	// DigestFile is the image's digest, handed in, which the guest agent
	// gives the job as HIVE_IMAGE_DIGEST.
	DigestFile = "image-digest"
	// runnerShared is GitHub's runner, handed in, which the guest agent
	// unpacks onto the VM's own disk.
	runnerShared = "runner.tar.gz"
)

// ReasonStaleRunner is why a Mac declines macOS jobs for a while after a
// job's runner said GitHub refused it as too old, and GitHub has a newer one.
const ReasonStaleRunner = "a macOS VM's runner was refused by GitHub as too old, so macOS jobs are declined for an hour; if it happens again, update hived and run hived macos image seal"

// ReasonStuck is why a Mac declines macOS jobs while VMs that would not
// stop hold every place Apple's licence allows.
const ReasonStuck = "a macOS VM that would not stop holds this Mac's last place for one; it is declining macOS jobs until it stops"

// What the guest may leave where its results go: a few small files.
const (
	mostOutBytes   = 1 << 20
	mostOutEntries = 64
)

// How long a VM has to boot and log in before the job is given up on, how
// long the guest has to power off once it has reported, how long a stopped
// VM has to go, and how long a refused runner declines jobs. Variables so
// the tests need not wait.
var (
	bootTimeout   = 5 * time.Minute
	powerOffGrace = 30 * time.Second
	stopGrace     = 5 * time.Second
	stopWait      = 15 * time.Second
	staleFor      = time.Hour
	poll          = time.Second
	// releases is where GitHub says which runner is its latest.
	releases = "https://api.github.com/repos/actions/runner/releases/latest"
	// runnerSHA256 is what the runner handed to each VM must hash to.
	runnerSHA256 = native.RunnerSHA256
)

// Memory is what a job's VM is given on a host with this much: as much as a
// Linux VM of the same size.
func Memory(hostBytes uint64) uint64 { return vm.GuestMemoryBytes(VCPUs, hostBytes) }

// Runner implements jobs.Runner for hive-macos jobs, each in a VM of its
// own. It is built once and asked often; readiness is checked at most once
// a minute.
type Runner struct {
	// Dir is the image. WorkDir holds each job's clone while it runs, and
	// must be on the same volume, so a clone takes no room until written.
	Dir     string
	WorkDir string
	Log     *slog.Logger
	// HostMemory is the Mac's memory, for what each VM is given.
	HostMemory uint64

	mu sync.Mutex
	// slots are the VMs running, by network identity. stuck counts those
	// held by a VM that would not stop, whose job has already ended.
	slots   [MostVMs]bool
	stuck   int
	checked time.Time
	problem string
	// staleUntil is when a runner GitHub refused stops declining jobs, in
	// Unix nanoseconds. It expires because a job can write any exit code:
	// one job must not switch macOS jobs off on this Mac for good.
	staleUntil atomic.Int64
	// claiming guards claimed and unclaim, apart from mu, so that offers are
	// not held up by a sweep. unclaim is kept so the lock's file is never
	// collected, which would drop the lock.
	claiming sync.Mutex
	claimed  bool
	unclaim  func()
	// launch and clone stand in for the host's own in tests, and latest
	// for GitHub.
	launch func(*instance) error
	clone  func(from, to string) error
	latest func(context.Context) (string, error)
}

var _ jobs.Runner = (*Runner)(nil)
var _ jobs.Unusable = (*Runner)(nil)
var _ jobs.Limiter = (*Runner)(nil)

// ErrFull means MostVMs are already running. The pool is told the limit,
// so an offer past it is declined before it gets here.
var ErrFull = errors.New("this Mac is already running as many macOS VMs as Apple's licence allows")

// Ready reports whether a job could start now.
func (r *Runner) Ready() bool { return r.Unusable() == "" }

// Unusable implements jobs.Unusable.
func (r *Runner) Unusable() string {
	if time.Now().UnixNano() < r.staleUntil.Load() {
		return ReasonStaleRunner
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stuck >= MostVMs {
		return ReasonStuck
	}
	if r.checked.IsZero() || time.Since(r.checked) > time.Minute {
		r.problem = r.inspect()
		r.checked = time.Now()
	}
	return r.problem
}

// Limit implements jobs.Limiter: two VMs at most, fewer while one that
// would not stop holds a place, and each one's memory free on the host.
func (r *Runner) Limit(limits *jobs.Limits) {
	r.mu.Lock()
	defer r.mu.Unlock()
	limits.MostMacOS = max(MostVMs-r.stuck, 1)
	limits.MacOSMemory = Memory(r.HostMemory)
}

// Status checks now and says what it found.
func (r *Runner) Status() (Image, string) {
	image, err := Load(r.Dir)
	if err != nil {
		return image, err.Error()
	}
	return image, r.inspect()
}

func (r *Runner) inspect() string {
	if problem := platformProblem(); problem != "" {
		return problem
	}
	image, err := Load(r.Dir)
	if err != nil {
		return err.Error()
	}
	return image.Problem()
}

// Claim makes the work directory this process's and clears what a daemon
// that died left there: a clone can hold whatever its job wrote, a signing
// key among it. The daemon claims it as it starts, whichever way it runs
// macOS jobs; a Mac with neither an image nor a work directory has nothing
// to claim, and its first job claims it.
func (r *Runner) Claim() error {
	if _, err := os.Stat(r.WorkDir); err != nil {
		if _, err := os.Stat(r.Dir); err != nil {
			return nil
		}
	}
	return r.claim()
}

func (r *Runner) claim() error {
	r.claiming.Lock()
	defer r.claiming.Unlock()
	if r.claimed {
		return nil
	}
	// Never given back: the kernel drops it when the process ends.
	release, err := vm.LockWorkDir(r.WorkDir)
	if err != nil {
		return err
	}
	swept, err := vm.SweepWorkDir(r.WorkDir)
	if err != nil {
		release()
		return err
	}
	if swept > 0 && r.Log != nil {
		r.Log.Info("cleared the macOS VMs a previous run left behind", "directories", swept)
	}
	r.unclaim, r.claimed = release, true
	return nil
}

// instance is one job's VM and everything on the host that belongs to it.
type instance struct {
	dir string
	// in is handed to the guest to read; out is where it writes.
	in, out string
	// The clones, and the image's hardware model, which is not cloned:
	// nothing writes it.
	disk, auxiliary, hardwareModel string
	mac                            net.HardwareAddr
	vcpus                          int
	// ended is closed, by end, when the VM stops; failure says whether it
	// stopped any way but powering itself off, and is written before.
	ended   chan struct{}
	ending  sync.Once
	failure error
	// stop pulls the plug. It is safe to call more than once, and on a VM
	// that has already stopped.
	stop func()
}

// end says the VM has stopped. It may be said more than once.
func (guest *instance) end() { guest.ending.Do(func() { close(guest.ended) }) }

func (guest *instance) alive() bool {
	select {
	case <-guest.ended:
		return false
	default:
		return true
	}
}

// Run implements jobs.Runner: a clone of the image, booted, handed the job,
// waited for, stopped and deleted. Run returns once the clone is gone, or
// once it is plain that the VM will not stop: see release.
func (r *Runner) Run(ctx context.Context, job jobs.Job) (jobs.Result, error) {
	started := time.Now()
	if err := r.claim(); err != nil {
		return jobs.Result{ExitCode: -1}, err
	}
	slot, err := r.take()
	if err != nil {
		return jobs.Result{ExitCode: -1}, err
	}
	guest, image, err := r.prepare(job, slot)
	if err != nil {
		r.give(slot)
		return jobs.Result{ExitCode: -1}, err
	}
	// Whatever happens: the VM stops and its clone and credential go.
	defer r.release(guest, slot, job.AssignmentID, image.Digest)
	launch := r.launch
	if launch == nil {
		launch = start
	}
	if err := launch(guest); err != nil {
		// Nothing to wait for: a VM that did not start is not running.
		guest.end()
		return jobs.Result{ExitCode: -1}, fmt.Errorf("start the macOS VM: %w", err)
	}
	r.Log.Info("macOS VM has the job", "job", job.AssignmentID, "image", image.Digest, "vcpus", guest.vcpus)

	err = guest.wait(ctx)
	code, hasExit := readExit(guest.out)
	if !hasExit && err == nil {
		err = errors.New("the macOS VM stopped without reporting a result")
	}
	if code == native.ExitRunnerDeprecated && err == nil {
		r.refused()
	}
	r.Log.Info("macOS job ended", "job", job.AssignmentID, "exit", code, "seconds", int(time.Since(started).Seconds()))
	return jobs.Result{ExitCode: code}, err
}

// refused is a job's word that GitHub refused its runner as too old. A job
// can write any exit code, so it is believed only when GitHub's latest
// runner is newer than the one hived pins; then macOS jobs are declined for
// a while, and asked about again after.
func (r *Runner) refused() {
	latest := r.latest
	if latest == nil {
		latest = latestRunner
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	newest, err := latest(ctx)
	switch {
	case err != nil:
		r.Log.Warn("a macOS job says GitHub refused its runner, and GitHub could not be asked whether a newer one is out; carrying on", "runner", native.RunnerVersion, "error", err)
	case !older(native.RunnerVersion, newest):
		r.Log.Warn("a macOS job says GitHub refused its runner, but it is GitHub's latest; carrying on", "runner", native.RunnerVersion)
	default:
		r.staleUntil.Store(time.Now().Add(staleFor).UnixNano())
		r.Log.Error("GitHub refused the macOS VMs' runner as too old; macOS jobs are declined for an hour", "runner", native.RunnerVersion, "latest", newest)
	}
}

// latestRunner asks GitHub which runner release is its latest.
func latestRunner(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, releases, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub answered %d", response.StatusCode)
	}
	var release struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&release); err != nil {
		return "", err
	}
	return strings.TrimPrefix(release.Tag, "v"), nil
}

// older reports whether version a comes before version b, as 2.337.0 does
// before 2.338.0. A version that does not read as one is not older.
func older(a, b string) bool {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(left), len(right)) {
		x, y := 0, 0
		var err error
		if i < len(left) {
			if x, err = strconv.Atoi(left[i]); err != nil {
				return false
			}
		}
		if i < len(right) {
			if y, err = strconv.Atoi(right[i]); err != nil {
				return false
			}
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// take reserves one of the VMs.
func (r *Runner) take() (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for slot, used := range r.slots {
		if !used {
			r.slots[slot] = true
			return slot, nil
		}
	}
	return -1, ErrFull
}

func (r *Runner) give(slot int) {
	r.mu.Lock()
	r.slots[slot] = false
	r.mu.Unlock()
}

// prepare makes the job's directory: clones of the image's disk and
// auxiliary storage, and what is handed in, the job's configuration last.
// The image is held while it is checked and cloned, so what is cloned is
// what was checked, and the digest handed in is that image's.
func (r *Runner) prepare(job jobs.Job, slot int) (*instance, Image, error) {
	release, err := lockImage(r.Dir, false)
	if err != nil {
		return nil, Image{}, err
	}
	defer release()
	image, err := Load(r.Dir)
	if err == nil {
		if problem := image.Problem(); problem != "" {
			err = errors.New(problem)
		}
	}
	if err != nil {
		return nil, Image{}, err
	}
	dir, err := os.MkdirTemp(r.WorkDir, "job-")
	if err != nil {
		return nil, Image{}, err
	}
	vcpus := job.VCPUs
	if vcpus < 1 {
		vcpus = VCPUs
	}
	guest := &instance{
		dir:           dir,
		in:            filepath.Join(dir, "in"),
		out:           filepath.Join(dir, "out"),
		disk:          filepath.Join(dir, DiskFile),
		auxiliary:     filepath.Join(dir, AuxiliaryFile),
		hardwareModel: image.path(HardwareModelFile),
		mac:           mac(r.WorkDir, slot),
		vcpus:         vcpus,
		ended:         make(chan struct{}),
		stop:          func() {},
	}
	fail := func(err error) (*instance, Image, error) {
		os.RemoveAll(dir)
		return nil, Image{}, err
	}
	// The whole of what the guest can see of the host.
	for _, shared := range []string{guest.in, guest.out} {
		if err := os.Mkdir(shared, 0o700); err != nil {
			return fail(err)
		}
	}
	clone := r.clone
	if clone == nil {
		clone = cloneFile
	}
	runner := filepath.Join(guest.in, runnerShared)
	for from, to := range map[string]string{
		image.path(DiskFile):      guest.disk,
		image.path(AuxiliaryFile): guest.auxiliary,
		image.RunnerPath():        runner,
	} {
		if err := clone(from, to); err != nil {
			return fail(fmt.Errorf("clone %s: %w", filepath.Base(from), err))
		}
	}
	// The runner is not part of the image's digest, and anything running
	// as this Mac's owner could have replaced it since it was fetched.
	if err := checkRunner(runner); err != nil {
		return fail(err)
	}
	handed := map[string]string{"host": hostName(), DigestFile: image.Digest}
	if job.Label != "" {
		handed["label"] = job.Label
	}
	for name, value := range handed {
		if err := writeIn(filepath.Join(guest.in, name), []byte(value)); err != nil {
			return fail(err)
		}
	}
	temporary := filepath.Join(dir, "jitconfig.tmp")
	if err := writeIn(temporary, []byte(job.JITConfig)); err != nil {
		return fail(err)
	}
	if err := os.Rename(temporary, filepath.Join(guest.in, "jitconfig")); err != nil {
		return fail(err)
	}
	return guest, image, nil
}

// checkRunner reports whether the runner at path is the one hived pins.
func checkRunner(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return err
	}
	if hex.EncodeToString(sum.Sum(nil)) != runnerSHA256 {
		return errors.New("the runner beside the macOS image does not match its checksum; run: hived macos image seal")
	}
	return nil
}

// wait returns when the VM has stopped, when the guest has reported and
// had its moment to power off, or when ctx ends, which stops the VM. A VM
// whose agent has not said it is ready within bootTimeout is stopped too:
// an image that does not boot, or has no agent, would otherwise hold the
// job until its timeout.
func (guest *instance) wait(ctx context.Context) error {
	booted := time.Now()
	ready := false
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		select {
		case <-guest.ended:
			return guest.failure
		case <-ctx.Done():
			_ = writeIn(filepath.Join(guest.in, "stop-reason"), []byte(stopReason(ctx)+"\n"))
			select {
			case <-guest.ended:
			case <-time.After(stopGrace):
			}
			return ctx.Err()
		case <-tick.C:
		}
		if !ready && present(filepath.Join(guest.out, "ready")) {
			// The agent reads the configuration before it says so, and
			// nothing else in the VM needs it.
			ready = true
			_ = os.Remove(filepath.Join(guest.in, "jitconfig"))
		}
		if overfull(guest.out) {
			return errors.New("the macOS VM wrote more where its results go than results can be")
		}
		if _, ok := readExit(guest.out); ok {
			select {
			case <-guest.ended:
			case <-time.After(powerOffGrace):
			}
			return nil
		}
		if !ready && time.Since(booted) > bootTimeout {
			return errors.New("the macOS VM did not start its agent; check the image with hived macos image open")
		}
	}
}

// overfull reports whether the guest has left more where its results go
// than a few small files. It is looked at as often as the results are, so
// it bounds a mistake rather than a guest set on filling the disk between
// two looks; macOS has no cheap quota for one directory.
func overfull(out string) bool {
	entries, size := 0, int64(0)
	err := filepath.WalkDir(out, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || path == out {
			return err
		}
		entries++
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() {
			size += info.Size()
		}
		if entries > mostOutEntries || size > mostOutBytes {
			return fs.SkipAll
		}
		return nil
	})
	return err == nil && (entries > mostOutEntries || size > mostOutBytes)
}

func stopReason(ctx context.Context) string {
	cause := context.Cause(ctx)
	switch {
	case errors.Is(cause, jobs.ErrTimeout) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return jobs.ReasonTimeout
	case errors.Is(cause, jobs.ErrReleased):
		return jobs.ReasonReleased
	}
	return jobs.ReasonDaemonShutdown
}

// release stops the VM if it is still running, then deletes its files and
// gives back its slot. A VM that has not stopped within stopWait keeps
// both: deleting a disk from under a running VM, or starting another beside
// it past Apple's licence, would be worse than waiting. They go when it
// does stop.
func (r *Runner) release(guest *instance, slot int, id, digest string) {
	if guest.alive() {
		guest.stop()
		select {
		case <-guest.ended:
		case <-time.After(stopWait):
		}
	}
	gone := func() {
		if err := os.RemoveAll(guest.dir); err != nil {
			r.Log.Error("could not delete a macOS VM's clone; the next start of hived will", "job", id, "error", err)
		} else {
			r.Log.Info("macOS VM deleted", "job", id, "image", digest)
		}
		r.give(slot)
	}
	if guest.alive() {
		r.Log.Error("a macOS VM did not stop when told to; its clone and its place stay taken until it does", "job", id)
		// Its job is over, so the pool no longer counts it: this does.
		r.mu.Lock()
		r.stuck++
		r.mu.Unlock()
		go func() {
			<-guest.ended
			r.mu.Lock()
			r.stuck--
			r.mu.Unlock()
			gone()
		}()
		return
	}
	gone()
}

// mac is a VM's network identity. The host's network remembers each card it
// has seen for a while, so each slot keeps one, derived from the work
// directory so that two daemons on one Mac do not share them.
func mac(workDir string, slot int) net.HardwareAddr {
	seed := sha256.Sum256([]byte(workDir + "\x00macos"))
	// 0x02: locally administered, unicast.
	return net.HardwareAddr{0x02, 0x6d, seed[0], seed[1], seed[2], seed[3]&0xf0 | byte(slot)}
}

func hostName() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "this machine"
	}
	return name
}
