//go:build !windows

package main

// launchd and systemd hand the agent a log file, and a person running a subcommand in a terminal already has its
// stdout: nothing to attach.
func attachConsole() {}
