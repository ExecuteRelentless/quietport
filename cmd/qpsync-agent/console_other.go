//go:build !windows

package main

// launchd and systemd start the agent with no console window, so there is nothing to hide.
func hideConsole() {}
