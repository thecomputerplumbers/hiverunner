package main

import "runtime"

// main runs on the process's main thread, which is the only one macOS draws
// windows on: hived macos image open shows the image's VM in one. Nothing
// else hived does minds where its main goroutine runs.
func init() { runtime.LockOSThread() }
