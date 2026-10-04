// Package vm runs one job in one throwaway virtual machine.
//
// The machine is a Linux guest under Apple's Virtualization.framework: its
// own kernel, its own memory, its own disk. The disk is a copy-on-write clone
// of the guest image, made for the job and deleted after it. The only thing
// the host shares with the guest is one directory created for that job,
// holding the job's one-time runner configuration.
package vm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Image is a built guest image: see daemon/image/build.sh.
type Image struct {
	Dir           string
	RunnerVersion string `json:"runnerVersion"`
	ImageVersion  int    `json:"imageVersion"`
	Kernel        string `json:"kernel"`
	Initrd        string `json:"initrd"`
	Disk          string `json:"disk"`
	CommandLine   string `json:"commandLine"`
}

// ErrNoImage means the guest image has not been built or installed.
var ErrNoImage = errors.New("no guest image is installed")

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
