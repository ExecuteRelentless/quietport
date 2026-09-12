package main

import "testing"

// The background agent is started by a Scheduled Task with an interactive token. Built as a console program it was
// handed a black, empty console window at every logon, and closing that window killed the agent: device #33 sat
// offline for 10 minutes after it was closed (2026-09-11). On Windows the agent is therefore built with
// -H windowsgui, so Windows never allocates a console for it. The cost is that the subcommands a person runs in a
// terminal lose their console too, so every one of them except "run" attaches to the console of whoever ran it.
// (Hiding the window from inside the process, 0.1.22, did not work on Windows 11: docs/adr/0017.)
func TestEverySubcommandExceptRunPrintsForItsCaller(t *testing.T) {
	for _, args := range [][]string{
		{`C:\x\qpsync-agent.exe`, "status"},
		{`C:\x\qpsync-agent.exe`, "version"},
		{`C:\x\qpsync-agent.exe`, "install", "--code", "x", "--payload", "y"},
		{`C:\x\qpsync-agent.exe`, "uninstall", "--remove-folder"},
		{`C:\x\qpsync-agent.exe`}, // no subcommand: the usage line has to print as well
	} {
		if !printsForCaller("windows", args) {
			t.Errorf("windows %v: this prints for the person who ran it and needs their console", args[1:])
		}
	}
	if printsForCaller("windows", []string{`C:\x\qpsync-agent.exe`, "run"}) {
		t.Error("windows run: the Scheduled Task starts this one, and taking a console would put the window back")
	}
	if printsForCaller("darwin", []string{"qpsync-agent", "status"}) {
		t.Error("darwin: the program is handed the terminal's stdout already, there is nothing to attach")
	}
}
