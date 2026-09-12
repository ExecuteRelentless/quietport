package main

// hidesConsole reports whether this process should hide the console window Windows gave it. The agent is a console
// program started from a Scheduled Task with an interactive token, so Windows opens a console for it: black, and
// empty because the agent logs to a file. Every other subcommand prints for whoever ran it and keeps its console.
func hidesConsole(goos string, args []string) bool {
	return goos == "windows" && len(args) > 1 && args[1] == "run"
}
