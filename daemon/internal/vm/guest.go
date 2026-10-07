package vm

import "net/http"

// GuestServiceFile is the file in the share that tells the guest agent the
// host is offering its service to this job, and on which port.
const GuestServiceFile = "cache"

// GuestService is something the host offers the VMs it runs, each over a
// socket that exists only between the host and that one VM.
type GuestService struct {
	// Port is the socket port, the same in every VM.
	Port uint32
	// Handler returns what answers one VM. pool reports whose job that VM
	// is running, or "" before it has one.
	Handler func(pool func() string) http.Handler
	// Enabled says whether a job starting now should be offered it.
	Enabled func() bool
}
