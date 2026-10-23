package native

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// hiveworker is in staff, as every user on a Mac is. A home folder staff
// can read is open to every job.
func TestAHomeFolderOpenToTheJobsUserIsSaid(t *testing.T) {
	staff := []string{"20", "12", "61", "204"}
	for _, c := range []struct {
		mode   os.FileMode
		group  string
		groups []string
		want   string
	}{
		{0o750, "20", staff, "readable by its group, which hiveworker is in"},
		{0o710, "20", staff, "open to its group, which hiveworker is in"},
		{0o700, "20", staff, ""},
		{0o755, "80", staff, "readable by everyone"},
		{0o701, "80", staff, "open to everyone"},
		{0o750, "80", staff, ""},
		// Its group's bits apply to a member, whatever everyone's say.
		{0o705, "20", staff, ""},
	} {
		if got := opening(c.mode, c.group, c.groups); !strings.HasPrefix(got, c.want) || (c.want == "") != (got == "") {
			t.Errorf("%o, group %s: said %q, want %q", c.mode, c.group, got, c.want)
		}
	}

	// From the folder itself, with this user standing in for the jobs'
	// one: in the folder's group, as hiveworker is in staff.
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "ada")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := homeOpen(home, me); got != "" {
		t.Errorf("a home folder closed to others: %q", got)
	}
	if err := os.Chmod(home, 0o750); err != nil {
		t.Fatal(err)
	}
	if got := homeOpen(home, me); !strings.HasPrefix(got, "readable by its group") {
		t.Errorf("a home folder its group can read: %q", got)
	}
}
