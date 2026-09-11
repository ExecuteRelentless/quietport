//go:build !windows

package agent

// launchd and systemd own the process tree on the other platforms and stop it themselves, so nothing here enumerates.
func runningProcs() []proc { return nil }

func stopOwnProcesses(string) {}

func stopLeftoverDaemons(string) int { return 0 }
