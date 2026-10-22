//go:build linux

package vm

import "testing"

func TestReadOnlySharesAreAskedOfVirtiofsdsHelp(t *testing.T) {
	for help, want := range map[string]bool{
		"      --readonly\n          Prevent the guest from making modifications to the filesystem\n": true,
		"      --readonly, --ro\n":                   true,
		"      --sandbox <SANDBOX>\n      --xattr\n": false,
		"      --readonly-mount <X>\n":               false,
	} {
		if got := offersReadOnly(help); got != want {
			t.Errorf("offersReadOnly(%q) = %v", help, got)
		}
	}
}
