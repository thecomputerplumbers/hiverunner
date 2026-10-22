package main

import (
	"cmp"
	"strings"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/fence"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/host"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
)

// networkState is what a job on this machine can reach, as hived status
// --json gives it.
type networkState struct {
	// Isolation is the owner's network-isolation: block, lan-allow or off.
	Isolation string `json:"isolation"`
	// Chosen is whether the owner chose it, rather than it being the
	// default.
	Chosen bool `json:"chosen"`
	// Allow is what the owner lets jobs reach all the same.
	Allow []string `json:"allow"`
	// Wide is those of Allow that take in so much they all but open the
	// fence.
	Wide []string `json:"wide"`
	// DNS is the resolvers a job is given: host or public.
	DNS string `json:"dns"`
	// Fenced is whether this machine holds jobs to that. A Mac does not
	// yet, whatever the setting says.
	Fenced bool `json:"fenced"`
	// IPv6Site is the length of the IPv6 prefix jobs are kept off around
	// this machine's own addresses.
	IPv6Site int `json:"ipv6Site"`
}

func networkOf(current settings.Settings, fences bool) networkState {
	state := networkState{
		Isolation: current.Isolation(),
		Chosen:    current.NetworkIsolation != "",
		Allow:     []string{},
		Wide:      current.WideAllowances(),
		DNS:       cmp.Or(current.DNS, settings.DNSHost),
		Fenced:    fences && current.Isolation() != settings.IsolationOff,
		IPv6Site:  cmp.Or(current.IPv6Site, fence.DefaultSite),
	}
	for _, allowance := range current.Network().Allow {
		state.Allow = append(state.Allow, allowance.String())
	}
	return state
}

// reaches is what a job reaches, in a few words.
func (n networkState) reaches() string {
	var reached string
	switch {
	case n.Isolation == settings.IsolationOff:
		return "the internet, and " + thisMachine + "'s LAN, tailnet and " + thisMachine + " itself"
	case !n.Fenced:
		return "the internet, and " + thisMachine + "'s LAN, tailnet and " + thisMachine + " itself: not yet kept off them here"
	case n.Isolation == settings.IsolationLAN:
		reached = "the internet and " + thisMachine + "'s LAN, not its tailnet or " + thisMachine + " itself"
	default:
		reached = "the internet only"
	}
	switch {
	case len(n.Wide) > 0:
		return reached + ", but allow-network opens nearly everything (" + strings.Join(n.Wide, ", ") + ")"
	case len(n.Allow) > 0:
		return strings.TrimSuffix(reached, " only") + ", and " + strings.Join(n.Allow, ", ")
	}
	return reached
}

// fenced is what hived doctor's network row is told of the fence. Neither
// way it can be open is something wrong: the owner chose one, and a Mac
// cannot yet do otherwise.
func (n networkState) fenced() host.Fence {
	switch {
	case n.Isolation == settings.IsolationOff:
		return host.Fence{Why: "network-isolation is off", Fix: "keep jobs off them with: hived config set network-isolation block"}
	case !n.Fenced:
		// A Mac's VMs are behind the system's NAT, which hived has no say
		// over: the row says what #104 always did.
		return host.Fence{}
	case len(n.Wide) > 0:
		return host.Fence{Why: "allow-network has " + strings.Join(n.Wide, ", "), Fix: "narrow it with: hived config set allow-network <the places jobs need>"}
	}
	if n.Isolation == settings.IsolationLAN {
		return host.Fence{On: true, LAN: true, Allow: n.Allow, Why: "network-isolation is lan-allow", Fix: "keep them off the LAN too with: hived config set network-isolation block"}
	}
	return host.Fence{On: true, Allow: n.Allow}
}

// allowNetworkText is allow-network as hived config shows it.
func allowNetworkText(current settings.Settings) string {
	if current.AllowNetwork == "" {
		return "none"
	}
	return fence.FormatAllowances(current.Network().Allow)
}
