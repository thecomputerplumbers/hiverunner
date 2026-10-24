package main

import (
	"fmt"
	"sync"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/host"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/jobs"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/vm"
)

// hostMemory is this machine's total memory, read once: it does not change
// while the daemon runs.
var hostMemory = sync.OnceValue(func() uint64 {
	facts, err := host.ReadFacts()
	if err != nil || facts.MemoryBytes < 0 {
		return 0
	}
	return uint64(facts.MemoryBytes)
})

// guestMemory is the memory a job of this many vCPUs is given on this host.
func guestMemory(vcpus int) uint64 { return vm.GuestMemoryBytes(vcpus, hostMemory()) }

// leanestGuest is the least a job of this many vCPUs is given when the host
// has less free than its size asks.
func leanestGuest(vcpus int) uint64 { return vm.LeanestGuestMemoryBytes(vcpus, hostMemory()) }

// overcommit says so when the owner's settings would, at their fullest, ask
// for more memory than the host can give now: every job slot filled with the
// largest job it takes, and the warm VM on top. It is "" when they fit, and
// when the host cannot say what is available.
func overcommit(facts host.Facts, current settings.Settings, available uint64) (problem, fix string) {
	if available == 0 {
		return "", ""
	}
	largest := uint64(0)
	for _, vcpus := range most(facts, current) {
		largest = max(largest, vm.GuestMemoryBytes(vcpus, uint64(facts.MemoryBytes)))
	}
	if largest == 0 {
		return "", ""
	}
	wanted := uint64(current.MaxJobs) * largest
	if current.Warm {
		wanted += vm.GuestMemoryBytes(warmVCPUs, uint64(facts.MemoryBytes))
	}
	wanted += jobs.MemoryMargin
	if wanted <= available {
		return "", ""
	}
	const gib = 1 << 30
	each := fmt.Sprintf("%d GB each", largest/gib)
	if current.Warm {
		each += ", plus the warm VM,"
	}
	return fmt.Sprintf("max-jobs %d of %s needs %d GB with a margin, but only %d GB is available now; a full load would run out of memory and take the daemon with it",
		current.MaxJobs, each, (wanted+gib-1)/gib, available/gib), "lower it with hived config set max-jobs or max-vcpus, or turn warm off"
}

// plan is what the owner's reserve-memory leaves for jobs on this host.
type plan struct {
	Total, Reserve, Budget uint64
	// Largest is the guest memory of the largest job the settings allow,
	// and Jobs how many of those fit in the budget beside the warm VM when
	// it is on, up to max-jobs.
	Largest uint64
	Jobs    int
}

// memoryPlan works out the job budget: the host's memory less the reserve.
// With no reserve set the budget is the whole host, and callers leave
// concurrency to max-jobs alone.
func memoryPlan(facts host.Facts, current settings.Settings) plan {
	total := uint64(max(facts.MemoryBytes, 0))
	p := plan{Total: total, Reserve: current.ReserveBytes(total)}
	p.Budget = total - min(p.Reserve, total)
	for _, vcpus := range most(facts, current) {
		p.Largest = max(p.Largest, vm.GuestMemoryBytes(vcpus, total))
	}
	if p.Largest == 0 {
		return p
	}
	left := p.Budget
	if current.Warm {
		warm := vm.GuestMemoryBytes(warmVCPUs, total)
		left -= min(warm, left)
	}
	p.Jobs = min(current.MaxJobs, int(left/p.Largest))
	return p
}

// reserveProblem says so when the reserve leaves room for no job at all.
func reserveProblem(facts host.Facts, current settings.Settings) (problem, fix string) {
	if current.ReserveMemory == "" || facts.MemoryBytes <= 0 {
		return "", ""
	}
	p := memoryPlan(facts, current)
	if p.Jobs > 0 || p.Largest == 0 {
		return "", ""
	}
	const gib = 1 << 30
	return fmt.Sprintf("reserve-memory %s leaves %d GB of %d GB for jobs, which is not enough for one job of %d GB, so none will run",
		current.ReserveMemory, p.Budget/gib, p.Total/gib, p.Largest/gib), "lower it with hived config set reserve-memory, or turn warm off"
}

// budgetLines say how much memory jobs may use under the reserve, and how
// many run at once within it. They are empty when no reserve is set.
func budgetLines(facts host.Facts, current settings.Settings) []string {
	if current.ReserveMemory == "" || facts.MemoryBytes <= 0 {
		return nil
	}
	const gib = 1 << 30
	p := memoryPlan(facts, current)
	noun := "jobs"
	if p.Jobs == 1 {
		noun = "job"
	}
	return []string{
		fmt.Sprintf("Memory budget %d of %d GB (%d GB reserved)", p.Budget/gib, p.Total/gib, p.Reserve/gib),
		fmt.Sprintf("Effective: %d %s at once", p.Jobs, noun),
	}
}
