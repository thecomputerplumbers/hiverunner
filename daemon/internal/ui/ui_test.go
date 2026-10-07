package ui

import (
	"strings"
	"testing"
)

func TestBoxRowsAreOneWidthWithOrWithoutColour(t *testing.T) {
	for _, screen := range []Screen{{}, {color: true}} {
		box := screen.Box("This Mac", "short", screen.Honey("coloured")+" and "+screen.Bar(3, 18, 18), "")
		for _, line := range strings.Split(strings.TrimSuffix(box, "\n"), "\n") {
			if got := Width(line); got != Inner+4 {
				t.Errorf("colour %v: line is %d columns, want %d: %q", screen.color, got, Inner+4, line)
			}
		}
	}
}

func TestBar(t *testing.T) {
	plain := Screen{}
	for _, test := range []struct {
		part, whole int
		want        string
	}{
		{0, 18, "░░░░░░"},
		{1, 180, "█░░░░░"},
		{9, 18, "███░░░"},
		{18, 18, "██████"},
		{4, 0, "░░░░░░"},
	} {
		if got := plain.Bar(test.part, test.whole, 6); got != test.want {
			t.Errorf("%d of %d: %s", test.part, test.whole, got)
		}
	}
}
