package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/api"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/config"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/connection"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/cooldown"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/fence"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/host"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/jobs"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/macvm"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/native"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/ui"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/update"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/vm"
)

const gib = 1 << 30

// labelFor is the runs-on label for a job size.
func labelFor(vcpus int) string {
	if vcpus == 2 {
		return "hive"
	}
	return "hive-" + strconv.Itoa(vcpus) + "cpu"
}

// refusal is why this machine does not take a job of this size, or "".
func refusal(vcpus int, facts host.Facts, current settings.Settings) string {
	switch {
	case facts.Cores < vcpus+jobs.OwnerCores:
		return fmt.Sprintf("needs %d cores", vcpus+jobs.OwnerCores)
	case !current.Takes(vcpus):
		return "above your max-vcpus"
	}
	return ""
}

// snapshot is everything hived status says, gathered once and then either
// drawn for a person or printed as JSON for a script.
type snapshot struct {
	Version string `json:"version"`
	Server  string `json:"server"`
	Machine struct {
		Name     string `json:"name"`
		Model    string `json:"model"`
		Cores    int    `json:"cores"`
		MemoryGB int64  `json:"memoryGB"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
	} `json:"machine"`
	// Hives is null when the server could not be asked.
	Hives      []string `json:"hives"`
	HivesError string   `json:"hivesError,omitempty"`
	Daemon     struct {
		Running bool   `json:"running"`
		Detail  string `json:"detail"`
	} `json:"daemon"`
	// Connection is each hive's tunnel as the running daemon last knew it;
	// empty when no daemon is running.
	Connection []tunnelState `json:"connection"`
	Jobs       struct {
		Taking bool   `json:"taking"`
		Reason string `json:"reason,omitempty"`
		Fix    string `json:"fix,omitempty"`
		// Cooldown is set while the machine lends less after the host
		// killed a job.
		Cooldown *cooldownState `json:"cooldown,omitempty"`
	} `json:"jobs"`
	Paused bool `json:"paused"`
	// Network is what a job on this machine can reach.
	Network networkState `json:"network"`
	// Labels is every runs-on label, and whether this machine takes it.
	Labels []labelState `json:"labels"`
	Image  *struct {
		Version       int    `json:"version"`
		Arch          string `json:"arch"`
		RunnerVersion string `json:"runnerVersion"`
	} `json:"image"`
	Update struct {
		Latest    string `json:"latest,omitempty"`
		Available bool   `json:"available"`
		Automatic bool   `json:"automatic"`
		// Held is why it does not install itself, or "": see update.Options.
		Held string `json:"held,omitempty"`
	} `json:"update"`
	Settings settings.Settings `json:"settings"`
}

// cooldownState is the wait after the host killed a job's VM for want of
// memory. Paused means no job at all; otherwise the machine runs one at a
// time until Until.
type cooldownState struct {
	Until   time.Time `json:"until"`
	Strikes int       `json:"strikes"`
	Paused  bool      `json:"paused"`
}

// coolingOff is nil outside a wait.
func coolingOff(wait cooldown.State, now time.Time, maxJobs int) *cooldownState {
	if !wait.Active(now) {
		return nil
	}
	_, hold := wait.Cap(now, maxJobs)
	return &cooldownState{Until: wait.Until, Strikes: wait.Strikes, Paused: hold}
}

type tunnelState struct {
	Hive      string    `json:"hive"`
	Connected bool      `json:"connected"`
	Since     time.Time `json:"since"`
	Reason    string    `json:"reason,omitempty"`
	Error     string    `json:"error,omitempty"`
}

type labelState struct {
	Label string `json:"label"`
	// Kind is linux for a job run in a VM, macos for one run on the Mac.
	Kind     string `json:"kind"`
	VCPUs    int    `json:"vcpus,omitempty"`
	MemoryGB int    `json:"memoryGB,omitempty"`
	Takes    bool   `json:"takes"`
	// Reason is why not, in a few words; Detail says how to change that.
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// macosLabel is what hive-macos jobs are sent with.
const macosLabel = "hive-macos"

// macosState is whether this machine takes macOS jobs, and if not, the
// first thing in the way: what is missing comes before the setting, since
// turning the setting on is refused until nothing is.
func macosState(current settings.Settings) labelState {
	state := labelState{Label: macosLabel, Kind: "macos"}
	if current.MacOSInVM() {
		problem := macosVMProblem(current)
		switch {
		case problem != "":
			state.Reason, state.Detail = "needs its macOS image: hived macos image", problem
		case !current.MacOSJobs:
			state.Reason, state.Detail = "macos-jobs is off", "turn it on with: hived config set macos-jobs on"
		default:
			state.Takes = true
		}
		return state
	}
	runner, err := native.New(nil)
	if err != nil {
		state.Reason, state.Detail = "not available", err.Error()
		return state
	}
	report := runner.Status()
	switch {
	case report.Problem != "" && runtime.GOOS != "darwin":
		state.Reason = "needs a Mac"
	case report.Problem != "" && runtime.GOARCH != "arm64":
		state.Reason = "needs Apple Silicon"
	case report.Problem != "" && report.Xcode == "":
		state.Reason = "needs Xcode"
	case report.Problem != "":
		state.Reason = "needs setup: hived macos setup"
	case !current.MacOSJobs:
		state.Reason, state.Detail = "macos-jobs is off", "turn it on with: hived config set macos-jobs on"
	default:
		state.Takes = true
	}
	if report.Problem != "" {
		state.Detail = report.Problem
	} else if state.Takes {
		state.Detail = report.Advice
	}
	return state
}

// macosVMProblem is why this Mac cannot run a macOS job in a VM, or "".
func macosVMProblem(current settings.Settings) string {
	dir, err := macosImageDir(current)
	if err != nil {
		return err.Error()
	}
	_, problem := (&macvm.Runner{Dir: dir, WorkDir: workDir(dir)}).Status()
	return problem
}

func gather() (snapshot, host.Facts, settings.Settings, host.Power, error) {
	var s snapshot
	machine, err := config.Load()
	if err != nil {
		return s, host.Facts{}, settings.Settings{}, host.Power{}, err
	}
	current, err := settings.Load()
	if err != nil {
		return s, host.Facts{}, settings.Settings{}, host.Power{}, err
	}
	facts, err := host.ReadFacts()
	if err != nil {
		return s, host.Facts{}, settings.Settings{}, host.Power{}, err
	}
	power := host.ReadPower()
	s.Version, s.Server, s.Settings, s.Paused = version, machine.Server, current, current.Paused
	s.Machine.Name, s.Machine.Model, s.Machine.Cores = facts.Name, facts.Model, facts.Cores
	s.Machine.MemoryGB, s.Machine.OS, s.Machine.Arch = facts.MemoryBytes/gib, facts.OS, facts.Arch

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if assignment, err := api.New(machine.Server).Me(ctx, machine.Token); err != nil {
		s.HivesError = connection.Reason(err.Error())
	} else {
		s.Hives = []string{}
		for _, pool := range assignment.Pools {
			s.Hives = append(s.Hives, pool.Name)
		}
	}

	s.Daemon.Detail = "not running; start it with: hived start"
	if _, err := installedService(); err != nil {
		s.Daemon.Detail = "not running; start it with: hived run"
	}
	var missing errAway
	if dir, err := imageDir(current); errors.As(err, &missing) {
		s.Daemon.Detail = "its image is in " + missing.home + ", which is not there"
	} else if err != nil {
		s.Daemon.Detail = "unknown"
	} else if image, err := vm.LoadImage(dir); err != nil {
		s.Daemon.Detail = "no runner image installed, so it cannot run jobs"
	} else {
		s.Image = &struct {
			Version       int    `json:"version"`
			Arch          string `json:"arch"`
			RunnerVersion string `json:"runnerVersion"`
		}{image.ImageVersion, image.Arch(), image.RunnerVersion}
		if vm.WorkDirInUse(workDir(dir)) {
			s.Daemon.Running, s.Daemon.Detail = true, "running"
		}
	}
	s.Connection = []tunnelState{}
	{
		// Only a running daemon's word counts.
		if remembered, err := connection.Load(); err == nil && remembered.Live() {
			for _, hive := range remembered.Hives {
				s.Connection = append(s.Connection, tunnelState{
					Hive: hive.Name, Connected: hive.Connected, Since: hive.Since,
					Reason: connection.Reason(hive.Error), Error: hive.Error,
				})
			}
		}
	}

	s.Jobs.Reason = current.Holding(power, time.Now())
	s.Jobs.Fix = current.Fix(power, time.Now())
	if state := coolingOff(cooldown.Load(), time.Now(), current.MaxJobs); state != nil {
		s.Jobs.Cooldown = state
		if state.Paused && s.Jobs.Reason == "" {
			s.Jobs.Reason = "cooling off until " + when(state.Until, time.Now()) + ", after the host killed a job"
			s.Jobs.Fix = "the machine ran short of memory; it takes jobs again by itself"
		}
	}
	// The owner's settings may allow a job that the machine cannot run.
	if s.Jobs.Reason == "" && s.Image != nil {
		s.Jobs.Reason, s.Jobs.Fix = cannotRunVMs()
	}
	s.Jobs.Taking = s.Jobs.Reason == ""
	s.Network = networkOf(current, vm.FencesNetwork)

	for _, vcpus := range settings.Sizes {
		reason := refusal(vcpus, facts, current)
		s.Labels = append(s.Labels, labelState{
			Label: labelFor(vcpus), Kind: "linux", VCPUs: vcpus,
			MemoryGB: int(vm.GuestMemoryBytes(vcpus, uint64(facts.MemoryBytes)) / gib),
			Takes:    reason == "", Reason: reason,
		})
	}
	if runtime.GOOS == "darwin" {
		s.Labels = append(s.Labels, macosState(current))
	}

	// What the daemon last learned, and a fresh look if that was a while
	// ago; never long enough to hold the rest of the page up.
	s.Update.Automatic = current.AutoUpdate
	if remembered, err := update.LoadState(); err == nil {
		if time.Since(remembered.CheckedAt) > update.Every {
			quick, cancel := context.WithTimeout(ctx, 3*time.Second)
			_, _, _ = update.Check(quick, &remembered)
			cancel()
			_ = update.SaveState(remembered)
		}
		s.Update.Latest = remembered.Latest
		s.Update.Available = update.Newer(version, remembered.Latest)
		if s.Update.Available && s.Update.Automatic {
			s.Update.Held = remembered.Held
		}
	}
	return s, facts, current, power, nil
}

// when is a time as someone reading status wants it: the clock today, and
// the day too for anything older.
func when(at, now time.Time) string {
	at = at.In(now.Location())
	if at.Year() == now.Year() && at.YearDay() == now.YearDay() {
		return at.Format("15:04")
	}
	return at.Format("Jan 2 15:04")
}

// tunnelRows are the lines saying whether the daemon is connected to each
// hive. One hive is one line; a hive it cannot reach says since when and why.
func tunnelRows(screen ui.Screen, tunnels []tunnelState, now time.Time) []string {
	var rows []string
	for i, tunnel := range tunnels {
		name := ""
		if i == 0 {
			name = "Tunnel"
		}
		said, colour := "connecting", screen.Dim
		switch {
		case tunnel.Connected:
			said = "connected since " + when(tunnel.Since, now)
		case tunnel.Reason != "":
			said, colour = "disconnected since "+when(tunnel.Since, now), screen.Honey
		}
		// An organization's name can be long; the state is what must fit.
		hive := tunnel.Hive
		if room := ui.Inner - 12 - 2 - 2 - ui.Width(said); ui.Width(hive) > room {
			hive = string([]rune(hive)[:room-1]) + "…"
		}
		rows = append(rows, field(name, screen.Dot(tunnel.Connected)+" "+hive+"  "+colour(said)))
		if !tunnel.Connected && tunnel.Reason != "" {
			for _, text := range wrap(tunnel.Reason+"; it keeps trying, and takes no jobs from this hive until then. See: hived doctor", ui.Inner-12) {
				rows = append(rows, field("", screen.Dim(text)))
			}
		}
	}
	return rows
}

func status(args []string) error {
	asJSON := false
	for _, arg := range args {
		switch arg {
		case "--json":
			asJSON = true
		default:
			return errors.New("usage: hived status [--json]")
		}
	}
	s, facts, current, power, err := gather()
	if err != nil {
		return err
	}
	if asJSON {
		out := json.NewEncoder(os.Stdout)
		out.SetIndent("", "  ")
		return out.Encode(s)
	}
	screen := ui.For(os.Stdout)

	pools := strings.Join(s.Hives, ", ")
	switch {
	case s.Hives == nil:
		pools = screen.Honey("could not ask the server") + screen.Dim(": "+s.HivesError)
	case len(s.Hives) == 0:
		pools = screen.Dim("none; add this machine to one with: hived link")
	}
	state := screen.Dot(true) + " taking jobs"
	if !s.Jobs.Taking {
		state = screen.Dot(false) + " not taking jobs: " + s.Jobs.Reason
	}

	fmt.Println()
	fmt.Print(indent(screen.Masthead(
		"",
		screen.Bold("Hive Runner"),
		"hived "+version,
		screen.Dim(s.Server),
	)))
	fmt.Println()
	rows := []string{
		field("Name", facts.Name+screen.Dim("  "+facts.Model)),
		field("Hardware", fmt.Sprintf("%d cores, %d GB", facts.Cores, facts.MemoryBytes/gib)),
		field("Hives", pools),
		field("Daemon", screen.Dot(s.Daemon.Running)+" "+s.Daemon.Detail),
	}
	rows = append(rows, tunnelRows(screen, s.Connection, time.Now())...)
	rows = append(rows, field("Jobs", state))
	if s.Jobs.Fix != "" {
		rows = append(rows, field("", screen.Dim(s.Jobs.Fix)))
	}
	if wait := s.Jobs.Cooldown; wait != nil && !wait.Paused {
		rows = append(rows, field("", screen.Honey(fmt.Sprintf("one job at a time until %s: the host killed a job", when(wait.Until, time.Now())))))
	}
	if runtime.GOOS == "darwin" {
		background, _, known := serviceSpawn()
		priority := priorityCheck(current, background, known, power)
		rows = append(rows, field("Priority", priority.Found))
		if priority.Fix != "" {
			rows = append(rows, field("", screen.Dim(priority.Fix)))
		}
	}
	rows = append(rows, networkRows(screen, s.Network)...)
	if problem, fix := asleep(current, power); problem != "" {
		for _, line := range wrap(problem, ui.Inner-12) {
			rows = append(rows, field("", screen.Honey(line)))
		}
		for _, line := range wrap(fix, ui.Inner-12) {
			rows = append(rows, field("", screen.Dim(line)))
		}
	}
	if s.Update.Available {
		// On two lines: together they are wider than the box.
		how := "update with: hived update"
		if s.Update.Automatic {
			how = "installs itself when idle, or now with: hived update"
		}
		rows = append(rows, field("Update", screen.Honey(s.Update.Latest+" is available")))
		if s.Update.Held != "" {
			for _, line := range wrap("it waits for you: "+s.Update.Held, ui.Inner-12) {
				rows = append(rows, field("", screen.Honey(line)))
			}
		} else {
			rows = append(rows, field("", screen.Dim(how)))
		}
	}
	fmt.Print(indent(screen.Box(strings.ToUpper(thisMachine[:1])+thisMachine[1:], rows...)))
	fmt.Println()
	fmt.Print(indent(screen.Box("What you lend", lending(screen, facts, current, s.Jobs.Taking, s.Labels)...)))
	fmt.Println()
	fmt.Print(indent(screen.Box("Actions cache", cacheRows(screen, current, false)...)))
	fmt.Println()
	fmt.Print(indent(screen.Box("Your settings", settingRows(screen, current)...)))
	fmt.Println(indent(screen.Dim("Change one with: hived config set max-jobs 2     Stop lending: hived pause")))
	return nil
}

// most is the fullest this machine gets: the jobs that use the most cores
// at once within the owner's limits, by size. Sizes double, so taking the
// largest that still fits each time is the best that can be done.
func most(facts host.Facts, current settings.Settings) []int {
	var jobsAtOnce []int
	free := facts.Cores - jobs.OwnerCores
	for len(jobsAtOnce) < current.MaxJobs {
		taken := 0
		for _, vcpus := range settings.Sizes {
			if vcpus <= free && refusal(vcpus, facts, current) == "" {
				taken = vcpus
			}
		}
		if taken == 0 {
			break
		}
		jobsAtOnce = append(jobsAtOnce, taken)
		free -= taken
	}
	return jobsAtOnce
}

// warmState says whether a warm VM is kept, for the startup log: "off" when
// HIVE_WARM=0 or the owner's setting is off, else "on" with its size.
func warmState(vcpus int, enabled bool) string {
	if vcpus == 0 || !enabled {
		return "off"
	}
	return fmt.Sprintf("on (%d vCPUs)", vcpus)
}

// warmVCPUs is the size of the VM the daemon keeps booted between jobs.
const warmVCPUs = 2

// lending draws how much of the machine its jobs use when it is as full as
// its owner allows, how much of that is in use before any job arrives, and
// which job sizes it takes at all. The warm VM is given to the first job
// that fits it, so what it holds is part of the fullest and not on top.
func lending(screen ui.Screen, facts host.Facts, current settings.Settings, taking bool, labels []labelState) []string {
	cores, memory := 0, 0
	fullest := most(facts, current)
	for _, vcpus := range fullest {
		cores += vcpus
		memory += int(vm.GuestMemoryBytes(vcpus, uint64(facts.MemoryBytes)) / gib)
	}
	atOnce := "One job at a time."
	if current.MaxJobs > 1 {
		atOnce = fmt.Sprintf("Up to %d jobs at a time.", current.MaxJobs)
	}
	heldCores, heldMemory := 0, 0
	idle := screen.Dim("Nothing is in use between jobs: warm is off.")
	switch {
	case current.Warm && !taking:
		idle = screen.Dim("Nothing is in use until it takes jobs again.")
	case current.Warm:
		heldCores, heldMemory = warmVCPUs, int(vm.GuestMemoryBytes(warmVCPUs, uint64(facts.MemoryBytes))/gib)
		idle = screen.Ember(ui.Held) + fmt.Sprintf(" In use between jobs too: %d cores, %d GB.", heldCores, heldMemory) + screen.Dim(" warm is on.")
	}
	const bar = 30
	rows := []string{
		field("Cores", screen.HeldBar(heldCores, cores, facts.Cores, bar)+fmt.Sprintf("  %d of %d", cores, facts.Cores)),
		field("Memory", screen.HeldBar(heldMemory, memory, int(facts.MemoryBytes/gib), bar)+fmt.Sprintf("  %d of %d GB", memory, facts.MemoryBytes/gib)),
		field("", screen.Dim("At its fullest. "+atOnce)),
		field("", idle),
	}
	for _, line := range budgetLines(facts, current) {
		rows = append(rows, field("", line))
	}
	if problem, _ := overcommit(facts, current, host.AvailableMemory()); problem != "" {
		for i, line := range wrap(problem, ui.Inner-14) {
			if i == 0 {
				line = screen.Ember(ui.Held) + " " + line
			}
			rows = append(rows, field("", line))
		}
	}
	rows = append(rows, "")
	for _, label := range labels {
		size := "on this Mac itself"
		if label.Kind == "linux" {
			size = fmt.Sprintf("%d vCPU  %2d GB", label.VCPUs, label.MemoryGB)
		}
		verdict := screen.Dot(true) + " takes these"
		if !label.Takes {
			verdict = screen.Dot(false) + " " + screen.Dim(label.Reason)
		}
		rows = append(rows, ui.Pad(screen.Honey(label.Label), 12)+ui.Pad(size, 20)+verdict)
	}
	return rows
}

// keyWidth fits the longest setting name.
const keyWidth = 18

func settingRows(screen ui.Screen, current settings.Settings) []string {
	limit := "auto"
	if current.MaxVCPUs != 0 {
		limit = strconv.Itoa(current.MaxVCPUs)
	}
	hours := "any"
	if current.Hours != "" {
		hours = current.Hours
	}
	reserve := cmp.Or(current.ReserveMemory, "off")
	cacheSize := strconv.Itoa(current.CacheGB) + " GB"
	if current.CacheGB == 0 {
		cacheSize = "auto"
		if dir, err := cacheDir(current); err == nil {
			cacheSize = "auto, " + bytesText(cacheLimit(current, dir))
		}
	}
	row := func(key, value, meaning string) string {
		return ui.Pad(key, keyWidth) + ui.Pad(screen.Honey(value), 14) + screen.Dim(meaning)
	}
	// A path has the row to itself: it is rarely short.
	place := func(key, dir, meaning string) string {
		if dir == "" {
			return row(key, "default", meaning)
		}
		if room := ui.Inner - keyWidth; ui.Width(dir) > room {
			dir = "…" + dir[len(dir)-room+1:]
		}
		return ui.Pad(key, keyWidth) + screen.Honey(dir)
	}
	// A list may be long: then it has the row to itself, as a path does.
	list := func(key, value, meaning string) string {
		if ui.Width(value) <= 13 {
			return row(key, value, meaning)
		}
		if room := ui.Inner - keyWidth; ui.Width(value) > room {
			value = value[:room-1] + "…"
		}
		return ui.Pad(key, keyWidth) + screen.Honey(value)
	}
	isolation := "what of the networks here jobs are kept off"
	if current.NetworkIsolation == "" {
		isolation = "the default, until you choose"
	}
	rows := []string{
		row("max-jobs", strconv.Itoa(current.MaxJobs), "how many jobs run at once"),
		row("max-vcpus", limit, "the largest job "+thisMachine+" takes"),
		row("warm", onOff(current.Warm), "keep a VM booted so jobs start sooner"),
		row("battery", onOff(current.Battery), "take jobs while unplugged"),
		row("lid", onOff(current.Lid), "take jobs with the lid closed"),
		row("hours", hours, "when jobs may start"),
		row("cache", onOff(current.Cache), "keep an Actions cache on "+thisMachine),
		row("cache-size", cacheSize, "the most disk the cache may use"),
		row("reserve-memory", reserve, "memory kept for everything else"),
		row("network-isolation", current.Isolation(), isolation),
		list("allow-network", allowNetworkText(current), "private places jobs may reach anyway"),
		row("dns", cmp.Or(current.DNS, settings.DNSHost), "the resolvers jobs are given"),
		row("ipv6-site", "/"+strconv.Itoa(cmp.Or(current.IPv6Site, fence.DefaultSite)), "the IPv6 site jobs are kept off"),
		row("macos-jobs", onOff(current.MacOSJobs), "take hive-macos jobs"),
		row("macos-isolation", cmp.Or(current.MacOSIsolation, settings.IsolationUser), "the "+native.User+" user, or a VM per job"),
		row("max-macos-vms", strconv.Itoa(current.MacOSVMs()), "how many macOS VMs run at once"),
		row("spotlight-exclude", onOff(current.SpotlightExclude), "keep Spotlight out of macOS jobs"),
		row("auto-update", onOff(current.AutoUpdate), "update to new releases when idle"),
		row("priority", priorityName(current), "how much of the CPU jobs get"),
		place("data-dir", current.DataDir, "where the image and job disks are kept"),
		place("cache-dir", current.CacheDir, "where the Actions cache is kept"),
		row("paused", yesNo(current.Paused), "hived pause, hived resume"),
	}
	pending := loadMoves()
	if pending.Data != nil || pending.Cache != nil {
		rows = append(rows, "", screen.Dim("A move is waiting for the daemon to have no job running."))
	}
	return rows
}

// networkRows say what a job here can reach, and whether this machine
// holds it to that.
func networkRows(screen ui.Screen, state networkState) []string {
	colour := screen.Dim
	if !state.Fenced {
		colour = screen.Honey
	}
	var rows []string
	for i, line := range wrap(state.reaches(), ui.Inner-12) {
		name := ""
		if i == 0 {
			name = "Network"
		}
		rows = append(rows, field(name, colour(line)))
	}
	return rows
}

func field(name, value string) string { return ui.Pad(name, 12) + value }

// wrap breaks text at spaces into lines no wider than width.
func wrap(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line != "" && ui.Width(line)+1+ui.Width(word) > width {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	return append(lines, line)
}

func indent(block string) string {
	lines := strings.Split(strings.TrimSuffix(block, "\n"), "\n")
	return "  " + strings.Join(lines, "\n  ") + "\n"
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func yesNo(yes bool) string {
	if yes {
		return "yes"
	}
	return "no"
}

const configUsage = `hived config                    Show this machine's settings
hived config set <name> <value>  Change one

  max-jobs   1 to 8              how many jobs run at once
  max-vcpus  2, 4, 8 or auto     the largest job this machine takes
  warm       on or off           keep one VM booted so jobs start sooner
  battery    on or off           take jobs while unplugged
  lid        on or off           take jobs with the lid closed; hived doctor
                                 says whether this machine stays awake closed
  hours      22:00-07:00 or any  when jobs may start, in local time
  cache      on or off           keep an Actions cache on this machine
  cache-size 1 to 2000 or auto   the most disk the cache may use, in GB
  reserve-memory 24G, 40% or off  memory kept for everything else; jobs get the rest
  network-isolation block, lan-allow or off
                                 block keeps jobs to the internet: off this
                                 machine's LAN, its tailnet and the machine
                                 itself; lan-allow lets them reach the LAN, and
                                 is the default until you choose; off is how it
                                 was before (Linux only so far: a Mac cannot
                                 keep jobs off yet)
  allow-network a list or none   private places jobs may reach anyway, like
                                 192.168.1.20:5432,10.0.0.0/24,[fd00::5]:80;
                                 not 0.0.0.0/0 or ::/0: that is
                                 network-isolation off
  dns        host or public      the resolvers jobs are given: this machine's,
                                 or Cloudflare's and Google's, which keeps a
                                 LAN resolver and its names out of reach
  ipv6-site  48, 56 or 64        the IPv6 prefix this machine's site has; jobs
                                 are kept off all of it: 56 for most homes, 48
                                 for an office, 64 if the /64 is its own
  macos-jobs on or off           take hive-macos jobs; hived macos setup first,
                                 or a macOS image for macos-isolation vm
  macos-isolation user or vm     run each as a separate user on this Mac (user),
                                 or in a throwaway macOS VM cloned from the image
                                 hived macos image makes (vm); experimental, and
                                 untested on real hardware so far
  max-macos-vms 1 or 2           how many macOS VMs run at once; two share one
                                 network, on which one job can come between the
                                 other and its gateway
  spotlight-exclude on or off    keep Spotlight from indexing what macOS jobs
                                 write, by keeping it in ~` + native.User + `/` + native.NoIndexDir + `;
                                 on by default
  auto-update on or off          update to new releases when no job is running;
                                 off, hived status and a notification say so
  priority   background or standard
                                 how a Mac runs the daemon and every job:
                                 background, the default, keeps them to the
                                 efficiency cores with the disk throttled, for
                                 a laptop lending idle time; standard lets them
                                 use every core, for a Mac kept for building.
                                 The daemon restarts to apply it once it has
                                 no job
  data-dir   a path or default   where the Linux image and each job's disk are
                                 kept; what is there is moved
  cache-dir  a path or default   where the Actions cache is kept; moved too

hived config get data-dir        Print where a directory setting points
`

func configure(args []string) error {
	current, err := settings.Load()
	if err != nil {
		return err
	}
	screen := ui.For(os.Stdout)
	switch {
	case len(args) == 0:
		fmt.Println()
		fmt.Print(indent(screen.Box("Your settings", settingRows(screen, current)...)))
		if path, err := settings.Path(); err == nil {
			fmt.Println(indent(screen.Dim("Kept in " + path)))
		}
		fmt.Println(indent(screen.Dim("Change one with: hived config set max-vcpus 4")))
		return nil
	case len(args) == 2 && args[0] == "get" && args[1] == "priority":
		// What the installer asks, so an update starts the daemon as its
		// owner chose.
		fmt.Println(priorityName(current))
		return nil
	case len(args) == 2 && args[0] == "get" && (args[1] == "data-dir" || args[1] == "cache-dir"):
		// What the installer asks, so an update puts the image where its
		// owner keeps it.
		home, err := chosenHome(current.DataDir, defaultDataHome)
		if args[1] == "cache-dir" {
			home, err = chosenHome(current.CacheDir, defaultCacheHome)
		}
		if err != nil {
			return err
		}
		fmt.Println(home)
		return nil
	case len(args) == 3 && args[0] == "set" && (args[1] == "data-dir" || args[1] == "cache-dir"):
		return configurePlace(current, args[1], args[2])
	case len(args) == 3 && args[0] == "set":
		if err := current.Set(args[1], args[2]); err != nil {
			return err
		}
		if args[1] == "macos-jobs" && current.MacOSJobs && current.MacOSInVM() {
			// On means jobs run in clones of the owner's image, which
			// has to be made and sealed first.
			if problem := macosVMProblem(current); problem != "" {
				return errors.New(problem)
			}
		} else if args[1] == "macos-jobs" && current.MacOSJobs {
			// On means jobs run here as another user: the Mac has to be
			// set up for that first, and the setup command is what says
			// what it will do.
			runner, err := native.New(nil)
			if err != nil {
				return err
			}
			if report := runner.Status(); report.Problem != "" {
				return errors.New(report.Problem)
			}
		}
		now := strings.ToLower(args[2])
		switch args[1] {
		case "reserve-memory":
			now = cmp.Or(current.ReserveMemory, "off")
		case "allow-network":
			now = allowNetworkText(current)
		case "ipv6-site":
			now = "/" + strconv.Itoa(cmp.Or(current.IPv6Site, fence.DefaultSite))
		}
		if err := saved(current, args[1]+" is now "+now); err != nil {
			return err
		}
		if wide := current.WideAllowances(); args[1] == "allow-network" && len(wide) > 0 {
			fmt.Println("\nBut " + strings.Join(wide, ", ") + " takes in nearly everything: jobs reach almost all of what " + thisMachine + " can, itself included.")
		}
		if args[1] == "network-isolation" || args[1] == "allow-network" || args[1] == "ipv6-site" || args[1] == "dns" {
			switch {
			case !vm.FencesNetwork && current.Isolation() != settings.IsolationOff:
				fmt.Println("\nBut " + thisMachine + " cannot yet keep jobs off its LAN and tailnet: its VMs are behind the system's NAT, which hived has no say over.")
			case vm.FencesNetwork:
				// A job already running keeps what it was given.
				fmt.Println("It applies from the next job.")
			}
		}
		if args[1] == "macos-isolation" && current.MacOSInVM() {
			if problem := macosVMProblem(current); problem != "" {
				fmt.Println("\nNo macOS job runs until that is put right: " + problem + "\nSee: hived macos image help")
			}
		}
		if args[1] == "priority" {
			fmt.Println(priorityApplies(daemonRunning(current)))
		}
		if args[1] == "spotlight-exclude" {
			fmt.Println("It applies from the next macOS job. The runner and the caches move when no job is running.")
		}
		if args[1] == "lid" {
			if problem, fix := asleep(current, host.ReadPower()); problem != "" {
				fmt.Println("\nBut " + strings.TrimPrefix(problem, "lid is on, but ") + ".\nKeep it awake with: " + fix)
			}
		}
		return nil
	case args[0] == "help" || args[0] == "-h" || args[0] == "--help":
		fmt.Print(configUsage)
		return nil
	}
	return errors.New("usage: hived config set <name> <value>; see hived config help")
}

// configurePlace changes where the image or the cache is kept, and moves
// what is there: at once when no daemon is running, and otherwise by the
// daemon, when it next has no job.
func configurePlace(current settings.Settings, key, value string) error {
	next := current
	if err := next.Set(key, value); err != nil {
		return err
	}
	if err := relocate(key, current, next); err != nil {
		return err
	}
	if err := settings.Save(next); err != nil {
		return err
	}
	moved, err := settle(next)
	for _, what := range moved {
		fmt.Println(strings.ToUpper(what[:1]) + what[1:] + ".")
	}
	pending := loadMoves()
	waiting := pending.Cache
	if key == "data-dir" {
		waiting = pending.Data
	}
	switch {
	case waiting == nil && len(moved) == 0:
		fmt.Println(key + " is unchanged.")
	case waiting == nil:
	case daemonRunning(next):
		fmt.Println("The daemon is running, so it moves what is there to " + waiting.To + " itself, as soon as it has no job.")
	default:
		return fmt.Errorf("%s is set, but nothing was moved yet: %w", key, err)
	}
	return nil
}

// daemonRunning reports whether a hived holds this machine's work directory.
func daemonRunning(current settings.Settings) bool {
	dir, err := imageDir(current)
	if err == nil && vm.WorkDirInUse(workDir(dir)) {
		return true
	}
	// A daemon holds the macOS VMs' directory once it has run one.
	dir, err = macosImageDir(current)
	return err == nil && vm.WorkDirInUse(workDir(dir))
}

func pause(paused bool) error {
	current, err := settings.Load()
	if err != nil {
		return err
	}
	current.Paused = paused
	// Either way the owner has spoken: the pause is no longer an update's
	// to lift.
	if mark, err := drainMark(); err == nil {
		_ = os.Remove(mark)
	}
	if paused {
		return saved(current, "Paused. A job already running finishes; no new ones start. Undo with: hived resume")
	}
	return saved(current, "Resumed.")
}

func saved(next settings.Settings, said string) error {
	if err := settings.Save(next); err != nil {
		return err
	}
	fmt.Println(said)
	fmt.Println("A running daemon picks this up within a few seconds.")
	return nil
}
