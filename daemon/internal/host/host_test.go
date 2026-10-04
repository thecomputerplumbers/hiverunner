package host

import "testing"

func TestJobsAllowBatteryAndNeedAnOpenLid(t *testing.T) {
	cases := []struct {
		power Power
		want  bool
	}{
		{Power{}, true},
		{Power{OnBattery: true}, true},
		{Power{LidClosed: true}, false},
		{Power{OnBattery: true, LidClosed: true}, false},
	}
	for _, c := range cases {
		if got := c.power.Available(); got != c.want {
			t.Errorf("%+v: Available() = %v, want %v", c.power, got, c.want)
		}
	}
}
