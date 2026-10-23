package main

import (
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/host"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/native"
	"github.com/thecomputerplumbers/hiverunner/daemon/internal/settings"
)

// spotlight is the spotlight check, for a Mac that runs macOS jobs as
// native.User: whether Spotlight indexes the volume the jobs write to, and
// whether they write where it skips. It is a note at worst, as FileVault is:
// indexing slows a job, and stops none.
func spotlight(current settings.Settings, system string, inVM bool) []host.Check {
	if system != "darwin" || !current.MacOSJobs || inVM {
		return nil
	}
	return spotlightChecks(native.Spotlight(current.SpotlightExclude))
}

func spotlightChecks(report native.SpotlightReport) []host.Check {
	volume := report.Volume
	if volume == "" {
		volume = "the volume " + native.Home + " is on"
	}
	privacy := "to keep the rest of " + native.Home + " out too, add it in System Settings > Spotlight > Search Privacy"
	switch {
	case report.Indexing == "off":
		return []host.Check{{Name: "spotlight", Found: "not indexing " + volume, OK: true}}
	case report.Indexing == "":
		return []host.Check{{Name: "spotlight", Found: "mdutil would not say whether Spotlight indexes " + volume, Note: true, Fix: "run mdutil -s " + volume}}
	case !report.NoIndex:
		return []host.Check{{Name: "spotlight", Found: "indexing " + volume + ", and with it every file a macOS job writes, which slows the job",
			Note: true, Fix: "hived config set spotlight-exclude on"}}
	case report.Moved != nil && !*report.Moved:
		return []host.Check{{Name: "spotlight", Found: "indexing " + volume + "; the job directories move into " + native.NoIndexDir + ", which it skips, when no job is running",
			Note: true, Fix: privacy}}
	}
	return []host.Check{{Name: "spotlight", Found: "indexing " + volume + ", but not the job directories: they are in " + native.NoIndexDir + ", which it skips",
		Note: true, Fix: privacy}}
}
