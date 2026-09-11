package agent

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// runningProcs lists the processes this account can open, each with the full path of the file it runs from. A process
// that cannot be opened (another account's, or the system's) is left out: it is not ours to stop anyway.
func runningProcs() []proc {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var out []proc
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if path := procPath(e.ProcessID); path != "" {
			out = append(out, proc{PID: int(e.ProcessID), Exe: path})
		}
	}
	return out
}

func procPath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// stopOwnProcesses ends this install's programs and nothing else. It replaces stopping by image name, which after the
// daemon was renamed would both miss our own daemon and match a real Tailscale install (docs/adr/0015).
func stopOwnProcesses(appDir string) { terminate(ownProcesses(runningProcs(), appDir, os.Getpid())) }

// stopLeftoverDaemons ends a daemon the previous version left running, and reports how many it ended.
func stopLeftoverDaemons(appDir string) int {
	pids := leftoverDaemons(runningProcs(), appDir)
	terminate(pids)
	return len(pids)
}

func terminate(pids []int) {
	for _, pid := range pids {
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
		if err != nil {
			continue
		}
		_ = windows.TerminateProcess(h, 1)
		_ = windows.CloseHandle(h)
	}
}
