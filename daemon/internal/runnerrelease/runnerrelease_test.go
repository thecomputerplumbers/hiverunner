package runnerrelease

import "testing"

func TestVersionsAreComparedAsNumbers(t *testing.T) {
	for _, test := range []struct {
		a, b string
		want bool
	}{
		{"2.337.0", "2.338.0", true},
		{"2.337.0", "2.337.0", false},
		{"2.337.0", "2.99.0", false},
		{"2.337.0", "2.337.1", true},
		{"2.337.0", "nonsense", false},
		{"", "2.337.0", false},
	} {
		if got := Older(test.a, test.b); got != test.want {
			t.Errorf("Older(%q, %q) = %v", test.a, test.b, got)
		}
	}
}
