package main

import (
	"strings"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/fence"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/host"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
)

// networkState is what a job on this machine can reach, as hived status
// --json gives it.
type networkState struct {
	// PrivateNetwork is the owner's setting: block or allow.
	PrivateNetwork string `json:"privateNetwork"`
	// Allow is what the owner lets jobs reach all the same.
	Allow []string `json:"allow"`
	// Fenced is whether this machine holds jobs to that. A Mac does not
	// yet, whatever the setting says.
	Fenced bool `json:"fenced"`
}

func networkOf(current settings.Settings, fences bool) networkState {
	state := networkState{PrivateNetwork: "block", Allow: []string{}, Fenced: fences && !current.PrivateNetwork}
	if current.PrivateNetwork {
		state.PrivateNetwork = "allow"
	}
	for _, allowance := range current.Network().Allow {
		state.Allow = append(state.Allow, allowance.String())
	}
	return state
}

// reaches is what a job reaches, in a few words.
func (n networkState) reaches() string {
	switch {
	case n.PrivateNetwork == "allow":
		return "the internet, and " + thisMachine + "'s LAN and tailnet"
	case !n.Fenced:
		return "the internet, and " + thisMachine + "'s LAN and tailnet: not yet kept off them here"
	case len(n.Allow) > 0:
		return "the internet, and " + strings.Join(n.Allow, ", ")
	}
	return "the internet only"
}

// check is what hived doctor says of it. Neither way it can be open is
// something wrong: the owner chose one, and a Mac cannot yet do otherwise.
func (n networkState) check() host.Check {
	switch {
	case n.PrivateNetwork == "allow":
		return host.Check{Name: "network", Found: "jobs reach " + n.reaches() + ", as private-network is allow", Note: true,
			Fix: "keep them to the internet with: hived config set private-network block"}
	case !n.Fenced:
		return host.Check{Name: "network", Found: "jobs reach the internet, and " + thisMachine + "'s LAN and tailnet: hived cannot yet keep them off those here", Note: true,
			Fix: "a job's VM is behind the system's NAT, which hived has no say over"}
	}
	return host.Check{Name: "network", Found: "jobs reach " + n.reaches(), OK: true}
}

// allowNetworkText is allow-network as hived config shows it.
func allowNetworkText(current settings.Settings) string {
	if current.AllowNetwork == "" {
		return "none"
	}
	return fence.FormatAllowances(current.Network().Allow)
}
