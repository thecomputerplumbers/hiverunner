// Package vm runs one job in one throwaway virtual machine.
//
// The machine is a Linux guest under Apple's Virtualization.framework: its
// own kernel, its own memory, its own disk. The disk is a copy-on-write clone
// of the guest image, made for the job and deleted after it. The only things
// the host shares with the guest are two directories created for that job:
// one it can only read, holding the job's one-time runner configuration,
// and one it writes its results to.
package vm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/sharefile"
)

// Image is a built guest image: see daemon/image/build.sh.
type Image struct {
	Dir           string
	RunnerVersion string `json:"runnerVersion"`
	ImageVersion  int    `json:"imageVersion"`
	// Architecture is Go's name for what the image was built for: arm64 or
	// amd64. A manifest written before there was a choice says nothing, and
	// is arm64.
	Architecture string `json:"arch"`
	// Kernel is what direct kernel boot is given: the uncompressed Image on
	// arm64, a bzImage on amd64.
	Kernel      string `json:"kernel"`
	Initrd      string `json:"initrd"`
	Disk        string `json:"disk"`
	CommandLine string `json:"commandLine"`
}

// Arch is the architecture the image runs on.
func (i Image) Arch() string {
	if i.Architecture == "" {
		return "arm64"
	}
	return i.Architecture
}

// ErrNoImage means the guest image has not been built or installed.
var ErrNoImage = errors.New("no guest image is installed")

// ErrWrongArch means the installed guest image is for another architecture.
// A machine boots guests of its own architecture and no other.
var ErrWrongArch = errors.New("the guest image is for another architecture")

// hostArch is what this machine is. A variable so tests can be another.
var hostArch = func() string { return runtime.GOARCH }

// DefaultImageDir is where build.sh writes and hived looks.
func DefaultImageDir() (string, error) {
	if override := os.Getenv("HIVE_IMAGE"); override != "" {
		return override, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "HiveWorker", "image"), nil
}

// LoadImage reads an image's manifest and checks its files are present.
func LoadImage(dir string) (Image, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Image{}, ErrNoImage
	}
	if err != nil {
		return Image{}, err
	}
	image := Image{Dir: dir}
	if err := json.Unmarshal(raw, &image); err != nil {
		return Image{}, fmt.Errorf("guest image manifest: %w", err)
	}
	if arch := image.Arch(); arch != "arm64" && arch != "amd64" {
		return Image{}, fmt.Errorf("guest image manifest names an unknown architecture %q", arch)
	} else if host := hostArch(); arch != host {
		return Image{}, fmt.Errorf("%w: it is %s and this machine is %s", ErrWrongArch, arch, host)
	}
	for _, name := range []string{image.Kernel, image.Initrd, image.Disk} {
		// A manifest names files beside itself and nowhere else.
		if name == "" || filepath.Base(name) != name {
			return Image{}, fmt.Errorf("guest image manifest names an invalid file %q", name)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return Image{}, fmt.Errorf("guest image is incomplete: %w", err)
		}
	}
	return image, nil
}

func (i Image) path(name string) string { return filepath.Join(i.Dir, name) }

// ExitRunnerDeprecated is the runner's exit code when GitHub refuses its
// version as too old. It never takes a job in that state.
const ExitRunnerDeprecated = 7

// ReasonStaleImage is why a machine with an outdated guest declines jobs.
const ReasonStaleImage = "this machine's guest image has a runner GitHub no longer accepts, so jobs are declined for an hour; run hived update"

// SweepWorkDir removes what a previous daemon left behind in its work
// directory and reports how many job directories it found.
//
// A daemon that was killed, or a Mac that lost power, cannot clean up after
// itself: a job's disk stays on the machine, and so can the directory that
// held its runner configuration.
//
// Call it only while holding LockWorkDir, and before starting any VM. The
// lock, not the directory names, is what proves these are abandoned: without
// it this would delete a live daemon's running jobs.
func SweepWorkDir(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	var failed []error
	for _, entry := range entries {
		// Only what this package creates. Anything else is not ours to delete.
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "job-") {
			continue
		}
		// One that cannot be removed does not keep the rest.
		if err := sharefile.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			failed = append(failed, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(failed...)
}
