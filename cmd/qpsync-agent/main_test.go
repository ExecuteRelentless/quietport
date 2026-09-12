package main

import "testing"

// The background agent is a console program launched by a Scheduled Task with an interactive token, so Windows gives
// it a console window: black, empty, and back at every logon. It is not cosmetic. A member who closes it stops their
// own syncing, and the task's only trigger is logon, so nothing starts the agent again (device #33 stayed offline for
// 10 minutes after Nitin closed it, 2026-09-11). Only "run" hides its console; the rest print for whoever ran them.
func TestHidesConsoleOnlyForWindowsRun(t *testing.T) {
	if !hidesConsole("windows", []string{`C:\x\qpsync-agent.exe`, "run"}) {
		t.Fatal("windows run: the console window must be hidden")
	}
	for _, args := range [][]string{
		{"qpsync-agent.exe", "status"},
		{"qpsync-agent.exe", "version"},
		{"qpsync-agent.exe", "install", "--code", "x", "--payload", "y"},
		{"qpsync-agent.exe", "uninstall", "--remove-folder"},
		{"qpsync-agent.exe"},
	} {
		if hidesConsole("windows", args) {
			t.Fatalf("windows %v: this prints for the person who ran it and must keep its console", args[1:])
		}
	}
	if hidesConsole("darwin", []string{"qpsync-agent", "run"}) {
		t.Fatal("darwin: launchd gives it no console window to hide")
	}
}
