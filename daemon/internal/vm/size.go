package vm

// GuestMemoryBytes is the memory a job's VM is given: 2 GiB a core, at least
// 4, which is what a hosted runner of the same size has or a little less,
// and never more than half the host. A host of unknown size is not capped.
func GuestMemoryBytes(vcpus int, hostBytes uint64) uint64 {
	const gib = 1 << 30
	memory := uint64(max(vcpus*2, 4)) * gib
	if hostBytes > 0 && memory > hostBytes/2 {
		memory = hostBytes / 2 / gib * gib
	}
	return memory
}
