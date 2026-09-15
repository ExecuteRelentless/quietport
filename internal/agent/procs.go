package agent

import "strings"

// proc is one running program: its id and the full path of the file it runs from.
type proc struct {
	PID int
	Exe string
}

// ownProcesses picks, out of everything running, the programs belonging to this install: the ones running from the
// app folder itself, never the process doing the asking. Matching is by path, so a Tailscale install elsewhere on
// the computer is left alone and an installer run from anywhere survives its own call (docs/adr/0015).
func ownProcesses(procs []proc, appDir string, selfPID int) []int {
	dir := normPath(appDir)
	if dir == "" {
		return nil
	}
	var out []int
	for _, p := range procs {
		if p.PID > 0 && p.PID != selfPID && under(dir, p.Exe) {
			out = append(out, p.PID)
		}
	}
	return out
}

// leftoverDaemons picks this install's mesh daemons out of everything running. A self-update never stops the daemon
// the previous agent started, so without this the device goes on running the daemon it just replaced. Agents are not
// included: one agent stopping another is a fight, not a handover.
func leftoverDaemons(procs []proc, appDir string) []int {
	dir := normPath(appDir)
	if dir == "" {
		return nil
	}
	var out []int
	for _, p := range procs {
		if p.PID <= 0 || !under(dir, p.Exe) {
			continue
		}
		// Windows reports a running program by its file's current name, and an update renames the running daemon's
		// file to ".prev", or a later update moves that aside to ".prev-<n>" (2026-09-13, Owl): the leftover runs
		// under that name
		name := base(p.Exe)
		if i := strings.Index(name, ".prev"); i > 0 {
			name = name[:i]
		}
		switch name {
		case normPath(daemonName("windows")), "tailscaled.exe": // the current name and the one before docs/adr/0015
			out = append(out, p.PID)
		}
	}
	return out
}

// under reports whether exe sits inside dir, which must already be normalised. The test is on a folder boundary: a
// sibling folder whose name merely starts the same way is a different install.
func under(dir, exe string) bool { return strings.HasPrefix(normPath(exe), dir+"/") }

func base(exe string) string {
	p := normPath(exe)
	return p[strings.LastIndex(p, "/")+1:]
}

// normPath makes two Windows paths comparable: case does not matter there and either separator may appear. This is
// only ever asked about Windows paths; the other platforms stop the agent through launchd or systemd.
func normPath(p string) string {
	return strings.ToLower(strings.TrimRight(strings.ReplaceAll(p, `\`, "/"), "/"))
}
