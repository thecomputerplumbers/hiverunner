package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/cache"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/settings"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/ui"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/vm"
)

// cacheLimit is the most the cache may hold, in bytes: what the owner chose,
// or a size taken from the disk's.
func cacheLimit(current settings.Settings, dir string) int64 {
	if current.CacheGB > 0 {
		return int64(current.CacheGB) << 30
	}
	return cache.AutoLimit(dir)
}

// openCache opens the machine's Actions cache with its owner's limit.
func openCache(current func() settings.Settings) (*cache.Store, error) {
	dir, err := cache.DefaultDir()
	if err != nil {
		return nil, err
	}
	store, err := cache.Open(dir)
	if err != nil {
		return nil, err
	}
	store.Limit = func() int64 { return cacheLimit(current(), dir) }
	return store, nil
}

// offerCache gives the runner's VMs the machine's Actions cache. Without
// it jobs still run: they use GitHub's cache, as they did before.
func offerCache(runner *vm.Runner, log *slog.Logger, current func() settings.Settings) *cache.Store {
	store, err := openCache(current)
	if err != nil {
		log.Warn("no Actions cache on this machine; jobs will use GitHub's", "error", err)
		return nil
	}
	runner.Guest = &vm.GuestService{
		Port:    cache.GuestPort,
		Handler: cache.New(store, log).Handler,
		Enabled: func() bool { return current().Cache },
	}
	return store
}

// keepCacheTrimmed applies a lowered limit without waiting for the next
// save, until ctx ends.
func keepCacheTrimmed(ctx context.Context, store *cache.Store) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		store.Trim()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func bytesText(bytes int64) string {
	const mib, gib = 1 << 20, 1 << 30
	switch {
	case bytes >= gib:
		return strconv.FormatFloat(float64(bytes)/gib, 'f', 1, 64) + " GB"
	case bytes >= mib:
		return strconv.FormatInt(bytes/mib, 10) + " MB"
	case bytes > 0:
		return "under 1 MB"
	}
	return "0 MB"
}

func plural(count int, one, many string) string {
	if count == 1 {
		return "1 " + one
	}
	return strconv.Itoa(count) + " " + many
}

// cacheRows draws how full the cache is and whose caches fill it.
func cacheRows(screen ui.Screen, current settings.Settings, detailed bool) []string {
	if !current.Cache {
		return []string{field("Cache", screen.Dot(false)+" off; jobs use GitHub's cache"),
			field("", screen.Dim("Turn it on with: hived config set cache on"))}
	}
	store, err := openCache(func() settings.Settings { return current })
	if err != nil {
		return []string{field("Cache", screen.Dot(false)+" unavailable: "+err.Error())}
	}
	repos, total := store.Usage()
	limit := store.Limit()
	entries := 0
	for _, repo := range repos {
		entries += repo.Entries
	}
	rows := []string{
		field("Disk", screen.Bar(int(total>>20), int(limit>>20), 30)+"  "+bytesText(total)+" of "+bytesText(limit)),
		field("", screen.Dim(plural(entries, "cache", "caches")+" from "+plural(len(repos), "repository", "repositories")+
			". Each repository has its own.")),
	}
	if !detailed || len(repos) == 0 {
		return rows
	}
	rows = append(rows, "")
	for _, repo := range repos {
		// GitHub's token names a repository by number and nothing else.
		name := "repository " + repo.Repo
		if repo.RepoName != "" {
			name = repo.RepoName
		}
		if ui.Width(name) > 32 {
			name = name[:31] + "…"
		}
		rows = append(rows, ui.Pad(screen.Honey(name), 34)+ui.Pad(bytesText(repo.Bytes), 10)+
			ui.Pad(plural(repo.Entries, "cache", "caches"), 11)+screen.Dim(ago(repo.Used)))
	}
	return rows
}

func ago(when time.Time) string {
	switch since := time.Since(when); {
	case when.IsZero():
		return ""
	case since < 2*time.Minute:
		return "used just now"
	case since < 2*time.Hour:
		return fmt.Sprintf("used %d min ago", int(since.Minutes()))
	case since < 48*time.Hour:
		return fmt.Sprintf("used %d h ago", int(since.Hours()))
	}
	return fmt.Sprintf("used %d days ago", int(time.Since(when).Hours()/24))
}

const cacheUsage = `hived cache                 Show what the Actions cache holds
hived cache clear           Delete every cache
hived cache clear <repo>    Delete one repository's, by the id shown

The cache is on by default. Change it with:
  hived config set cache off
  hived config set cache-size 40     gigabytes, or auto
`

func cacheCommand(args []string) error {
	current, err := settings.Load()
	if err != nil {
		return err
	}
	screen := ui.For(os.Stdout)
	switch {
	case len(args) == 0:
		fmt.Println()
		fmt.Print(indent(screen.Box("Actions cache", cacheRows(screen, current, true)...)))
		if dir, err := cache.DefaultDir(); err == nil {
			fmt.Println(indent(screen.Dim("Kept in " + dir)))
		}
		fmt.Println(indent(screen.Dim("Empty it with: hived cache clear     Resize it with: hived config set cache-size 40")))
		return nil
	case args[0] == "clear" && len(args) <= 2:
		store, err := openCache(func() settings.Settings { return current })
		if err != nil {
			return err
		}
		match := ""
		if len(args) == 2 {
			match = args[1]
		}
		freed, cleared := store.Clear(match)
		if cleared == 0 && match != "" {
			return fmt.Errorf("the cache holds nothing for %q; see hived cache", match)
		}
		fmt.Printf("Deleted %s, freeing %s.\n", plural(cleared, "cache", "caches"), bytesText(freed))
		return nil
	case args[0] == "help" || args[0] == "-h" || args[0] == "--help":
		fmt.Print(cacheUsage)
		return nil
	}
	return errors.New("usage: hived cache [clear [repository-id]]; see hived cache help")
}
