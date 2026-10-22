package main

// opensWindow is whether a command line is hived macos image open, the one
// command that draws a window.
func opensWindow(args []string) bool {
	return len(args) > 3 && args[1] == "macos" && args[2] == "image" && args[3] == "open"
}
