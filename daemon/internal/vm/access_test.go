package vm

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWhyADeviceCouldNotBeOpenedSaysWhatToRun(t *testing.T) {
	for _, test := range []struct {
		name   string
		device deniedDevice
		want   []string
	}{
		{"given to another group", deniedDevice{Path: "/dev/kvm", Group: "render", Listed: true, Holds: true},
			[]string{"belongs to group render, not kvm", "sudo chgrp kvm /dev/kvm"}},
		{"given to another group under Windows", deniedDevice{Path: "/dev/kvm", Group: "systemd-resolve", Listed: true, Holds: true, Windows: true},
			[]string{"given to group systemd-resolve", "another Linux on this PC", "wsl -d HiveWorker -u root /usr/local/sbin/hive-devices"}},
		{"a service older than the membership", deniedDevice{Path: "/dev/vhost-vsock", Group: "kvm", KVM: true, Listed: true, UID: 1000},
			[]string{"started before your user joined the kvm group", "99-hiverunner-$USER.rules", "70-hiverunner-$USER.rules", "sudo setfacl -m u:$USER:rw /dev/kvm /dev/vhost-vsock", "systemctl --user restart hiveworker", "sudo systemctl restart user@1000", "ends every session"}},
		{"a udev rule wiped by uaccess", deniedDevice{Path: "/dev/kvm", Group: "kvm", KVM: true, Listed: true, UID: 1000, Early: "70-hiverunner-seth.rules", Uaccess: true},
			[]string{"70-hiverunner-seth.rules", "73-seat-late.rules", "uaccess", "wipes that ACL when a VM starts", "99-hiverunner-$USER.rules", "rm -f /etc/udev/rules.d/70-hiverunner-$USER.rules"}},
		{"never a member", deniedDevice{Path: "/dev/kvm", Group: "kvm", KVM: true},
			[]string{"your user cannot open /dev/kvm", "sudo usermod -aG kvm $USER"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := test.device.reason()
			for _, want := range test.want {
				if !strings.Contains(got, want) {
					t.Errorf("%q lacks %q", got, want)
				}
			}
			// status and doctor show what follows the first colon as the fix.
			if _, fix, ok := strings.Cut(got, ": "); !ok || !strings.Contains(fix, "`") {
				t.Errorf("no command after a colon in %q", got)
			}
		})
	}
}

func TestAnACLThatUaccessWillWipeIsSaidNotToLast(t *testing.T) {
	stale := deniedDevice{Path: "/dev/kvm", KVM: true, Listed: true, UID: 1000, Uaccess: true}
	got := stale.wontLast()
	for _, want := range []string{"only an ACL", "73-seat-late.rules", "uaccess", "wipes when a VM starts", "99-hiverunner-$USER.rules"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
	if _, fix, ok := strings.Cut(got, ": "); !ok || !strings.Contains(fix, "`") {
		t.Errorf("no command after a colon in %q", got)
	}

	early := stale
	early.Early = "70-hiverunner-seth.rules"
	got = early.wontLast()
	if !strings.Contains(got, "70-hiverunner-seth.rules") || !strings.Contains(got, "until a VM starts") {
		t.Errorf("early rule was not blamed: %q", got)
	}

	for _, test := range []struct {
		name   string
		device deniedDevice
	}{
		{"holds the group", deniedDevice{Path: "/dev/kvm", KVM: true, Listed: true, Holds: true, Uaccess: true}},
		{"a rule after uaccess", deniedDevice{Path: "/dev/kvm", KVM: true, Listed: true, Uaccess: true, Late: true}},
		{"no uaccess", deniedDevice{Path: "/dev/kvm", KVM: true, Listed: true}},
		{"Windows", deniedDevice{Path: "/dev/kvm", KVM: true, Listed: true, Uaccess: true, Windows: true}},
	} {
		if got := test.device.wontLast(); got != "" {
			t.Errorf("%s: %q", test.name, got)
		}
	}
}

func TestHiveRunnerUdevRulesAreClassifiedByNameOrder(t *testing.T) {
	if udevAfterSeatLate("70-hiverunner-seth.rules") {
		t.Fatal("70-hiverunner-seth.rules must run before 73-seat-late.rules")
	}
	if udevAfterSeatLate("73-hiverunner-seth.rules") {
		t.Fatal("73-hiverunner-seth.rules sorts before 73-seat-late.rules")
	}
	if !udevAfterSeatLate("99-hiverunner-seth.rules") {
		t.Fatal("99-hiverunner-seth.rules must run after 73-seat-late.rules")
	}

	files := map[string]string{
		"/usr/lib/udev/rules.d/73-seat-late.rules":   "TAG==\"uaccess\", RUN{builtin}+=\"uaccess\"\n",
		"/etc/udev/rules.d/70-hiverunner-seth.rules": udevACLRuleBody,
	}
	early, late, uaccess := inspectUdev("seth", readFiles(files), listFiles(files))
	if early != "70-hiverunner-seth.rules" || late || !uaccess {
		t.Fatalf("got early=%q late=%v uaccess=%v", early, late, uaccess)
	}

	files["/etc/udev/rules.d/99-hiverunner-seth.rules"] = udevACLRuleBody
	early, late, uaccess = inspectUdev("seth", readFiles(files), listFiles(files))
	if early != "70-hiverunner-seth.rules" || !late || !uaccess {
		t.Fatalf("got early=%q late=%v uaccess=%v", early, late, uaccess)
	}

	early, late, uaccess = inspectUdev("ada", readFiles(files), listFiles(files))
	if early != "" || late || !uaccess {
		t.Fatalf("another user's rules were taken: early=%q late=%v uaccess=%v", early, late, uaccess)
	}

	early, late, uaccess = inspectUdev("seth", readFiles(nil), listFiles(nil))
	if early != "" || late || uaccess {
		t.Fatalf("an empty machine was not empty: early=%q late=%v uaccess=%v", early, late, uaccess)
	}
}

func TestInstallerOrdersTheKvmUdevRuleAfterSeatLate(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "release", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	if !strings.Contains(script, "99-hiverunner-") {
		t.Error("install.sh does not write 99-hiverunner-$USER.rules")
	}
	if !strings.Contains(script, "70-hiverunner-") || !strings.Contains(script, "rm -f") {
		t.Error("install.sh does not remove a 70-hiverunner-$USER.rules of ours")
	}
	if !strings.Contains(script, "73-seat-late") {
		t.Error("install.sh does not say why the rule must be 99-")
	}
	if strings.Contains(script, "tee /etc/udev/rules.d/70-hiverunner") {
		t.Error("install.sh writes a 70- udev rule")
	}
}

func readFiles(files map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		if body, ok := files[path]; ok {
			return []byte(body), nil
		}
		return nil, os.ErrNotExist
	}
}

func listFiles(files map[string]string) func(string) ([]fs.DirEntry, error) {
	return func(dir string) ([]fs.DirEntry, error) {
		if !strings.HasSuffix(dir, "/") {
			dir += "/"
		}
		var entries []fs.DirEntry
		for path := range files {
			if name, ok := strings.CutPrefix(path, dir); ok && !strings.Contains(name, "/") {
				entries = append(entries, fs.FileInfoToDirEntry(file(name)))
			}
		}
		return entries, nil
	}
}
