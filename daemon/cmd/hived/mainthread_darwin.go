package main

import (
	"os"
	"runtime"
)

// hived macos image open shows the image's VM in a window, and macOS draws
// windows on the process's main thread only. main starts there if init
// locks it, so it is locked for that command and no other: the daemon and
// every other command run as they would anywhere.
func init() {
	if opensWindow(os.Args) {
		runtime.LockOSThread()
	}
}
