//go:build darwin

package host

import "testing"

func TestPmsetValue(t *testing.T) {
	settings := "System-wide power settings:\n SleepDisabled\t\t1\nCurrently in use:\n standby              1\n sleep                10 (sleep prevented by powerd)\n displaysleep         2\n"
	for name, want := range map[string]string{"SleepDisabled": "1", "sleep": "10", "displaysleep": "2", "autorestart": ""} {
		if got := pmsetValue(settings, name); got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
}
