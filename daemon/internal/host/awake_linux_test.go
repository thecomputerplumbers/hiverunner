//go:build linux

package host

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// standIn puts an executable script called name first on PATH.
func standIn(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func onPath(t *testing.T) string {
	dir := t.TempDir()
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return dir
}

// The real inhibitor, with logind stood in for by a systemd-inhibit that
// grants every lock: what it runs under the lock says it holds, and the
// last release ends all of it.
func TestTheLinuxInhibitorSaysItHoldsAndEndsWithTheLastRelease(t *testing.T) {
	dir := onPath(t)
	standIn(t, dir, "systemd-inhibit", `while [ "${1#--}" != "$1" ]; do shift; done; exec "$@"`)
	log := &said{}
	a := NewAwake(slog.New(slog.NewTextHandler(log, nil)))
	a.Hold()
	eventually(t, "never said it kept the machine awake", func() bool { return log.lines(keeping) == 1 })
	a.mu.Lock()
	group := a.cmd.Process.Pid
	a.mu.Unlock()
	a.Release()
	eventually(t, "what ran under the lock outlived the last release", func() bool {
		return errors.Is(syscall.Kill(-group, 0), syscall.ESRCH)
	})
	if log.lines("may sleep again") != 1 {
		t.Fatal("the last release did not say the machine may sleep again")
	}
}

func TestPolkitRefusingIsToldFromLogindBeingAway(t *testing.T) {
	for said, want := range map[string]bool{
		"Failed to inhibit: Access denied as the requested operation requires interactive authentication. However, interactive authentication has not been enabled by the calling program.": true,
		"Failed to inhibit: Access denied":                                                          true,
		"Failed to connect to bus: No such file or directory":                                       false,
		"Failed to inhibit: The name org.freedesktop.login1 was not provided by any .service files": false,
	} {
		if refused(said) != want {
			t.Errorf("refused(%q) = %v, want %v", said, !want, want)
		}
	}
}

// What the fix's command writes is the rule, to the byte.
func TestTheFixWritesTheRule(t *testing.T) {
	path := awakeRulePath("someone")
	if path != "/etc/polkit-1/rules.d/49-hiverunner-someone.rules" {
		t.Fatalf("rule path is %s", path)
	}
	fix := awakeFix("someone")
	out := filepath.Join(t.TempDir(), "rule")
	local := strings.Replace(fix, "sudo tee "+path, "tee "+out, 1)
	local = strings.Replace(local, "sudo chmod 0644 "+path, "chmod 0644 "+out, 1)
	if local == fix || strings.Contains(local, "sudo") {
		t.Fatalf("the fix is not shaped as expected: %s", fix)
	}
	if err := exec.Command("sh", "-c", local).Run(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != awakeRule("someone") {
		t.Fatalf("the fix writes\n%s\nwant\n%s", got, awakeRule("someone"))
	}
	if info, _ := os.Stat(out); info.Mode().Perm() != 0o644 {
		t.Fatalf("the rule is %v, and polkitd must read it", info.Mode().Perm())
	}
	if !strings.Contains(awakeRule("someone"), `action.id == "org.freedesktop.login1.inhibit-block-sleep" && subject.user == "someone"`) {
		t.Fatal("the rule allows something other than this user's lock against sleep")
	}
	for _, name := range []string{"", "Some One", "a'b", "x;rm", `a"b`, "-a"} {
		if awakeFix(name) != "" {
			t.Errorf("a fix for the user %q", name)
		}
	}
}

// install.sh writes and prints the same rule as hived does.
func TestInstallShWritesTheSameRule(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "release", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	var functions []string
	for _, name := range []string{"keep_awake_rule", "keep_awake_cmd"} {
		body := regexp.MustCompile(`(?ms)^` + name + `\(\) \{\n.*?^\}\n`).Find(raw)
		if body == nil {
			t.Fatalf("install.sh has no %s", name)
		}
		functions = append(functions, string(body))
	}
	run := func(call string) string {
		out, err := exec.Command("sh", "-c", strings.Join(functions, "")+call).Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if got := run("keep_awake_rule someone"); got != awakeRule("someone") {
		t.Fatalf("install.sh writes\n%s\nhived writes\n%s", got, awakeRule("someone"))
	}
	if got := run("keep_awake_cmd someone"); got != awakeFix("someone") {
		t.Fatalf("install.sh prints\n%s\nhived prints\n%s", got, awakeFix("someone"))
	}
	if !strings.Contains(string(raw), `sudo -n tee "$polkit_rule"`) {
		t.Fatal("install.sh does not write the rule with a sudo that never asks for a password")
	}
}

// A refused lock is put right with sudo when sudo needs no password, and
// said with the fix when it does.
func TestARefusedLockIsAllowedOnlyWithoutAPassword(t *testing.T) {
	retry := awakeRetry
	awakeRetry = time.Millisecond
	t.Cleanup(func() { awakeRetry = retry })
	dir := onPath(t)
	rule := filepath.Join(dir, "rule")
	standIn(t, dir, "systemd-inhibit", `[ -e "`+rule+`" ] && exit 0
echo "Failed to inhibit: Access denied as the requested operation requires interactive authentication." >&2
exit 1`)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	// sudo asks for a password, so it is not used, and the fix is said.
	standIn(t, dir, "sudo", `echo "sudo $*" >> "`+dir+`/asked"; echo "sudo: a password is required" >&2; exit 1`)
	log := &said{}
	if allowAwake(slog.New(slog.NewTextHandler(log, nil))) {
		t.Fatal("allowed with sudo refusing")
	}
	asked, _ := os.ReadFile(filepath.Join(dir, "asked"))
	for _, line := range strings.Split(strings.TrimSpace(string(asked)), "\n") {
		if !strings.HasPrefix(line, "sudo -n ") {
			t.Fatalf("sudo was asked without -n: %q", line)
		}
	}
	if log.lines("polkit refuses hived a lock against sleep") != 1 || log.lines("sudo tee /etc/polkit-1/rules.d/49-hiverunner-") != 1 {
		t.Fatalf("the refusal was not said with its fix:\n%s", log.buf.String())
	}

	// sudo needs none: the rule is written as the fix would write it.
	standIn(t, dir, "sudo", `[ "$1" = -n ] || exit 1
shift
case "$1" in
  tee) cat > "`+rule+`" ;;
  chmod) ;;
  *) exit 1 ;;
esac`)
	log = &said{}
	if !allowAwake(slog.New(slog.NewTextHandler(log, nil))) {
		t.Fatalf("not allowed with sudo needing no password:\n%s", log.buf.String())
	}
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(rule); string(got) != awakeRule(me.Username) {
		t.Fatalf("wrote\n%s\nwant\n%s", got, awakeRule(me.Username))
	}
	if log.lines("allowed hived to keep this machine awake") != 1 {
		t.Fatal("did not say it allowed it")
	}
	// Allowed already: nothing is said, and nothing is written.
	standIn(t, dir, "sudo", `exit 1`)
	if !allowAwake(quiet) {
		t.Fatal("an allowed lock was not")
	}
}
