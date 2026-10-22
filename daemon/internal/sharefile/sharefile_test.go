package sharefile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// hostFile is a file of the host's that a guest would like read or written.
func hostFile(t *testing.T) (string, []byte) {
	t.Helper()
	secret := []byte("the host's own\n")
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, secret, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, secret
}

// trap is something a guest can leave in a share under a name the host
// reads or writes.
type trap struct {
	name  string
	plant func(t *testing.T, at, host string)
}

func traps() []trap {
	return []trap{
		{"a link to a host file", func(t *testing.T, at, host string) { symlink(t, host, at) }},
		{"a link to a device", func(t *testing.T, at, _ string) { symlink(t, "/dev/zero", at) }},
		{"a link to nothing", func(t *testing.T, at, _ string) { symlink(t, "/nonexistent/hive", at) }},
		{"a hard link to a host file", func(t *testing.T, at, host string) {
			if err := os.Link(host, at); err != nil {
				t.Fatal(err)
			}
		}},
		{"a FIFO", func(t *testing.T, at, _ string) {
			if err := unix.Mkfifo(at, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"a directory", func(t *testing.T, at, _ string) {
			if err := os.Mkdir(at, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	}
}

func symlink(t *testing.T, to, at string) {
	t.Helper()
	if err := os.Symlink(to, at); err != nil {
		t.Fatal(err)
	}
}

// soon fails the test if f does not return promptly: a FIFO must never be
// waited on.
func soon(t *testing.T, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked")
	}
}

func TestATrapIsNeverRead(t *testing.T) {
	for _, trap := range traps() {
		t.Run(trap.name, func(t *testing.T) {
			host, _ := hostFile(t)
			at := filepath.Join(t.TempDir(), "exit")
			trap.plant(t, at, host)
			soon(t, func() {
				if raw, err := Read(at, 32); err == nil {
					t.Errorf("Read = %q", raw)
				}
				if raw, err := Tail(at, 32); err == nil {
					t.Errorf("Tail = %q", raw)
				}
				if Present(at) {
					t.Error("Present")
				}
				if err := CopyTail(at, filepath.Join(t.TempDir(), "kept"), 32); err == nil {
					t.Error("CopyTail copied it")
				}
			})
		})
	}
}

func TestADeviceIsNeverRead(t *testing.T) {
	// Opened by its own name, not through a link: what it is decides.
	soon(t, func() {
		if raw, err := Read("/dev/zero", 32); !errors.Is(err, ErrNotResult) {
			t.Errorf("Read(/dev/zero) = %d bytes, %v", len(raw), err)
		}
		if raw, err := Tail("/dev/zero", 32); !errors.Is(err, ErrNotResult) {
			t.Errorf("Tail(/dev/zero) = %d bytes, %v", len(raw), err)
		}
	})
}

func TestAResultIsReadUpToItsLimit(t *testing.T) {
	dir := t.TempDir()
	at := filepath.Join(dir, "exit")
	if err := os.WriteFile(at, []byte("7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if raw, err := Read(at, 32); err != nil || string(raw) != "7\n" {
		t.Fatalf("Read = %q, %v", raw, err)
	}
	if !Present(at) {
		t.Error("a regular file is not Present")
	}
	if err := os.WriteFile(at, bytes.Repeat([]byte("7"), 33), 0o600); err != nil {
		t.Fatal(err)
	}
	if raw, err := Read(at, 32); !errors.Is(err, ErrTooLarge) || raw != nil {
		t.Fatalf("an oversized result: %q, %v", raw, err)
	}
	if raw, err := Read(at, 33); err != nil || len(raw) != 33 {
		t.Fatalf("one at the limit: %d bytes, %v", len(raw), err)
	}
}

func TestOnlyTheEndOfALogIsRead(t *testing.T) {
	dir := t.TempDir()
	at := filepath.Join(dir, "console.log")
	if err := os.WriteFile(at, []byte(strings.Repeat("x", 1000)+"the end"), 0o600); err != nil {
		t.Fatal(err)
	}
	if raw, err := Tail(at, 7); err != nil || string(raw) != "the end" {
		t.Fatalf("Tail = %q, %v", raw, err)
	}
	if raw, err := Tail(at, 1<<20); err != nil || len(raw) != 1007 {
		t.Fatalf("Tail of a short file = %d bytes, %v", len(raw), err)
	}
	kept := filepath.Join(dir, "kept")
	if err := CopyTail(at, kept, 7); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(kept); string(raw) != "the end" {
		t.Fatalf("CopyTail kept %q", raw)
	}
	// Never over something already there.
	if err := CopyTail(at, kept, 7); err == nil {
		t.Error("CopyTail wrote over an existing file")
	}
}

func TestATrapIsNeverWrittenThrough(t *testing.T) {
	for _, trap := range traps() {
		t.Run(trap.name, func(t *testing.T) {
			host, secret := hostFile(t)
			at := filepath.Join(t.TempDir(), "stop-reason")
			trap.plant(t, at, host)
			soon(t, func() {
				if err := WriteFresh(at, []byte("timeout\n")); err != nil {
					t.Fatalf("WriteFresh: %v", err)
				}
			})
			if raw, err := os.ReadFile(host); err != nil || !bytes.Equal(raw, secret) {
				t.Errorf("the host's file is now %q, %v", raw, err)
			}
			info, err := os.Lstat(at)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
				t.Fatalf("what was written: %v, %v", info, err)
			}
			if raw, _ := os.ReadFile(at); string(raw) != "timeout\n" {
				t.Errorf("wrote %q", raw)
			}
		})
	}
}

func TestADirectoryWithSomethingInItIsNotWrittenThrough(t *testing.T) {
	at := filepath.Join(t.TempDir(), "clock")
	if err := os.MkdirAll(filepath.Join(at, "inside"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFresh(at, []byte("1")); err == nil {
		t.Fatal("wrote over a directory that is not empty")
	}
	if _, err := os.Stat(filepath.Join(at, "inside")); err != nil {
		t.Errorf("what was in it: %v", err)
	}
}

func TestOverfullCountsWithoutFollowing(t *testing.T) {
	dir := t.TempDir()
	if Overfull(dir, 2, 10) {
		t.Fatal("an empty directory is overfull")
	}
	if err := os.WriteFile(filepath.Join(dir, "exit"), []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A link to something huge is one name, not its size.
	symlink(t, "/dev/zero", filepath.Join(dir, "big"))
	if Overfull(dir, 2, 10) {
		t.Fatal("two small names are overfull")
	}
	if err := os.WriteFile(filepath.Join(dir, "notices"), bytes.Repeat([]byte("x"), 11), 0o600); err != nil {
		t.Fatal(err)
	}
	if !Overfull(dir, 5, 10) {
		t.Error("eleven bytes are not more than ten")
	}
	if !Overfull(dir, 2, 1<<20) {
		t.Error("three names are not more than two")
	}
}

func TestWhatCannotBeLookedAtIsOverfull(t *testing.T) {
	dir := t.TempDir()
	// A directory the guest took its own permissions from, to hide what
	// it writes beside it.
	hidden := filepath.Join(dir, "0")
	if err := os.Mkdir(hidden, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hidden, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(hidden, 0o700) })
	if !Overfull(dir, 64, 1<<20) {
		t.Error("a directory where results go is not overfull")
	}
	if Overfull(filepath.Join(dir, "missing"), 64, 1<<20) {
		t.Error("nothing at all is overfull")
	}
	link := filepath.Join(t.TempDir(), "out")
	symlink(t, t.TempDir(), link)
	if !Overfull(link, 64, 1<<20) {
		t.Error("a link for where results go was followed")
	}
}

func TestRemoveAllTakesBackWhatTheGuestLockedAndFollowsNothing(t *testing.T) {
	host, secret := hostFile(t)
	before, err := os.Stat(filepath.Dir(host))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "job-1")
	locked := filepath.Join(dir, "out", "d", "e")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "f"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	symlink(t, host, filepath.Join(locked, "secret"))
	symlink(t, filepath.Dir(host), filepath.Join(dir, "out", "hostdir"))
	for _, at := range []string{locked, filepath.Dir(locked)} {
		if err := os.Chmod(at, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("left %v", err)
	}
	if raw, err := os.ReadFile(host); err != nil || !bytes.Equal(raw, secret) {
		t.Errorf("the host's file: %q, %v", raw, err)
	}
	if info, err := os.Stat(filepath.Dir(host)); err != nil || info.Mode() != before.Mode() {
		t.Errorf("the host's directory: %v, %v", info, err)
	}
}
