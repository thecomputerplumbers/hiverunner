package macvm

import (
	"embed"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// guestFiles are what the owner installs in the image: the agent that runs
// each job at boot, its launch daemon, and the script that installs both.
//
//go:embed guest
var guestFiles embed.FS

// CreateOptions is what Create installs, and where.
type CreateOptions struct {
	// IPSW is a macOS restore image on this Mac. Nothing is downloaded.
	IPSW string
	// Dir is where the image is made. It must not be there yet.
	Dir string
	// DiskGB is the size the image's disk can grow to. It takes only what
	// is written to it.
	DiskGB int
	Say    io.Writer
}

// DefaultDiskGB is room for macOS, an Xcode and its simulator runtimes,
// and a job's build.
const DefaultDiskGB = 150

// OpenOptions is a session of the owner's with the image, in a window.
type OpenOptions struct {
	Dir string
	// Share, if set, is a folder of the owner's the VM can read, such as
	// one holding an Xcode .xip, under /Volumes/My Shared Files/files.
	Share string
	Say   io.Writer
}

// WriteGuestFiles puts the guest's files in dir, for the VM to read.
func WriteGuestFiles(dir string) error {
	return fs.WalkDir(guestFiles, "guest", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := guestFiles.ReadFile(path)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if filepath.Ext(path) != ".plist" {
			mode = 0o755
		}
		return os.WriteFile(filepath.Join(dir, filepath.Base(path)), raw, mode)
	})
}
