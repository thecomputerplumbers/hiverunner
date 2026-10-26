//go:build darwin

package native

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A clean's list of what to stop leaves out the clean itself, and the clean
// signals by pid alone: kill(-1) on a Mac signals the caller too, which is
// how a clean once stopped itself and never finished (#132). Nothing here
// is signalled but -1 and 0, which are refused before any kill.
func TestDarwinProcessesLeavesOutTheClean(t *testing.T) {
	sleep := exec.Command("/bin/sleep", "30")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleep.Process.Kill(); _ = sleep.Wait() })
	system := darwinProcesses{uid: uint32(os.Getuid()), self: os.Getpid()}
	left, err := system.Left()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range left {
		if p.PID == os.Getpid() {
			t.Fatal("the clean is on its own list")
		}
		found = found || p.PID == sleep.Process.Pid
	}
	if !found {
		t.Fatalf("the child %d is not on the list", sleep.Process.Pid)
	}
	for _, pid := range []int{-1, 0} {
		if err := system.Signal(pid, syscall.Signal(0)); err == nil {
			t.Fatalf("signalled %d", pid)
		}
	}
}

// The rule names Child, whose path has spaces in it. Unescaped, sudo reads
// the command as "/Library/Application" with arguments; escaped, visudo
// takes it as one path.
func TestTheSudoRuleNamesChildWhole(t *testing.T) {
	rule := "someone ALL=(" + User + ") NOPASSWD: " + sudoersPath(Child) + " macos-job\n"
	if !strings.Contains(rule, `/Library/Application\ Support/Hive\ Runner/bin/hived macos-job`) {
		t.Fatalf("rule %q", rule)
	}
	path := filepath.Join(t.TempDir(), "hiveworker")
	if err := os.WriteFile(path, []byte(rule), 0o440); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/sbin/visudo", "-cf", path).CombinedOutput(); err != nil {
		t.Fatalf("visudo: %v %s", err, out)
	}
}

// Setup gives the owner ChildDir and Runners and leaves Base root's: the
// daemon writes in them without sudo, and no one else can.
func TestSetupGivesTheOwnerTheFoldersInBase(t *testing.T) {
	steps := giveFolders("501", "20")
	joined := make([]string, len(steps))
	for i, step := range steps {
		joined[i] = strings.Join(step, " ")
	}
	all := strings.Join(joined, "\n")
	for _, want := range []string{
		"/usr/sbin/chown root:wheel " + Base,
		"/usr/sbin/chown -R 501:20 " + ChildDir + " " + Runners,
		"/bin/chmod 0755 " + ChildDir + " " + Runners,
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("setup runs %q, not %q", all, want)
		}
	}
}

// What hiveworker runs is copied whole, and once.
func TestKeepChildCopiesHivedOnce(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "hived")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho one\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(dir, "bin", "hived")
	if err := os.Mkdir(filepath.Dir(child), 0o755); err != nil {
		t.Fatal(err)
	}
	said := 0
	say := func(string, ...any) { said++ }
	for range 2 {
		if err := keepChild(exe, child, say); err != nil {
			t.Fatal(err)
		}
	}
	if same, err := sameFile(exe, child); !same || err != nil || said != 1 {
		t.Fatalf("same %v, err %v, copied %d times", same, err, said)
	}
	if info, _ := os.Stat(child); info.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", info.Mode())
	}
	// A new hived, as an update leaves, replaces it.
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho two\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := keepChild(exe, child, say); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(child); string(got) != "#!/bin/sh\necho two\n" || said != 2 {
		t.Fatalf("child %q after %d copies", got, said)
	}
	if entries, _ := os.ReadDir(filepath.Dir(child)); len(entries) != 1 {
		t.Fatalf("left %v beside it", entries)
	}
}

// A runner the daemon unpacks is one a job may clone: the owner's,
// read-only to others, nothing setuid. It replaces what was there.
func TestUnpackRunner(t *testing.T) {
	src := t.TempDir()
	for name, mode := range map[string]os.FileMode{"run.sh": 0o775, "bin/Runner.Listener": 0o777 | os.ModeSetuid} {
		path := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	work := t.TempDir()
	tarball := filepath.Join(work, "runner.tar.gz")
	if out, err := exec.Command("/usr/bin/tar", "-czf", tarball, "-C", src, ".").CombinedOutput(); err != nil {
		t.Fatalf("tar: %v %s", err, out)
	}
	top := filepath.Join(work, "top")
	runners := filepath.Join(top, "runner")
	if err := os.MkdirAll(runners, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{top, runners} {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(runners, RunnerVersion)
	if err := os.MkdirAll(filepath.Join(dir, "stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unpackRunner(context.Background(), tarball, top, dir); err != nil {
		t.Fatal(err)
	}
	if err := checkRunner(top, dir, uint32(os.Getuid())); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stale")); err == nil {
		t.Fatal("what was there before is still there")
	}
	if entries, _ := os.ReadDir(runners); len(entries) != 1 {
		t.Fatalf("left %v beside it", entries)
	}
}
