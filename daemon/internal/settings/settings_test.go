package settings

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thecomputerplumbers/hiverunner/daemon/internal/host"
)

func at(clock string) time.Time {
	parsed, _ := time.Parse("15:04", clock)
	return parsed
}

func TestHoursWindow(t *testing.T) {
	for _, test := range []struct {
		hours, now string
		want       bool
	}{
		{"", "03:00", true},
		{"09:00-17:00", "09:00", true},
		{"09:00-17:00", "17:00", false},
		{"22:00-07:00", "23:30", true},
		{"22:00-07:00", "06:59", true},
		{"22:00-07:00", "12:00", false},
	} {
		if got := (Settings{Hours: test.hours}).InHours(at(test.now)); got != test.want {
			t.Errorf("hours %q at %s: got %v", test.hours, test.now, got)
		}
	}
}

func TestSetRefusesWhatItCannotMean(t *testing.T) {
	current := Default()
	for _, bad := range [][2]string{
		{"max-vcpus", "3"},
		{"max-jobs", "0"},
		{"cache", "sometimes"},
		{"cache-size", "0"},
		{"cache-size", "lots"},
		{"max-jobs", "9"},
		{"warm", "maybe"},
		{"hours", "9-5"},
		{"hours", "09:00-09:00"},
		{"cores", "4"},
		{"reserve-memory", "24"},
		{"reserve-memory", "0G"},
		{"reserve-memory", "0%"},
		{"reserve-memory", "95%"},
		{"reserve-memory", "lots"},
		{"reserve-memory", "-4G"},
	} {
		if err := current.Set(bad[0], bad[1]); err == nil {
			t.Errorf("set %s %s was accepted", bad[0], bad[1])
		}
	}
	if current != Default() {
		t.Errorf("a refused value changed the settings: %+v", current)
	}
}

func TestSetAndHolding(t *testing.T) {
	current := Default()
	for _, change := range [][2]string{
		{"max-vcpus", "4"},
		{"max-jobs", "3"},
		{"cache", "off"},
		{"cache-size", "40GB"},
		{"battery", "off"},
		{"hours", "22:00-07:00"},
	} {
		if err := current.Set(change[0], change[1]); err != nil {
			t.Fatal(err)
		}
	}
	if current.Cache || current.CacheGB != 40 {
		t.Errorf("the cache settings were not set: %+v", current)
	}
	if err := current.Set("cache-size", "auto"); err != nil || current.CacheGB != 0 {
		t.Errorf("cache-size auto: %+v %v", current, err)
	}
	if current.MaxJobs != 3 {
		t.Errorf("max-jobs was not set: %d", current.MaxJobs)
	}
	if current.Takes(8) || !current.Takes(4) {
		t.Errorf("a limit of 4 takes the wrong sizes")
	}
	if got := current.Holding(host.Power{}, at("23:00")); got != "" {
		t.Errorf("plugged in, inside the window: holding for %q", got)
	}
	if got := current.Holding(host.Power{OnBattery: true}, at("23:00")); got != "on battery" {
		t.Errorf("on battery: got %q", got)
	}
	if got := current.Holding(host.Power{}, at("12:00")); got != "outside 22:00-07:00" {
		t.Errorf("outside the window: got %q", got)
	}
	current.Paused = true
	if got := current.Holding(host.Power{LidClosed: true}, at("23:00")); got != "paused by you" {
		t.Errorf("paused: got %q", got)
	}
}

func TestAClosedLidHoldsJobsUnlessTheOwnerAllowsIt(t *testing.T) {
	current, closed := Default(), host.Power{LidClosed: true, HasLid: true}
	if got := current.Holding(closed, at("12:00")); got != "lid closed" {
		t.Errorf("lid off, closed: got %q", got)
	}
	if got := current.Fix(closed, at("12:00")); got != "open the lid or run hived config set lid on" {
		t.Errorf("lid off, closed: told to %q", got)
	}
	if err := current.Set("lid", "on"); err != nil {
		t.Fatal(err)
	}
	if reason, fix := current.Holding(closed, at("12:00")), current.Fix(closed, at("12:00")); reason != "" || fix != "" {
		t.Errorf("lid on, closed: holding for %q, told to %q", reason, fix)
	}
	// The lid being allowed does not allow anything else.
	current.Battery = false
	closed.OnBattery = true
	if got := current.Holding(closed, at("12:00")); got != "on battery" {
		t.Errorf("lid on, closed, unplugged with battery off: got %q", got)
	}
}

func TestLiveFollowsTheFileAndKeepsTheLastGoodOne(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HIVE_CONFIG", filepath.Join(dir, "machine.json"))
	live, err := Watch()
	if err != nil {
		t.Fatal(err)
	}
	if live.Get() != Default() {
		t.Fatalf("no file should be the defaults")
	}

	next := Default()
	next.MaxVCPUs = 4
	if err := Save(next); err != nil {
		t.Fatal(err)
	}
	if got := live.Get().MaxVCPUs; got != 4 {
		t.Fatalf("a saved change was not seen: max %d", got)
	}

	var complaint error
	live.OnError = func(err error) { complaint = err }
	path, _ := Path()
	if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := live.Get().MaxVCPUs; got != 4 || complaint == nil {
		t.Fatalf("a broken file should be reported and ignored: max %d, error %v", got, complaint)
	}
}

func TestADirIsTakenAsTyped(t *testing.T) {
	current := Default()
	if err := current.Set("data-dir", "/Volumes/M482 2TB External/HiveWorker/"); err != nil || current.DataDir != "/Volumes/M482 2TB External/HiveWorker" {
		t.Errorf("data-dir: %q %v", current.DataDir, err)
	}
	if err := current.Set("cache-dir", "relative/path"); err == nil {
		t.Error("a relative cache-dir was accepted")
	}
	if err := current.Set("data-dir", "Default"); err != nil || current.DataDir != "" {
		t.Errorf("default: %q %v", current.DataDir, err)
	}
}

func TestReserveMemory(t *testing.T) {
	const gib = 1 << 30
	for _, test := range []struct {
		typed, stored string
		of60          uint64
	}{
		{"24G", "24G", 24 * gib},
		{" 24gb ", "24G", 24 * gib},
		{"40%", "40%", 24 * gib},
		{"off", "", 0},
	} {
		current := Default()
		current.ReserveMemory = "8G"
		if err := current.Set("reserve-memory", test.typed); err != nil {
			t.Fatalf("set reserve-memory %q: %v", test.typed, err)
		}
		if current.ReserveMemory != test.stored {
			t.Errorf("%q stored as %q, want %q", test.typed, current.ReserveMemory, test.stored)
		}
		if got := current.ReserveBytes(60 * gib); got != test.of60 {
			t.Errorf("%q of 60 GB = %d, want %d", test.typed, got, test.of60)
		}
	}
	// A hand-edited file is checked like the command line.
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"reserveMemory":"lots"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := read(path); err == nil {
		t.Error("a reserve that means nothing was read")
	}
	if err := os.WriteFile(path, []byte(`{"reserveMemory":"24G"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := read(path); err != nil || got.ReserveMemory != "24G" {
		t.Errorf("read = %+v, %v", got, err)
	}
}

func TestJobsAreKeptOffPrivateNetworksUnlessTheOwnerSays(t *testing.T) {
	current := Default()
	if policy := current.Network(); policy.Open || len(policy.Allow) != 0 {
		t.Fatalf("a machine whose owner decided nothing lends its network: %+v", policy)
	}
	if err := current.Set("allow-network", "192.168.1.20:5432, 10.0.0.9/24"); err != nil {
		t.Fatal(err)
	}
	if current.AllowNetwork != "192.168.1.20:5432,10.0.0.0/24" || len(current.Network().Allow) != 2 {
		t.Errorf("allow-network stored as %q", current.AllowNetwork)
	}
	if err := current.Set("private-network", "allow"); err != nil || !current.Network().Open {
		t.Errorf("private-network allow: %+v %v", current, err)
	}
	if err := current.Set("private-network", "block"); err != nil || current.Network().Open {
		t.Errorf("private-network block: %+v %v", current, err)
	}
	for _, bad := range [][2]string{{"private-network", "on"}, {"allow-network", "printer.local"}, {"allow-network", "192.168.1.20:0"}} {
		before := current
		if err := current.Set(bad[0], bad[1]); err == nil || current != before {
			t.Errorf("set %s %s was accepted", bad[0], bad[1])
		}
	}
	if err := current.Set("allow-network", "none"); err != nil || current.AllowNetwork != "" {
		t.Errorf("allow-network none: %q %v", current.AllowNetwork, err)
	}

	// A hand-edited file is checked like the command line.
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"allowNetwork":"the office"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := read(path); err == nil {
		t.Error("an allowance that means nothing was read")
	}
	if err := os.WriteFile(path, []byte(`{"privateNetwork":true,"allowNetwork":"10.0.0.0/24"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := read(path); err != nil || !got.PrivateNetwork || got.AllowNetwork != "10.0.0.0/24" {
		t.Errorf("read = %+v, %v", got, err)
	}
}

func TestTheOwnerSaysHowBigTheIPv6SiteIs(t *testing.T) {
	current := Default()
	if current.IPv6Site != 56 || current.Network().Site != 56 {
		t.Fatalf("the default site is /%d", current.IPv6Site)
	}
	for _, typed := range []string{"48", "/64", "56"} {
		if err := current.Set("ipv6-site", typed); err != nil {
			t.Errorf("ipv6-site %s: %v", typed, err)
		}
	}
	if current.IPv6Site != 56 {
		t.Errorf("ipv6-site 56 stored as %d", current.IPv6Site)
	}
	for _, bad := range []string{"60", "128", "0", "site"} {
		// "0" typed is refused; only a file may leave it out.
		if err := current.Set("ipv6-site", bad); err == nil {
			t.Errorf("ipv6-site %s was accepted", bad)
		}
	}
	// A file from before the setting, or written by hand without it, is
	// the default; one that says something else is refused.
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"maxJobs":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := read(path); err != nil || got.IPv6Site != 56 {
		t.Errorf("no ipv6Site: %d %v", got.IPv6Site, err)
	}
	if err := os.WriteFile(path, []byte(`{"ipv6Site":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := read(path); err != nil || got.IPv6Site != 56 {
		t.Errorf("ipv6Site 0: %d %v", got.IPv6Site, err)
	}
	if err := os.WriteFile(path, []byte(`{"ipv6Site":60}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := read(path); err == nil {
		t.Error("an ipv6Site of 60 was read")
	}
}
