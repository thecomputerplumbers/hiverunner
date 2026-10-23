package native

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// visudo stands in for the real one: it notes the file it was asked to
// check, by name and inode, and refuses a rule that says "bad".
const visudo = `#!/bin/sh
[ "$1" = -cf ] || exit 2
ls -di "$2" >>"$CHECKED"
if grep -q bad "$2"; then exit 1; fi
`

// installRule runs installSudoers as setup does, but as this user.
func installRule(t *testing.T, rule, path string) (checked string, err error) {
	t.Helper()
	tools := t.TempDir()
	fake := filepath.Join(tools, "visudo")
	if err := os.WriteFile(fake, []byte(visudo), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(tools, "checked")
	cmd := exec.Command("/bin/sh", "-c", installSudoers, "hived", fake, path)
	cmd.Env = append(os.Environ(), "CHECKED="+log)
	cmd.Stdin = strings.NewReader(rule)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("%s", out)
	}
	raw, _ := os.ReadFile(log)
	return string(raw), err
}

func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	found, _ := filepath.Glob(filepath.Join(dir, ".hiveworker.*"))
	return found
}

// The rule visudo checks is the file sudo then reads: made in the rule's own
// directory, which only root can write, and moved into place whole.
func TestTheSudoRuleCheckedIsTheOneInstalled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hiveworker")
	rule := "ada ALL=(hiveworker) NOPASSWD: /Users/ada/.local/bin/hived macos-job\n"
	checked, err := installRule(t, rule, path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != rule {
		t.Fatalf("installed %q, %v", raw, err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o440 {
		t.Fatalf("installed as %v, %v", info.Mode(), err)
	}
	inode, name, _ := strings.Cut(strings.TrimSpace(checked), " ")
	if filepath.Dir(strings.TrimSpace(name)) != dir {
		t.Fatalf("checked %q, outside %s, where its owner's processes could change it after", name, dir)
	}
	if want := info.Sys().(*syscall.Stat_t).Ino; inode != strconv.FormatUint(want, 10) {
		t.Fatalf("checked inode %s, installed %d", inode, want)
	}
	if left := leftovers(t, dir); len(left) != 0 {
		t.Fatalf("left %v", left)
	}

	// A rule visudo refuses leaves the one there as it was, and nothing
	// beside it.
	if _, err := installRule(t, "bad rule\n", path); err == nil {
		t.Fatal("a refused rule was installed")
	}
	if raw, _ := os.ReadFile(path); string(raw) != rule {
		t.Fatalf("a refused rule replaced the one there: %q", raw)
	}
	if left := leftovers(t, dir); len(left) != 0 {
		t.Fatalf("a refused rule left %v", left)
	}
}
