//go:build darwin || linux

package vm

import (
	"errors"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/fence"
)

func TestARunningVMTakesUpTheMachinesNewAddressesAndNothingElse(t *testing.T) {
	before := []netip.Prefix{netip.MustParsePrefix("203.0.113.7/24")}
	after := []netip.Prefix{netip.MustParsePrefix("203.0.113.7/24"), netip.MustParsePrefix("2001:db8::77/64")}
	networks := before
	hostNetworks = func() ([]netip.Prefix, error) { return networks, nil }
	defer func() { hostNetworks = fence.HostNetworks }()

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
	networks = after
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

func TestTheMachinesNetworksAreKeptWhenTheyCannotBeRead(t *testing.T) {
	known := []netip.Prefix{netip.MustParsePrefix("2001:db8:1:1::5/64")}
	hostNetworks = func() ([]netip.Prefix, error) { return known, nil }
	defer func() { hostNetworks = fence.HostNetworks }()
	runner := &Runner{}
	vm := &instance{ended: make(chan struct{})}
	runner.refence(vm)

	hostNetworks = func() ([]netip.Prefix, error) { return nil, errors.New("netlink is busy") }
	vm.rehost()
	if got := vm.network.Load().Host; !slices.Equal(got, known) {
		t.Errorf("a failed read while running left %v", got)
	}
	runner.refence(vm)
	if got := vm.network.Load().Host; !slices.Equal(got, known) {
		t.Errorf("a failed read at a handoff left %v", got)
	}
	// A VM that has never read them is not started where it is fenced: it
	// would reach the machine's public addresses and their neighbours. The
	// read is tried again first.
	hostRetry = time.Millisecond
	defer func() { hostRetry = 200 * time.Millisecond }()
	tries := 0
	hostNetworks = func() ([]netip.Prefix, error) { tries++; return nil, errors.New("netlink is busy") }
	fresh := &instance{ended: make(chan struct{})}
	err := runner.refence(fresh)
	switch {
	case FencesNetwork && (err == nil || fresh.network.Load() != nil || tries != hostTries):
		t.Errorf("a first read that failed %d times: %v, %+v", tries, err, fresh.network.Load())
	case !FencesNetwork && err != nil:
		t.Errorf("unfenced, a first read that failed stopped the VM: %v", err)
	}
	// Under network-isolation off there is nothing of the machine's to keep
	// the guest off.
	open := &Runner{Network: func() fence.Policy { return fence.Policy{Open: true} }}
	if err := open.refence(&instance{ended: make(chan struct{})}); err != nil {
		t.Errorf("open, a first read that failed stopped the VM: %v", err)
	}
	// One that fails and then succeeds starts.
	tries = 0
	hostNetworks = func() ([]netip.Prefix, error) {
		if tries++; tries < 2 {
			return nil, errors.New("netlink is busy")
		}
		return known, nil
	}
	retried := &instance{ended: make(chan struct{})}
	if err := runner.refence(retried); err != nil || !slices.Equal(retried.network.Load().Host, known) {
		t.Errorf("a read that worked the second time: %v", err)
	}
}
