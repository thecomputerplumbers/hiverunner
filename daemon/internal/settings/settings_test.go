package settings

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/host"
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
