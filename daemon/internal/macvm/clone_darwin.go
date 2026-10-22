//go:build darwin

package macvm

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// cloneFile makes an APFS copy-on-write clone: instant, and it shares every
// block with the image until the job writes. There is no copying instead: a
// copy of a macOS disk for every job would take minutes and its whole size.
func cloneFile(from, to string) error {
	if err := unix.Clonefile(from, to, 0); err != nil {
		return fmt.Errorf("%w (the macOS image and its jobs must be on one APFS volume)", err)
	}
	return nil
}
