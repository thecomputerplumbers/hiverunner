package jobs

import (
	"fmt"
	"strconv"
	"strings"
)

// Why a runner or its VM stopped, when that was not a normal job finish.
// These are the words the daemon sends in job.exited and the coordinator
// stores; keep them stable.
const (
	ReasonGuestOOM       = "guest_oom"
	ReasonHostKilled     = "host_killed"
	ReasonDiskFull       = "disk_full"
	ReasonTimeout        = "timeout"
	ReasonDaemonShutdown = "daemon_shutdown"
	ReasonVMCrash        = "vm_crash"
	ReasonReleased       = "released"
)

// Result is what a runner returns: the process exit, and, when the job did
// not just finish, why the runner or VM was stopped.
type Result struct {
	ExitCode int
	Reason   string
	Detail   string
	// PeakGuestBytes is the most memory the guest's VM process held at
	// once, as the host saw it. Zero means it could not be read.
	PeakGuestBytes uint64
}

// Evidence is what Classify needs to name a stop. Fields are optional; an
// empty value means it was not observed.
type Evidence struct {
	Host           string
	Label          string
	MemoryBytes    uint64
	PeakUsedBytes  uint64
	SwapTotalBytes uint64
	SwapUsedBytes  uint64
	GuestOOM       bool
	DiskFull       bool
	HostKilled     bool
	// SiblingKilled is set when the host killed another job's VM while this
	// one was running. A job that then stops without a result of its own was
	// taken down by the same shortage.
	SiblingKilled bool
	// HostAvailableBytes is MemAvailable when the kill was seen; 0 if unread.
	HostAvailableBytes uint64
	VMFailed           bool
	ExitCode           int
	HasExit            bool
	Timeout            bool
	Released           bool
	Shutdown           bool
}

// End is a classified stop: a stable reason and one sentence a human can
// read in a job log.
type End struct {
	Reason string
	Detail string
}

// Classify names why a runner stopped. The more specific guest and host
// facts win over a later timeout or release, so an OOM is not reported as
// "the coordinator released the job" just because GitHub then said it failed.
func Classify(e Evidence) End {
	reason := ""
	// A sibling's kill explains a stop only when nothing more specific does:
	// a job that finished, timed out or was released stopped for that reason.
	sibling := e.SiblingKilled && !e.HostKilled && !e.GuestOOM && !e.DiskFull &&
		!e.Timeout && !e.Released && (e.Shutdown || e.VMFailed || !e.HasExit)
	switch {
	case e.GuestOOM:
		reason = ReasonGuestOOM
	case e.DiskFull:
		reason = ReasonDiskFull
	case e.HostKilled, sibling:
		reason = ReasonHostKilled
	case e.VMFailed:
		reason = ReasonVMCrash
	case e.Timeout:
		reason = ReasonTimeout
	case e.Shutdown:
		reason = ReasonDaemonShutdown
	case e.Released:
		reason = ReasonReleased
	case !e.HasExit && (e.Timeout || e.Shutdown || e.Released):
		// Unreachable: the flags above already match. Kept so a future
		// flag cannot fall through to "no reason" when the VM vanished.
	case !e.HasExit:
		reason = ReasonVMCrash
	case e.ExitCode == 137 && e.MemoryBytes > 0 && e.PeakUsedBytes*100 >= e.MemoryBytes*85:
		reason = ReasonGuestOOM
	}
	if reason == "" {
		return End{}
	}
	return End{Reason: reason, Detail: FormatDetail(e, reason)}
}

// FormatDetail is the one sentence a human should see: why, where, and the
// numbers that justify it.
func FormatDetail(e Evidence, reason string) string {
	where := e.Host
	if where == "" {
		where = "this machine"
	}
	if e.Label != "" {
		where += " (" + e.Label + ")"
	}
	mem := memoryPhrase(e)
	switch reason {
	case ReasonGuestOOM:
		if mem != "" {
			return "runner killed: guest OOM (" + mem + ") on " + where
		}
		return "runner killed: guest OOM on " + where
	case ReasonHostKilled:
		avail := ""
		if e.HostAvailableBytes > 0 {
			avail = formatBytes(e.HostAvailableBytes) + " available on host"
		}
		if !e.HostKilled {
			return "stopped: host ran out of memory (" + joinPhrases("sibling job killed", avail) + ") on " + where
		}
		return "runner killed: host ran out of memory (" + joinPhrases(mem, avail) + ") on " + where
	case ReasonDiskFull:
		return "runner stopped: disk full on " + where
	case ReasonTimeout:
		return "runner stopped: job timed out on " + where
	case ReasonDaemonShutdown:
		return "runner stopped: daemon shutting down on " + where
	case ReasonVMCrash:
		if e.ExitCode != 0 && e.ExitCode != -1 {
			return fmt.Sprintf("runner crashed (exit %d) on %s", e.ExitCode, where)
		}
		return "runner crashed on " + where
	case ReasonReleased:
		return "runner released on " + where
	default:
		return "runner stopped on " + where
	}
}

func joinPhrases(parts ...string) string {
	var kept []string
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, ", ")
}

// memoryPhrase is the guest's own use against the RAM it was given, never the
// host's: "guest used 5.5 of 7.8 GB" says nothing about the machine under it.
func memoryPhrase(e Evidence) string {
	if e.MemoryBytes == 0 {
		return ""
	}
	peak := e.PeakUsedBytes
	if peak == 0 {
		return "guest has " + formatBytes(e.MemoryBytes)
	}
	phrase := "guest used " + formatRatio(peak, e.MemoryBytes)
	if e.SwapTotalBytes > 0 {
		phrase += ", " + formatRatio(e.SwapUsedBytes, e.SwapTotalBytes) + " swap"
	}
	return phrase
}

func formatRatio(used, total uint64) string {
	const gib = 1 << 30
	if total >= gib {
		return trimGB(used) + " of " + trimGB(total) + " GB"
	}
	return formatBytes(used) + " of " + formatBytes(total)
}

func trimGB(n uint64) string {
	const gib = 1 << 30
	if n%gib == 0 {
		return fmt.Sprintf("%d", n/gib)
	}
	return fmt.Sprintf("%.1f", float64(n)/float64(gib))
}

func formatBytes(n uint64) string {
	const gib = 1 << 30
	const mib = 1 << 20
	if n >= gib {
		if n%gib == 0 {
			return fmt.Sprintf("%d GB", n/gib)
		}
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gib))
	}
	return fmt.Sprintf("%d MB", n/mib)
}

// ParseDiag reads the key=value file the guest writes where its results go.
func ParseDiag(text string) (used, total, swapUsed, swapTotal uint64, oom, diskFull bool) {
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "peak_used_kb":
			used = kb(value) << 10
		case "mem_total_kb":
			total = kb(value) << 10
		case "swap_used_kb":
			swapUsed = kb(value) << 10
		case "swap_total_kb":
			swapTotal = kb(value) << 10
		case "oom":
			oom = value == "1"
		case "disk_full":
			diskFull = value == "1"
		}
	}
	return
}

// ScanConsole looks only for kernel phrases. The guest console can hold a
// job's output, so callers must not log it; they only take these flags.
func ScanConsole(text string) (oom, diskFull, panicked bool) {
	lower := strings.ToLower(text)
	oom = strings.Contains(lower, "out of memory") ||
		strings.Contains(lower, "oom-kill") ||
		strings.Contains(lower, "invoked oom-killer") ||
		strings.Contains(lower, "killed process")
	diskFull = strings.Contains(lower, "no space left on device")
	panicked = strings.Contains(lower, "kernel panic")
	return
}

func kb(value string) uint64 {
	n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
