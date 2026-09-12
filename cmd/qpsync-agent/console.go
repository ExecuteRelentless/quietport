package main

// attachesConsole reports whether this process should take the console of whoever ran it. On Windows the agent is a
// GUI-subsystem program (-H windowsgui in the release builds), so Windows allocates no console for it and the
// Scheduled Task cannot open a window at logon. The cost is that the subcommands a person runs in a terminal are
// given no console either, and anything they print would go nowhere: every subcommand except "run" therefore
// attaches to the caller's console, the usage line included. See docs/adr/0017.
func attachesConsole(goos string, args []string) bool {
	if goos != "windows" {
		return false
	}
	return !(len(args) > 1 && args[1] == "run")
}
