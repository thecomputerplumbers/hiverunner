//go:build linux

package host

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// awakeSignals: the lock is logind's to grant, so the inhibitor says when
// it holds one.
const awakeSignals = true

// awakeCommand takes a logind inhibitor lock against sleep and idle for as
// long as it runs. What it runs under the lock says so on its stdout, then
// waits for hived, so the lock ends with hived even if nothing stops it
// (Pdeathsig covers the inhibitor itself). A machine without systemd has no
// lock to take, and is left as it is.
func awakeCommand() *exec.Cmd {
	inhibit, err := exec.LookPath("systemd-inhibit")
	if err != nil {
		return nil
	}
	cmd := exec.Command(inhibit, "--what=sleep:idle", "--who=hived", "--why=running a GitHub Actions job", "--mode=block",
		"sh", "-c", "echo holding && exec tail --pid="+strconv.Itoa(os.Getpid())+" -f /dev/null >/dev/null 2>&1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	return cmd
}

// stopAwake ends the inhibitor and what it runs, which share a group.
func stopAwake(cmd *exec.Cmd) {
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}

// awakeRuleDir is where polkit reads the rules an administrator adds.
const awakeRuleDir = "/etc/polkit-1/rules.d"

// awakeRulePath is the polkit rule that lets name's hived hold off sleep.
func awakeRulePath(name string) string { return awakeRuleDir + "/49-hiverunner-" + name + ".rules" }

// A user name a rule can carry as it is, in JavaScript and in a shell.
var plainName = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

// awakeRuleLines lets name, and nobody else, take logind's lock against sleep.
// hived is a user service, with no login session, and polkit lets a
// process with no active session hold off sleep only with an
// administrator's password; holding off idle needs no rule. Each line has
// no ' or \ or %, so a shell can print it as it is (see awakeFix).
func awakeRuleLines(name string) []string {
	return []string{
		"// Hive Runner: lets hived, running as " + name + ", keep this machine from",
		"// sleeping while it runs a job, and nothing else.",
		"polkit.addRule(function(action, subject) {",
		`    if (action.id == "org.freedesktop.login1.inhibit-block-sleep" && subject.user == "` + name + `") {`,
		"        return polkit.Result.YES;",
		"    }",
		"});",
	}
}

// awakeRule is the rule's file, as install.sh writes it too.
func awakeRule(name string) string { return strings.Join(awakeRuleLines(name), "\n") + "\n" }

// awakeFix is the command that installs name's rule, or "" for a name a
// rule cannot carry.
func awakeFix(name string) string {
	if !plainName.MatchString(name) {
		return ""
	}
	quoted := make([]string, 0, 8)
	for _, line := range awakeRuleLines(name) {
		quoted = append(quoted, "'"+line+"'")
	}
	path := awakeRulePath(name)
	return "printf '%s\\n' " + strings.Join(quoted, " ") + " | sudo tee " + path + " >/dev/null && sudo chmod 0644 " + path
}

// refused is whether what systemd-inhibit said is polkit refusing the
// lock, which a rule fixes, rather than logind being absent or broken.
func refused(said string) bool {
	return strings.Contains(said, "Access denied") || strings.Contains(said, "interactive authentication")
}

// awakeProbe takes the lock against sleep and lets it go at once, in the
// context given: what systemd-inhibit said, and whether it took it. ran is
// false when it could not be asked at all.
func awakeProbe(prefix ...string) (said string, took, ran bool) {
	args := append(prefix, "systemd-inhibit", "--what=sleep", "--who=hived", "--why=checking it may keep this machine awake", "--mode=block", "true")
	cmd := exec.Command(args[0], args[1:]...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if _, exited := err.(*exec.ExitError); err != nil && !exited {
		return err.Error(), false, false
	}
	return strings.TrimSpace(out.String()), err == nil, true
}

// awakeRetry is how long to wait between asking again once the rule is in.
var awakeRetry = 200 * time.Millisecond

// allowAwake makes sure logind lets this process hold off sleep. If polkit
// refuses, it installs the rule that allows it, only if sudo needs no
// password to; if it cannot, it says what to run. Whether the lock will be
// let hold.
func allowAwake(log *slog.Logger) bool {
	if _, err := exec.LookPath("systemd-inhibit"); err != nil {
		return true
	}
	said, took, ran := awakeProbe()
	if took || !ran {
		return true
	}
	if !refused(said) {
		log.Warn("this machine may sleep under a job: logind will not take a lock against sleep", "said", said)
		return false
	}
	me, err := user.Current()
	if err != nil || awakeFix(me.Username) == "" {
		log.Warn("this machine may sleep under a job: polkit refuses hived a lock against sleep", "said", said)
		return false
	}
	fix, path := awakeFix(me.Username), awakeRulePath(me.Username)
	if writeAwakeRule(me.Username) == nil {
		// polkit notices a new rule within a moment.
		for range 10 {
			if _, took, _ := awakeProbe(); took {
				log.Info("allowed hived to keep this machine awake while it runs jobs", "rule", path)
				return true
			}
			time.Sleep(awakeRetry)
		}
		log.Warn("this machine may sleep under a job: polkit still refuses hived a lock against sleep with its rule installed; this polkit may be too old to read it", "rule", path)
		return false
	}
	log.Warn("this machine may sleep under a job: polkit refuses hived a lock against sleep unless its owner allows it", "said", said, "fix", fix)
	return false
}

// writeAwakeRule installs name's rule with sudo, if sudo asks for no
// password: hived never asks for one.
func writeAwakeRule(name string) error {
	path := awakeRulePath(name)
	write := exec.Command("sudo", "-n", "tee", path)
	write.Stdin = strings.NewReader(awakeRule(name))
	if err := write.Run(); err != nil {
		return err
	}
	return exec.Command("sudo", "-n", "chmod", "0644", path).Run()
}

// awakeAllowed is whether hived's service may hold off sleep, asked as the
// service asks: in a transient unit of this user's systemd, with no login
// session. known is false when that could not be asked.
func awakeAllowed() (allowed, known bool) {
	if _, err := exec.LookPath("systemd-inhibit"); err != nil {
		return true, true
	}
	said, took, ran := awakeProbe("systemd-run", "--user", "--quiet", "--wait", "--pipe", "--collect")
	switch {
	case took:
		return true, true
	case ran && refused(said):
		return false, true
	}
	return false, false
}
