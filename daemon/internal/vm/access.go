package vm

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

// Ubuntu's 73-seat-late.rules runs the uaccess builtin on a change event,
// which a VM starting sends, and replaces the ACL on /dev/kvm. A udev rule
// that sorts before that is wiped; one named 99- runs after it and puts the
// ACL back. Hive Runner's rule is 99-hiverunner-$USER.rules; a 70- file of
// the same stem is ours from before this, and is removed.
const (
	seatLateRule    = "73-seat-late.rules"
	udevRuleDir     = "/etc/udev/rules.d"
	udevACLRuleBody = `KERNEL=="kvm", ACTION=="add|change", RUN+="/usr/bin/setfacl -m u:%s:rw /dev/kvm"\nKERNEL=="vhost-vsock", ACTION=="add|change", RUN+="/usr/bin/setfacl -m u:%s:rw /dev/vhost-vsock"\n`
)

// A device the kvm group opens, and what was found when this user could
// not: /dev/kvm for the VMs, /dev/vhost-vsock for the cache.
type deniedDevice struct {
	Path string
	// Group is the device's group, by name where it has one here.
	Group string
	// KVM is whether the device's group is this system's kvm group.
	KVM bool
	// Listed is whether the user is named in the kvm group, and Holds
	// whether this process has it: a service keeps the groups its user had
	// when the user's services started.
	Listed, Holds bool
	// UID is the user's, for the command that restarts their services.
	UID int
	// User is the login name, for matching a udev rule named
	// NN-hiverunner-$USER.rules.
	User string
	// Windows is whether this Linux is one inside Windows' Linux subsystem,
	// where every Linux shares /dev and there is no password for sudo.
	Windows bool
	// Early is a udev rule of ours that grants this user the device, but
	// sorts before 73-seat-late.rules, so uaccess wipes that ACL.
	Early string
	// Late is whether a rule of ours sorts after that.
	Late bool
	// Uaccess is whether this system runs Ubuntu's (systemd's) uaccess on
	// change events.
	Uaccess bool
}

// reason is why the device could not be opened, and after a colon what to
// run. Which of the things it is decides the command: the device was given
// to another group, the service is older than the user's membership, an
// early udev rule was wiped, or the user was never a member.
func (d deniedDevice) reason() string {
	switch {
	case !d.KVM && d.Windows:
		// Every Linux under Windows shares one /dev, and the group on a
		// device is a number: another Linux starting gives it the number
		// its own kvm group has.
		return fmt.Sprintf("%s was given to group %s, not kvm, by another Linux on this PC: from Windows, run `wsl -d HiveWorker -u root /usr/local/sbin/hive-devices`", d.Path, d.Group)
	case !d.KVM:
		return fmt.Sprintf("%s belongs to group %s, not kvm: run `sudo chgrp kvm %s`", d.Path, d.Group, d.Path)
	case d.Listed && !d.Holds:
		return d.staleGroups(true)
	default:
		return fmt.Sprintf("your user cannot open %s: run `sudo usermod -aG kvm $USER`, then log out and back in", d.Path)
	}
}

// wontLast is why an ACL that lets this process open the device will not
// last, or "". The process is not in the kvm group, so a VM starting sends
// a change event, uaccess wipes the ACL, and the next open fails. A 99-
// udev rule puts it back; a 70- rule of ours does not.
func (d deniedDevice) wontLast() string {
	if !d.KVM || !d.Listed || d.Holds || d.Windows || d.Late {
		return ""
	}
	if d.Early == "" && !d.Uaccess {
		return ""
	}
	return d.staleGroups(false)
}

// staleGroups is the command for a user who is in the kvm group when this
// process is not. denied is whether the device could not be opened at all,
// rather than opened only by an ACL that will be wiped.
func (d deniedDevice) staleGroups(denied bool) string {
	fix := aclFix()
	switch {
	case d.Early != "":
		until := d.Path
		if !denied {
			until = d.Path + " until a VM starts"
		}
		return fmt.Sprintf("your udev rule /etc/udev/rules.d/%s grants %s, but Ubuntu's 73-seat-late.rules (uaccess) runs after it and wipes that ACL when a VM starts: run %s, or restart the machine. (`sudo systemctl restart user@%d` works too, but ends every session of yours)", d.Early, until, fix, d.UID)
	case denied:
		// Restarting the user's services would do it, and would end every
		// session they have, an agent's in tmux included. A udev rule that
		// runs after uaccess ends nothing; it lasts until the machine
		// restarts, by when the group is enough.
		return fmt.Sprintf("your user's services started before your user joined the kvm group, so cannot open %s: run %s, or restart the machine. (`sudo systemctl restart user@%d` works too, but ends every session of yours)", d.Path, fix, d.UID)
	default:
		return fmt.Sprintf("your user's services started before your user joined the kvm group, so %s is only an ACL that Ubuntu's 73-seat-late.rules (uaccess) wipes when a VM starts: run %s, or restart the machine. (`sudo systemctl restart user@%d` works too, but ends every session of yours)", d.Path, fix, d.UID)
	}
}

// aclFix is the command that writes Hive Runner's udev rule after
// 73-seat-late.rules, removes a 70- rule of ours, lends the devices now,
// and restarts only the daemon.
func aclFix() string {
	return "`printf '" + udevACLRuleBody + "' \"$USER\" \"$USER\" | sudo tee " + udevRuleDir + "/99-hiverunner-$USER.rules >/dev/null && sudo rm -f " + udevRuleDir + "/70-hiverunner-$USER.rules && sudo setfacl -m u:$USER:rw /dev/kvm /dev/vhost-vsock; systemctl --user restart hiveworker`"
}

// inspectUdev looks at this system's udev rules for uaccess and for a
// Hive Runner rule that grants user the devices. read is os.ReadFile and
// list os.ReadDir, given here so a test can be any machine.
func inspectUdev(user string, read func(string) ([]byte, error), list func(string) ([]fs.DirEntry, error)) (early string, late, uaccess bool) {
	for _, dir := range []string{"/usr/lib/udev/rules.d", "/lib/udev/rules.d"} {
		body, err := read(dir + "/" + seatLateRule)
		if err == nil {
			if strings.Contains(string(body), "uaccess") {
				uaccess = true
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			uaccess = true
			break
		}
	}
	if user == "" {
		return "", false, uaccess
	}
	entries, err := list(udevRuleDir)
	if err != nil {
		return "", false, uaccess
	}
	suffix := "-hiverunner-" + user + ".rules"
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, suffix) {
			continue
		}
		if udevAfterSeatLate(name) {
			late = true
			continue
		}
		if early == "" {
			early = name
		}
	}
	return early, late, uaccess
}

// udevAfterSeatLate is whether udev will run this rule after
// 73-seat-late.rules: it sorts the files by name.
func udevAfterSeatLate(name string) bool {
	return name > seatLateRule
}
