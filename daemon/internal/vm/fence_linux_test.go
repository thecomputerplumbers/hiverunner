//go:build linux

package vm

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
	"time"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/fence"
)

// The sockets a VM's network is made of carry the guest's frames through
// the fence, as QEMU and passt would see them.
func TestTheGuestsNetworkGoesThroughTheFence(t *testing.T) {
	passtFile, fenceNetwork, err := socketPair()
	if err != nil {
		t.Fatal(err)
	}
	fenceGuest, qemuFile, err := socketPair()
	if err != nil {
		t.Fatal(err)
	}
	toNetwork, toGuest, err := conns(fenceNetwork, fenceGuest)
	if err != nil {
		t.Fatal(err)
	}
	// The test's own ends, as connections so that a read can time out.
	passtEnd, qemuEnd, err := conns(passtFile, qemuFile)
	if err != nil {
		t.Fatal(err)
	}
	defer passtEnd.Close()
	defer qemuEnd.Close()
	ended := make(chan error, 1)
	go func() { ended <- fence.Relay(toGuest, toNetwork, func() *fence.Policy { return &fence.Policy{} }) }()

	// ARP passes whatever the policy; it is the smallest frame there is.
	arp := make([]byte, 42)
	binary.BigEndian.PutUint16(arp[12:14], 0x0806)
	sent := binary.BigEndian.AppendUint32(nil, uint32(len(arp)))
	sent = append(sent, arp...)
	if _, err := qemuEnd.Write(sent); err != nil {
		t.Fatal(err)
	}
	_ = passtEnd.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(sent))
	for read := 0; read < len(got); {
		n, err := passtEnd.Read(got[read:])
		if err != nil {
			t.Fatalf("passt got % x: %v", got[:read], err)
		}
		read += n
	}
	if !bytes.Equal(got, sent) {
		t.Fatalf("passt got % x, not % x", got, sent)
	}

	// QEMU going hangs up on passt.
	qemuEnd.Close()
	select {
	case err := <-ended:
		if err != nil {
			t.Errorf("a VM that stopped is not an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the fence outlived the VM")
	}
	_ = passtEnd.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := passtEnd.Read(make([]byte, 1)); err == nil || os.IsTimeout(err) {
		t.Errorf("passt was not hung up on: %v", err)
	}
}
