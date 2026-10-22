// Package macvm runs each hive-macos job in a throwaway macOS VM, for a Mac
// whose owner sets `macos-isolation vm`. Nothing a job leaves behind, a
// process still running or a file in a home folder, is there for the next.
//
// The VM is a clone of an image the owner made once: macOS installed from
// an IPSW (Create), then set up by hand in a window (Open) with a user that
// logs in by itself, Xcode and the tools every job needs, and the guest
// agent, and then sealed (Seal), which takes its digest. A job gets an APFS
// clone of the image's disk and auxiliary storage, a machine identifier of
// its own, a directory it can only read holding GitHub's runner, the job's
// one-time runner configuration and the image's digest, and one it writes
// its results to. Its VM is stopped and its clone deleted when the job
// ends, however it ends.
//
// The digest names an image; it does not vouch for one. This Mac computes
// it, and its owner, or anything running as them, can change the image and
// seal it again. It says which image a job ran on, for an honest Mac.
//
// Apple's licence allows a Mac two macOS VMs besides itself, so a Mac runs
// two of these at most.
package macvm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/native"
)

// The files of an image, beside its manifest.
const (
	DiskFile          = "disk.img"
	AuxiliaryFile     = "aux.img"
	HardwareModelFile = "hardware-model"
	// IdentifierFile is the image's own machine identifier, for the owner's
	// sessions with it. A job's VM is given a new one.
	IdentifierFile = "machine-identifier"
	manifestFile   = "manifest.json"
)

// sealed are the files the digest covers, in the order it reads them: what
// makes one job's VM the same as another's.
var sealed = []string{HardwareModelFile, AuxiliaryFile, DiskFile}

// Image is a macOS image on this Mac, described by its manifest.
type Image struct {
	Dir string `json:"-"`
	// MacOS is what the IPSW installed, as "26.0.1 (25A362)".
	MacOS   string    `json:"macos"`
	Created time.Time `json:"created"`
	// Digest is "sha256:" and the hash Seal took of the sealed files, or ""
	// before it has been sealed. A job reads it as HIVE_IMAGE_DIGEST. The
	// runner beside the image is not part of it: it is checked against the
	// checksum hived pins, for every job.
	Digest string `json:"digest,omitempty"`
	// Stamps are the sealed files' sizes and modification times when the
	// digest was taken. A job compares them, which is cheap, rather than
	// hash a whole disk: that catches the owner opening the image again and
	// forgetting to seal it, not a change made to look like none. A copy of
	// the image to another disk changes them, and needs sealing again.
	Stamps map[string]Stamp `json:"stamps,omitempty"`
}

// Stamp is what a file looked like when its image was sealed.
type Stamp struct {
	Size     int64 `json:"size"`
	Modified int64 `json:"modified"`
}

// ErrNoImage means the owner has not made a macOS image.
var ErrNoImage = errors.New("there is no macOS image; make one with: hived macos image create --ipsw <file>")

// Load reads an image's manifest and checks its files are there.
func Load(dir string) (Image, error) {
	raw, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if errors.Is(err, os.ErrNotExist) {
		return Image{}, ErrNoImage
	}
	if err != nil {
		return Image{}, err
	}
	image := Image{Dir: dir}
	if err := json.Unmarshal(raw, &image); err != nil {
		return Image{}, fmt.Errorf("macOS image manifest: %w", err)
	}
	for _, name := range sealed {
		if _, err := os.Stat(image.path(name)); err != nil {
			return Image{}, fmt.Errorf("the macOS image is incomplete: %w", err)
		}
	}
	return image, nil
}

func (i Image) path(name string) string { return filepath.Join(i.Dir, name) }

// RunnerPath is GitHub's runner beside the image, as hived pins it.
func (i Image) RunnerPath() string { return i.path(native.RunnerFile) }

// Problem is why a job cannot be run from this image, in words the owner
// can act on, or "" when one can.
func (i Image) Problem() string {
	if i.Digest == "" {
		return "the macOS image is not sealed yet; finish it in hived macos image open, then run: hived macos image seal"
	}
	if !i.unchanged() {
		return "the macOS image has changed since it was sealed; run: hived macos image seal"
	}
	if _, err := os.Stat(i.RunnerPath()); err != nil {
		return "GitHub's runner " + native.RunnerVersion + " is not beside the macOS image; run: hived macos image seal"
	}
	return ""
}

// unchanged reports whether every sealed file is as it was when sealed.
func (i Image) unchanged() bool {
	for _, name := range sealed {
		stamp, ok := i.Stamps[name]
		if !ok {
			return false
		}
		now, err := stampOf(i.path(name))
		if err != nil || now != stamp {
			return false
		}
	}
	return true
}

func stampOf(path string) (Stamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Stamp{}, err
	}
	return Stamp{Size: info.Size(), Modified: info.ModTime().UnixNano()}, nil
}

// save writes the manifest in one step.
func (i Image) save() error {
	raw, err := json.MarshalIndent(i, "", "  ")
	if err != nil {
		return err
	}
	temporary := i.path(manifestFile + ".tmp")
	if err := os.WriteFile(temporary, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, i.path(manifestFile))
}

// Download fetches GitHub's runner into a directory and returns its path:
// native.DownloadRunner, which checks it against the pinned checksum.
type Download func(dir string) (string, error)

// Seal takes the image's digest, reading every byte of it each time, and
// puts GitHub's runner beside it unless the one there is the one hived
// pins. A job runs only from a sealed image, and one whose files have
// changed since is refused until it is sealed again. Nothing may have the
// image open meanwhile.
func Seal(dir string, download Download, say io.Writer) (Image, error) {
	release, err := lockImage(dir, true)
	if err != nil {
		return Image{}, err
	}
	defer release()
	image, err := Load(dir)
	if err != nil {
		return Image{}, err
	}
	fmt.Fprintln(say, "Taking the image's digest. This reads the whole disk once, and can take a few minutes.")
	stamps := map[string]Stamp{}
	for _, name := range sealed {
		if stamps[name], err = stampOf(image.path(name)); err != nil {
			return Image{}, err
		}
	}
	digest, err := digestOf(dir)
	if err != nil {
		return Image{}, err
	}
	// A file that changed while it was read is not what was hashed.
	for _, name := range sealed {
		if now, err := stampOf(image.path(name)); err != nil || now != stamps[name] {
			return Image{}, fmt.Errorf("%s changed while its digest was taken; is the image open?", name)
		}
	}
	image.Digest, image.Stamps = digest, stamps
	if err := image.save(); err != nil {
		return Image{}, err
	}
	if checkRunner(image.RunnerPath()) != nil {
		os.Remove(image.RunnerPath())
		fmt.Fprintf(say, "Downloading GitHub's runner %s...\n", native.RunnerVersion)
		path, err := download(dir)
		if err != nil {
			return Image{}, err
		}
		if path != image.RunnerPath() {
			return Image{}, fmt.Errorf("the runner was saved as %s, not %s", path, image.RunnerPath())
		}
	}
	return image, nil
}

// digestOf is sha256 over each sealed file's name, size and contents, in
// order. Holes in a sparse disk read as zeros, quickly.
func digestOf(dir string) (string, error) {
	sum := sha256.New()
	for _, name := range sealed {
		file, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return "", err
		}
		info, err := file.Stat()
		if err == nil {
			fmt.Fprintf(sum, "%s %d\n", name, info.Size())
			_, err = io.Copy(sum, file)
		}
		file.Close()
		if err != nil {
			return "", err
		}
	}
	return "sha256:" + hex.EncodeToString(sum.Sum(nil)), nil
}

// Remove deletes the image, once nothing has it open.
func Remove(dir string) error {
	release, err := lockImage(dir, true)
	if errors.Is(err, ErrNoImage) {
		return nil
	}
	if err != nil {
		return err
	}
	defer release()
	return os.RemoveAll(dir)
}
