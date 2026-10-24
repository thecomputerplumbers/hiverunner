package vm

import "testing"

func TestGuestMemoryBytes(t *testing.T) {
	if got := GuestMemoryBytes(2, 32*gib); got != 4*gib {
		t.Fatalf("2 vCPU on a large host = %d, want 4 GiB", got)
	}
	if got := GuestMemoryBytes(4, 32*gib); got != 8*gib {
		t.Fatalf("4 vCPU = %d, want 8 GiB", got)
	}
	if got := GuestMemoryBytes(8, 32*gib); got != 16*gib {
		t.Fatalf("8 vCPU = %d, want 16 GiB", got)
	}
	if got := GuestMemoryBytes(4, 12*gib); got != 6*gib {
		t.Fatalf("4 vCPU on a 12 GiB host is capped at half = %d, want 6 GiB", got)
	}
}

func TestGuestSwapHeadroomFollowsTheLabel(t *testing.T) {
	cases := []struct {
		vcpus int
		ram   uint64
		zram  uint64
		file  uint64
	}{
		{2, 4 * gib, 2 * gib, 2 * gib},
		{4, 8 * gib, 4 * gib, 4 * gib},
		{8, 16 * gib, 8 * gib, 4 * gib},
	}
	for _, c := range cases {
		ram := GuestMemoryBytes(c.vcpus, 64*gib)
		if ram != c.ram {
			t.Fatalf("%d vCPU RAM = %d, want %d", c.vcpus, ram, c.ram)
		}
		if got := GuestZramBytes(ram); got != c.zram {
			t.Fatalf("%d vCPU zram = %d, want %d", c.vcpus, got, c.zram)
		}
		if got := GuestSwapFileBytes(ram); got != c.file {
			t.Fatalf("%d vCPU swap file = %d, want %d", c.vcpus, got, c.file)
		}
	}
}

func TestLeanestGuestMemoryBytes(t *testing.T) {
	for _, c := range []struct {
		vcpus int
		host  uint64
		want  uint64
	}{
		{2, 64 * gib, 4 * gib},
		{4, 64 * gib, 6 * gib},
		{8, 64 * gib, 12 * gib},
		// Never more than the size itself, on a host that caps it.
		{4, 6 * gib, 3 * gib},
	} {
		if got := LeanestGuestMemoryBytes(c.vcpus, c.host); got != c.want {
			t.Errorf("LeanestGuestMemoryBytes(%d, %d GiB) = %d GiB, want %d", c.vcpus, c.host/gib, got/gib, c.want/gib)
		}
	}
}
