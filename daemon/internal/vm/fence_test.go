//go:build darwin || linux

package vm

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/fence"
)

func TestARunningVMTakesUpTheMachinesNewAddressesAndNothingElse(t *testing.T) {
	before, after := []netip.Addr{netip.MustParseAddr("203.0.113.7")}, []netip.Addr{netip.MustParseAddr("203.0.113.7"), netip.MustParseAddr("2001:db8::77")}
	addresses := before
	hostAddresses = func() []netip.Addr { return addresses }
	defer func() { hostAddresses = fence.HostAddresses }()

	owner := fence.Policy{Allow: []fence.Allowance{{Prefix: netip.MustParsePrefix("10.0.0.0/24")}}}
	runner := &Runner{Network: func() fence.Policy { return owner }}
	vm := &instance{ended: make(chan struct{}), resolvers: []netip.Addr{netip.MustParseAddr("192.168.1.1")}}
	runner.refence(vm)
	if got := vm.network.Load(); !slices.Equal(got.Host, before) || len(got.Allow) != 1 || len(got.Resolvers) != 1 {
		t.Fatalf("refence: %+v", got)
	}

	// The owner changes their mind mid-job, and the machine gains an
	// address. Only the address is taken up.
	owner = fence.Policy{Open: true}
	addresses = after
	done := make(chan struct{})
	go func() { vm.watchHost(time.Millisecond); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Equal(vm.network.Load().Host, after) {
		if time.Now().After(deadline) {
			t.Fatal("the new address was never taken up")
		}
		time.Sleep(time.Millisecond)
	}
	if got := vm.network.Load(); got.Open || len(got.Allow) != 1 || len(got.Resolvers) != 1 {
		t.Errorf("the rest of the policy changed mid-job: %+v", got)
	}
	close(vm.ended)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch outlived the VM")
	}
}
