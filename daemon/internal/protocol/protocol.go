// Package protocol is the daemon's copy of the tunnel protocol defined in
// packages/protocol. The JSON fixtures there are the contract: this package's
// tests decode every one of them, so the two sides cannot drift silently.
package protocol

import (
	"encoding/json"
	"fmt"
	"runtime"
	"time"
)

// Version is bumped on any change an older peer could not safely ignore.
const Version = 3

// Keepalive frames. The coordinator answers Ping at the edge without waking.
const (
	Ping = "ping"
	Pong = "pong"
)

// Presence is what the daemon tells the coordinator about this machine.
type Presence string

const (
	Offline  Presence = "offline"
	Paused   Presence = "paused"
	Online   Presence = "online"
	Busy     Presence = "busy"
	Draining Presence = "draining"
)

// Close codes the daemon acts on.
const (
	// CloseReplaced: another connection for this machine took over.
	CloseReplaced = 4000
	// CloseRevoked: the machine token is no longer valid. Enroll again.
	CloseRevoked = 4001
	// CloseUnsupported: this daemon is too old for the coordinator.
	CloseUnsupported = 4002
)

// The architecture of a machine, and of the jobs it can run, in the words
// GitHub's runner uses: Go's amd64 is x64 here.
const (
	ArchARM64 = "arm64"
	ArchX64   = "x64"
)

// HostArch is this machine's architecture as the protocol spells it. A
// machine of any other kind runs no jobs, and says what Go calls it.
func HostArch() string {
	if runtime.GOARCH == "amd64" {
		return ArchX64
	}
	return runtime.GOARCH
}

// Hello is the first frame the daemon sends.
type Hello struct {
	Type          string   `json:"type"`
	Protocol      int      `json:"protocol"`
	DaemonVersion string   `json:"daemonVersion"`
	Presence      Presence `json:"presence"`
	// Arch is what this machine is, so the pool only offers it jobs whose
	// label names that architecture. A pool that hears none goes by what the
	// machine enrolled as.
	Arch string `json:"arch,omitempty"`
	// Holding is the jobs this machine is holding or running for this pool,
	// if any. A daemon that reconnects mid-job says so, so the pool counts
	// them again instead of offering cores that are already at work.
	Holding []string `json:"holding,omitempty"`
	// MaxVCPUs is the largest job the machine's owner lets it take. Absent
	// means as large as the machine allows.
	MaxVCPUs int `json:"maxVcpus,omitempty"`
	// MaxJobs is how many jobs the owner lets the machine run at once.
	// Absent means one.
	MaxJobs int `json:"maxJobs,omitempty"`
}

// PresenceUpdate reports a change of state.
type PresenceUpdate struct {
	Type     string   `json:"type"`
	Presence Presence `json:"presence"`
}

// JobAccepted, JobRejected and JobExited answer a JobAssign.
type JobAccepted struct {
	Type         string `json:"type"`
	AssignmentID string `json:"assignmentId"`
}

type JobRejected struct {
	Type         string `json:"type"`
	AssignmentID string `json:"assignmentId"`
	Reason       string `json:"reason"`
}

type JobExited struct {
	Type         string `json:"type"`
	AssignmentID string `json:"assignmentId"`
	ExitCode     int    `json:"exitCode"`
}

// Welcome is the coordinator's first frame.
type Welcome struct {
	MachineID string `json:"machineId"`
	Protocol  int    `json:"protocol"`
}

// StartTimeout is how long a machine holds its slot for an accepted offer
// before giving up on JobStart and freeing itself.
const StartTimeout = 30 * time.Second

// JobAssign offers this machine one job. It carries no credential: nothing
// is acquired from GitHub until the machine has accepted.
type JobAssign struct {
	AssignmentID string `json:"assignmentId"`
	// Label is the runs-on label, which is also the scale set.
	Label string `json:"label"`
	// Arch is the architecture the label names. Absent from a coordinator
	// that predates a second one, when every job was arm64.
	Arch           string `json:"arch,omitempty"`
	VCPUs          int    `json:"vcpus"`
	TimeoutMinutes int    `json:"timeoutMinutes"`
}

// JobStart follows an accepted offer with what the runner needs.
type JobStart struct {
	AssignmentID string `json:"assignmentId"`
	// JITConfig is GitHub's one-time runner configuration, passed to the
	// runner unchanged. It is a credential: never log it.
	JITConfig string `json:"jitConfig"`
	// RunnerName is the name the JIT config registers.
	RunnerName string `json:"runnerName"`
}

// JobRelease backs out of an offer: free the slot and destroy the VM.
type JobRelease struct {
	AssignmentID string `json:"assignmentId"`
}

// Error is the coordinator rejecting a frame without closing the tunnel.
type Error struct {
	Message string `json:"message"`
}

// Drain asks the daemon to finish its job and take no more.
type Drain struct{}

func NewHello(daemonVersion string, presence Presence, holding ...string) Hello {
	return Hello{Type: "hello", Protocol: Version, DaemonVersion: daemonVersion, Presence: presence, Holding: holding}
}

func NewPresence(presence Presence) PresenceUpdate {
	return PresenceUpdate{Type: "presence", Presence: presence}
}

func NewJobAccepted(id string) JobAccepted {
	return JobAccepted{Type: "job.accepted", AssignmentID: id}
}

func NewJobRejected(id, reason string) JobRejected {
	return JobRejected{Type: "job.rejected", AssignmentID: id, Reason: reason}
}

func NewJobExited(id string, exitCode int) JobExited {
	return JobExited{Type: "job.exited", AssignmentID: id, ExitCode: exitCode}
}

// DecodeCoordinator turns a frame from the coordinator into one of Welcome,
// JobAssign, JobStart, JobRelease, Drain or Error. Unknown types are an error rather than ignored:
// a frame this daemon does not understand may be one it must not skip.
func DecodeCoordinator(raw []byte) (any, error) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode frame: %w", err)
	}
	decode := func(target any) (any, error) {
		if err := json.Unmarshal(raw, target); err != nil {
			return nil, fmt.Errorf("decode %s: %w", envelope.Type, err)
		}
		return target, nil
	}
	switch envelope.Type {
	case "welcome":
		return decode(&Welcome{})
	case "job.assign":
		message, err := decode(&JobAssign{})
		if err != nil {
			return nil, err
		}
		if assign := message.(*JobAssign); assign.AssignmentID == "" || assign.VCPUs < 1 {
			return nil, fmt.Errorf("incomplete job.assign")
		}
		return message, nil
	case "job.start":
		message, err := decode(&JobStart{})
		if err != nil {
			return nil, err
		}
		if start := message.(*JobStart); start.AssignmentID == "" || start.JITConfig == "" || start.RunnerName == "" {
			return nil, fmt.Errorf("incomplete job.start")
		}
		return message, nil
	case "job.release":
		message, err := decode(&JobRelease{})
		if err != nil {
			return nil, err
		}
		if message.(*JobRelease).AssignmentID == "" {
			return nil, fmt.Errorf("incomplete job.release")
		}
		return message, nil
	case "drain":
		return &Drain{}, nil
	case "error":
		return decode(&Error{})
	default:
		return nil, fmt.Errorf("unknown frame type %q", envelope.Type)
	}
}
